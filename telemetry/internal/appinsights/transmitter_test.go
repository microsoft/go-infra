//go:build goexperiment.synctest

package appinsights

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestBasicTransmit(t *testing.T) {
	synctest.Run(func() {
		events := []string{"foobar0", "foobar1", "foobar2"}
		client, server := newTestClientServer(t, serverResponse{
			statusCode: http.StatusOK,
			body: backendResponse{
				ItemsReceived: 3,
				ItemsAccepted: 3,
			},
			want: envelopes(events...),
		})
		defer server.Close()

		var errbuf bytes.Buffer
		client.ErrorLog = log.New(&errbuf, "", 0)

		for _, event := range events {
			client.TrackEvent(event)
		}

		client.Close(t.Context())

		if errbuf.Len() != 0 {
			t.Errorf("err: %s", errbuf.String())
		}
	})
}

func TestFailedTransmit(t *testing.T) {
	synctest.Run(func() {
		events := []string{"foobar0", "foobar1", "foobar2"}
		client, server := newTestClientServer(t, serverResponse{
			statusCode: http.StatusForbidden,
			body: backendResponse{
				ItemsReceived: 3,
				ItemsAccepted: 0,
				Errors: []itemTransmissionResult{
					{Index: 2, StatusCode: 403},
				},
			},
			want: envelopes(events...),
		})
		defer server.Close()

		var errbuf bytes.Buffer
		client.ErrorLog = log.New(&errbuf, "", 0)
		for _, event := range events {
			client.TrackEvent(event)
		}
		client.Close(t.Context())

		errstr := errbuf.String()
		if errstr != "Failed to transmit payload: code=403, received=3, accepted=0, canRetry=false, throttled=false\n" {
			t.Errorf("unexpected error: %s", errstr)
		}
	})
}

type resultProperties struct {
	isSuccess        bool
	isFailure        bool
	canRetry         bool
	isThrottled      bool
	isPartialSuccess bool
	retryableErrors  bool
}

func checkTransmitResult(t *testing.T, result *transmissionResponse, expected *resultProperties) {
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

	checkTransmitResult(t, &transmissionResponse{200, time.Time{}, allAccepted},
		&resultProperties{isSuccess: true})
	checkTransmitResult(t, &transmissionResponse{206, time.Time{}, partialSomeRetries},
		&resultProperties{isPartialSuccess: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResponse{206, time.Time{}, partialNoRetries},
		&resultProperties{isPartialSuccess: true, canRetry: true})
	checkTransmitResult(t, &transmissionResponse{206, time.Time{}, noneAccepted},
		&resultProperties{isPartialSuccess: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResponse{206, time.Time{}, allAccepted},
		&resultProperties{isSuccess: true})
	checkTransmitResult(t, &transmissionResponse{400, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true})
	checkTransmitResult(t, &transmissionResponse{408, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResponse{408, retryAfter, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResponse{429, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResponse{429, retryAfter, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true, isThrottled: true})
	checkTransmitResult(t, &transmissionResponse{500, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResponse{503, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true, canRetry: true})
	checkTransmitResult(t, &transmissionResponse{401, time.Time{}, backendResponse{}},
		&resultProperties{isFailure: true})
	checkTransmitResult(t, &transmissionResponse{408, time.Time{}, partialSomeRetries},
		&resultProperties{isFailure: true, canRetry: true, retryableErrors: true})
	checkTransmitResult(t, &transmissionResponse{500, time.Time{}, partialSomeRetries},
		&resultProperties{isFailure: true, canRetry: true, retryableErrors: true})
}

func TestGetRetryItems(t *testing.T) {
	originalItems := batchItems()
	for i := range 7 {
		name := fmt.Sprintf("msg%d", i+1)
		originalItems = append(originalItems, batchItems(name)...)
	}

	res1 := &transmissionResponse{
		statusCode: 200,
		response:   backendResponse{ItemsReceived: 7, ItemsAccepted: 7},
	}

	items1 := res1.getRetryItems(slices.Clone(originalItems))
	if len(items1) > 0 {
		t.Error("GetRetryItems shouldn't return anything")
	}

	res2 := &transmissionResponse{statusCode: 408}

	items2 := res2.getRetryItems(slices.Clone(originalItems))
	if !reflect.DeepEqual(originalItems, items2) {
		t.Error("GetRetryItems shouldn't return anything")
	}

	res3 := &transmissionResponse{
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
