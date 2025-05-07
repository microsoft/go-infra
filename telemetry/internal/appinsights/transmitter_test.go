package appinsights

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

func TestBasicTransmit(t *testing.T) {
	client, server := newTestClientServer(t)
	defer server.Close()
	defer client.Close(t.Context())

	var errbuf bytes.Buffer
	client.ErrorLog = log.New(&errbuf, "", 0)
	client.MaxBatchSize = 3

	server.responseData = []byte(`{"itemsReceived":3, "itemsAccepted":3, "errors":[]}`)
	server.responseHeaders["Content-type"] = "application/json"
	client.TrackEvent("foobar0")
	client.TrackEvent("foobar1")
	client.TrackEvent("foobar2")
	req := server.waitForRequest(t)

	if errbuf.Len() != 0 {
		t.Errorf("err: %s", errbuf.String())
	}

	if req.request.Method != "POST" {
		t.Error("request.Method")
	}

	cencoding := req.request.Header[http.CanonicalHeaderKey("Content-Encoding")]
	if len(cencoding) != 1 || cencoding[0] != "gzip" {
		t.Errorf("content-encoding: %q", cencoding)
	}

	// Check for gzip magic number
	if len(req.body) < 2 || req.body[0] != 0x1f || req.body[1] != 0x8b {
		t.Fatal("missing gzip magic number")
	}

	// Decompress payload
	reader, err := gzip.NewReader(bytes.NewReader(req.body))
	if err != nil {
		t.Fatalf("Couldn't create gzip reader: %s", err.Error())
	}

	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatalf("Couldn't read compressed data: %s", err.Error())
	}

	if !bytes.Contains(body, []byte("foobar")) {
		t.Error("missing foobar")
	}

	ctype := req.request.Header[http.CanonicalHeaderKey("Content-Type")]
	if len(ctype) != 1 || ctype[0] != "application/x-json-stream" {
		t.Errorf("content-type: %q", ctype)
	}
}

func TestFailedTransmit(t *testing.T) {
	client, server := newTestClientServer(t)
	defer server.Close()

	var errbuf bytes.Buffer
	client.ErrorLog = log.New(&errbuf, "", 0)
	client.MaxBatchSize = 3

	server.responseCode = 403
	server.responseData = []byte(`{"itemsReceived":3, "itemsAccepted":0, "errors":[{"index": 2, "statusCode": 403, "message": "Hello"}]}`)
	server.responseHeaders["Content-type"] = "application/json"
	client.TrackEvent("foobar0")
	client.TrackEvent("foobar1")
	client.TrackEvent("foobar2")
	client.Close(t.Context())

	errstr := errbuf.String()
	if errstr != "Failed to transmit payload: code=403, received=3, accepted=0, canRetry=false, throttled=false\n" {
		t.Errorf("unexpected error: %s", errstr)
	}
}

type resultProperties struct {
	isSuccess        bool
	isFailure        bool
	canRetry         bool
	isThrottled      bool
	isPartialSuccess bool
	retryableErrors  bool
}

func checkTransmitResult(t *testing.T, result *transmissionResult, expected *resultProperties) {
	retryAfter := "<nil>"
	if !result.retryAfter.IsZero() {
		retryAfter = result.retryAfter.String()
	}
	id := fmt.Sprintf("%d, retryAfter:%s, response:%v", result.statusCode, retryAfter, result.response)

	if result.isSuccess() != expected.isSuccess {
		t.Errorf("Expected IsSuccess() == %t [%s]", expected.isSuccess, id)
	}

	if result.isFailure() != expected.isFailure {
		t.Errorf("Expected IsFailure() == %t [%s]", expected.isFailure, id)
	}

	if result.canRetry() != expected.canRetry {
		t.Errorf("Expected CanRetry() == %t [%s]", expected.canRetry, id)
	}

	if result.isThrottled() != expected.isThrottled {
		t.Errorf("Expected IsThrottled() == %t [%s]", expected.isThrottled, id)
	}

	if result.isPartialSuccess() != expected.isPartialSuccess {
		t.Errorf("Expected IsPartialSuccess() == %t [%s]", expected.isPartialSuccess, id)
	}

	// retryableErrors is true if CanRetry() and any error is recoverable
	retryableErrors := false
	if result.canRetry() {
		for _, err := range result.response.Errors {
			if err.canRetry() {
				retryableErrors = true
			}
		}
	}

	if retryableErrors != expected.retryableErrors {
		t.Errorf("Expected any(Errors.CanRetry) == %t [%s]", expected.retryableErrors, id)
	}
}

