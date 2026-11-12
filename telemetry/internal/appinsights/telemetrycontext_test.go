package appinsights

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

type channelTestTransmitter struct {
	sent chan int
}

func (t channelTestTransmitter) transmit(_ context.Context, items []batchItem) (*transmissionResponse, error) {
	t.sent <- len(items)
	return &transmissionResponse{statusCode: http.StatusOK}, nil
}

func TestDefaultTags(t *testing.T) {
	context := newTelemetryContext(test_ikey)
	context.Tags["test"] = "OK"
	context.Tags["no-write"] = "Fail"

	data := contracts.EventData{
		Name: "Hello world.",
		Ver:  2,
	}

	envelope := context.envelop(data)

	if envelope.Tags["test"] != "OK" {
		t.Error("Default client tags did not propagate to telemetry")
	}

	if envelope.Tags["no-write"] != "Fail" {
		t.Error("Default client tag did not propagate to telemetry")
	}
}

func TestSanitize(t *testing.T) {
	name := strings.Repeat("Z", 1024)

	data := contracts.EventData{
		Name: name,
		Ver:  2,
	}

	ctx := newTelemetryContext(test_ikey)

	// We'll be looking for messages with these values:
	found := map[string]int{
		"EventData.Name exceeded": 0,
	}

	// This may break due to hardcoded limits... Check contracts.
	envelope := ctx.envelop(data)

	err := envelope.Sanitize()
	msg := err.Error()
	for k := range found {
		if strings.Contains(msg, k) {
			found[k] = found[k] + 1
		}
	}

	// Make sure all the warnings were found in the output
	for k, v := range found {
		if v != 1 {
			t.Errorf("Did not find a warning containing %q", k)
		}
	}

	evdata := envelope.Data.BaseData
	if evdata.Name != name[:512] {
		t.Error("Event name was not truncated")
	}
}

func TestSanitizeProperties(t *testing.T) {
	longKey := strings.Repeat("X", 300)
	longValue := strings.Repeat("Z", 9000)

	data := contracts.EventData{
		Name: "Test",
		Ver:  2,
		Properties: map[string]string{
			longKey: longValue,
			"ok":    "OK",
		},
	}

	ctx := newTelemetryContext(test_ikey)

	found := map[string]int{
		"EventData.Properties has value with length exceeding max": 0,
		"EventData.Properties has key with length exceeding max":   0,
	}

	envelope := ctx.envelop(data)

	err := envelope.Sanitize()
	msg := err.Error()
	for k := range found {
		if strings.Contains(msg, k) {
			found[k] = found[k] + 1
		}
	}

	for k, v := range found {
		if v != 1 {
			t.Errorf("Did not find a warning containing %q", k)
		}
	}

	ps := envelope.Data.BaseData.Properties
	if _, ok := ps[longKey]; ok {
		t.Error("Property with long key was not removed")
	}
	if v, ok := ps[longKey[:150]]; ok {
		if v != longValue[:8192] {
			t.Error("Property value was not truncated")
		}
	} else {
		t.Error("Property with truncated key was not added")
	}
	if ps["ok"] != "OK" {
		t.Error("Property with short key was changed")
	}
}

func TestRetryAfterSupportsDeltaSeconds(t *testing.T) {
	now := time.Now()
	got, err := parseRetryAfter("30", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(30 * time.Second); !got.Equal(want) {
		t.Errorf("parsed retry time = %v, want %v", got, want)
	}
}

func TestCompressedPartialResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusPartialContent)
		var body bytes.Buffer
		writer := gzip.NewWriter(&body)
		if _, err := io.WriteString(writer, `{"itemsReceived":2,"itemsAccepted":1,"errors":[{"index":1,"statusCode":500}]}`); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		if _, err := w.Write(body.Bytes()); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	item := batchItem{
		item: newTelemetryContext(test_ikey).envelop(contracts.EventData{Name: "event", Ver: 2}),
	}
	response, err := newTransmitter(server.URL, server.Client()).transmit(t.Context(), []batchItem{item, item})
	if err != nil {
		t.Fatalf("transmit rejected a valid compressed response: %v", err)
	}
	if response.response.ItemsAccepted != 1 || len(response.response.Errors) != 1 {
		t.Errorf("decoded response = %+v, want one accepted item and one error", response.response)
	}
}

func TestPartialResponseCountsAcceptedItems(t *testing.T) {
	response := &transmissionResponse{
		statusCode: http.StatusPartialContent,
		response: contracts.BackendResponse{
			ItemsReceived: 3,
			ItemsAccepted: 2,
			Errors: []contracts.BackendResponseError{
				{Index: 1, StatusCode: http.StatusInternalServerError},
			},
		},
	}
	succeeded, failed, retries := response.result(make([]batchItem, 3))
	if succeeded != 2 || failed != 0 || len(retries) != 1 {
		t.Errorf("result = (%d succeeded, %d failed, %d retries), want (2, 0, 1)", succeeded, failed, len(retries))
	}
}

func TestThrottleExpiryFlushesEventsAfterEmptyRetry(t *testing.T) {
	channel := newInMemoryChannel("https://example.invalid", 1, time.Hour, nil, nil)
	sent := make(chan int, 1)
	channel.transmitter = channelTestTransmitter{sent: sent}
	go channel.acceptLoop()
	defer channel.stop()

	retryAt := time.Now().Add(250 * time.Millisecond)
	channel.setThrottle(retryAt)
	if !channel.retry(true, retryAt, nil) {
		t.Fatal("could not enqueue empty throttled retry")
	}
	channel.send(newTelemetryContext(test_ikey).envelop(contracts.EventData{Name: "event", Ver: 2}))

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	channel.close(ctx)
	if err := ctx.Err(); err != nil {
		t.Fatalf("Close waited for its context instead of flushing after throttle expiry: %v", err)
	}
	select {
	case count := <-sent:
		if count != 1 {
			t.Errorf("transmitted %d items, want 1", count)
		}
	default:
		t.Fatal("buffered event was not transmitted")
	}
}

func TestExpiredThrottleRetryDoesNotRestoreThrottle(t *testing.T) {
	channel := newInMemoryChannel("https://example.invalid", 1, time.Hour, nil, nil)
	sent := make(chan int, 1)
	channel.transmitter = channelTestTransmitter{sent: sent}
	go channel.acceptLoop()
	defer channel.stop()

	retryAt := time.Now().Add(-time.Second)
	channel.setThrottle(retryAt)
	if !channel.waitUntilUnthrottled() {
		t.Fatal("expired throttle unexpectedly blocked transmission")
	}
	if !channel.retry(true, retryAt, nil) {
		t.Fatal("could not enqueue expired throttled retry")
	}
	channel.send(newTelemetryContext(test_ikey).envelop(contracts.EventData{Name: "event", Ver: 2}))

	select {
	case count := <-sent:
		if count != 1 {
			t.Errorf("transmitted %d items, want 1", count)
		}
	case <-time.After(time.Second):
		t.Fatal("event remained buffered after the throttle deadline")
	}
}