func TestTransmitResults(t *testing.T) {
	retryAfter := time.Unix(1502322237, 0)
	partialNoRetries := backendResponse{
		ItemsAccepted: 3,
		ItemsReceived: 5,
		Errors: []itemTransmissionResult{
			{Index: 2, StatusCode: 400, Message: "Bad 1"},
			{Index: 4, StatusCode: 400, Message: "Bad 2"},
		},
	}

	partialSomeRetries := backendResponse{
		ItemsAccepted: 2,
		ItemsReceived: 4,
		Errors: []itemTransmissionResult{
			{Index: 2, StatusCode: 400, Message: "Bad 1"},
			{Index: 4, StatusCode: 408, Message: "OK Later"},
		},
	}

	noneAccepted := backendResponse{
		ItemsAccepted: 0,
		ItemsReceived: 5,
		Errors: []itemTransmissionResult{
			{Index: 0, StatusCode: 500, Message: "Bad 1"},
			{Index: 1, StatusCode: 500, Message: "Bad 2"},
			{Index: 2, StatusCode: 500, Message: "Bad 3"},
			{Index: 3, StatusCode: 500, Message: "Bad 4"},
			{Index: 4, StatusCode: 500, Message: "Bad 5"},
		},
	}

	allAccepted := backendResponse{
		ItemsAccepted: 6,
		ItemsReceived: 6,
		Errors:        make([]itemTransmissionResult, 0),
	}

	checkTransmitResult(t, &transmissionResult{200, time.Time{}, allAccepted},
		&resultProperties{isSuccess: true})
	checkTransmitResult(t, &transmissionResult{206, time.Time{}, partialSomeRetries},
		&resultProperties{isPartialSuccess: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResult{206, time.Time{}, partialNoRetries},
		&resultProperties{isPartialSuccess: true, canRetry: true})
	checkTransmitResult(t, &transmissionResult{206, time.Time{}, noneAccepted},
		&resultProperties{isPartialSuccess: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResult{206, time.Time{}, allAccepted},
		&resultProperties{isSuccess: true})
	checkTransmitResult(t, &transmissionResult{400, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true})
	checkTransmitResult(t, &transmissionResult{408, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResult{408, retryAfter, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResult{429, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResult{429, retryAfter, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResult{500, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResult{503, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResult{401, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true})
	checkTransmitResult(t, &transmissionResult{408, time.Time{}, partialSomeRetries},
		&resultProperties{isFailure: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResult{500, time.Time{}, partialSomeRetries},
		&resultProperties{isFailure: true, canRetry: true, retryableErrors: true})
}

func TestGetRetryItems(t *testing.T) {
	originalItems := telemetryItems()
	for i := range 7 {
		addEventData(&originalItems, contracts.EventData{Name: fmt.Sprintf("msg%d", i+1), Ver: 2})
	}

	res1 := &transmissionResult{
		statusCode: 200,
		response:   backendResponse{ItemsReceived: 7, ItemsAccepted: 7},
	}

	items1 := res1.getRetryItems(slices.Clone(originalItems))
	if len(items1) > 0 {
		t.Error("GetRetryItems shouldn't return anything")
	}

	res2 := &transmissionResult{statusCode: 408}

	items2 := res2.getRetryItems(slices.Clone(originalItems))
	if !reflect.DeepEqual(originalItems, items2) {
		t.Error("GetRetryItems shouldn't return anything")
	}

	res3 := &transmissionResult{
		statusCode: 206,
		response: backendResponse{
			ItemsReceived: 7,
			ItemsAccepted: 4,
			Errors: []itemTransmissionResult{
				{Index: 1, StatusCode: 200, Message: "OK"},
				{Index: 3, StatusCode: 400, Message: "Bad"},
				{Index: 5, StatusCode: 408, Message: "Later"},
				{Index: 6, StatusCode: 500, Message: "Oops"},
			},
		},
	}

	items3 := res3.getRetryItems(slices.Clone(originalItems))
	expected3 := []batchItem{originalItems[5], originalItems[6]}
	if !reflect.DeepEqual(expected3, items3) {
		t.Error("Unexpected result")
	}
}
