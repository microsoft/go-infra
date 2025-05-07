//go:build goexperiment.synctest

package appinsights

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func slowTick(seconds int) {
	for range seconds {
		time.Sleep(1*time.Second + 5*time.Millisecond)
		synctest.Wait()
	}
}

const ten_seconds = 10 * time.Second

type testTransmitter struct {
	requests  chan *testTransmission
	responses chan *transmissionResult
}

func (transmitter *testTransmitter) transmit(ctx context.Context, payload []byte, items []batchItem) (*transmissionResult, error) {
	itemsCopy := make([]batchItem, len(items))
	copy(itemsCopy, items)

	transmitter.requests <- &testTransmission{
		payload:   string(payload),
		items:     itemsCopy,
		timestamp: time.Now(),
	}

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case resp := <-transmitter.responses:
		return resp, nil
	}
}

func (transmitter *testTransmitter) Close() {
	close(transmitter.requests)
	close(transmitter.responses)
}

func (transmitter *testTransmitter) prepResponse(statusCodes ...int) {
	for _, code := range statusCodes {
		transmitter.responses <- &transmissionResult{statusCode: code}
	}
}

func (transmitter *testTransmitter) prepThrottle(after time.Duration) time.Time {
	retryAfter := time.Now().Add(after)

	transmitter.responses <- &transmissionResult{
		statusCode: 408,
		retryAfter: retryAfter,
	}

	return retryAfter
}

func (transmitter *testTransmitter) waitForRequest(t *testing.T) *testTransmission {
	t.Helper()
	return <-transmitter.requests
}

func (transmitter *testTransmitter) assertNoRequest(t *testing.T) {
	t.Helper()
	select {
	case <-transmitter.requests:
		t.Fatal("Expected no request")
	case <-time.After(time.Duration(10) * time.Millisecond):
		return
	}
}

type testTransmission struct {
	timestamp time.Time
	payload   string
	items     []batchItem
}

func newTestChannelServer(client *Client) (*Client, *testTransmitter) {
	transmitter := &testTransmitter{
		requests:  make(chan *testTransmission, 16),
		responses: make(chan *transmissionResult, 16),
	}

	if client == nil {
		client = &Client{
			MaxBatchInterval: ten_seconds, // assumed by every test
		}
	}
	client.InstrumentationKey = test_ikey
	client.init()

	client.channel.transmitter = transmitter

	return client, transmitter
}

func assertTimeApprox(t *testing.T, x, y time.Time) {
	t.Helper()
	const delta = (time.Duration(100) * time.Millisecond)
	if (x.Before(y) && y.Sub(x) > delta) || (y.Before(x) && x.Sub(y) > delta) {
		t.Errorf("Time isn't a close match: %v vs %v", x, y)
	}
}

func TestSimpleSubmit(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		client.TrackEvent("~msg~")
		tm := time.Now()
		transmitter.prepResponse(200)

		slowTick(11)
		req := transmitter.waitForRequest(t)

		assertTimeApprox(t, req.timestamp, tm.Add(ten_seconds))

		if !strings.Contains(string(req.payload), "~msg~") {
			t.Errorf("Payload does not contain message")
		}
	})
}

func TestMultipleSubmit(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		transmitter.prepResponse(200, 200)

		start := time.Now()

		for i := range 16 {
			client.TrackEvent(fmt.Sprintf("~msg-%x~", i))
			slowTick(1)
		}

		slowTick(10)

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, start.Add(ten_seconds))

		for i := range 10 {
			if !strings.Contains(req1.payload, fmt.Sprintf("~msg-%x~", i)) {
				t.Errorf("Payload does not contain expected item: %x", i)
			}
		}

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, start.Add(ten_seconds+ten_seconds))

		for i := 10; i < 16; i++ {
			if !strings.Contains(req2.payload, fmt.Sprintf("~msg-%x~", i)) {
				t.Errorf("Payload does not contain expected item: %x", i)
			}
		}
	})
}

func TestFlush(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		transmitter.prepResponse(200, 200)

		// Empty flush should do nothing
		client.Flush()

		tm := time.Now()
		client.TrackEvent("~msg~")
		client.Flush()

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, tm)
		if !strings.Contains(req1.payload, "~msg~") {
			t.Error("Unexpected payload")
		}

		// Next one goes back to normal
		client.TrackEvent("~next~")
		slowTick(11)

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, tm.Add(ten_seconds))
		if !strings.Contains(req2.payload, "~next~") {
			t.Error("Unexpected payload")
		}
	})
}

func TestStop(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		transmitter.prepResponse(200)

		client.TrackEvent("Not sent")
		client.Stop()
		slowTick(20)
		transmitter.assertNoRequest(t)
	})
}

func TestCloseFlush(t *testing.T) {
	client, transmitter := newTestChannelServer(nil)
	defer func() {
		client.Close(t.Context())
		transmitter.Close()
	}()

	transmitter.prepResponse(200)

	client.TrackEvent("~flushed~")
	client.Close(t.Context())

	req := transmitter.waitForRequest(t)
	if !strings.Contains(req.payload, "~flushed~") {
		t.Error("Unexpected payload")
	}
}

func TestCloseFlushRetry(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer transmitter.Close()

		transmitter.prepResponse(500, 200)

		client.TrackEvent("~flushed~")
		client.Flush()

		tm := time.Now()

		slowTick(30)

		client.Close(t.Context())

		req1 := transmitter.waitForRequest(t)
		if !strings.Contains(req1.payload, "~flushed~") {
			t.Error("Unexpected payload")
		}

		assertTimeApprox(t, req1.timestamp, tm)

		req2 := transmitter.waitForRequest(t)
		if !strings.Contains(req2.payload, "~flushed~") {
			t.Error("Unexpected payload")
		}

		assertTimeApprox(t, req2.timestamp, tm.Add(client.MaxBatchInterval))
	})
}

func TestSendOnBufferFull(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(&Client{MaxBatchSize: 4})
		defer client.Close(t.Context())

		transmitter.prepResponse(200, 200)

		for i := range 5 {
			client.TrackEvent(fmt.Sprintf("~msg-%d~", i))
		}

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, time.Now())

		for i := range 4 {
			if !strings.Contains(req1.payload, fmt.Sprintf("~msg-%d~", i)) || len(req1.items) != 4 {
				t.Errorf("Payload does not contain expected message")
			}
		}

		slowTick(5)
		transmitter.assertNoRequest(t)
		slowTick(5)

		// The last one should have gone out as normal

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, time.Now())
		if !strings.Contains(req2.payload, "~msg-4~") || len(req2.items) != 1 {
			t.Errorf("Payload does not contain expected message")
		}
	})
}

func TestRetryOnFailure(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		transmitter.prepResponse(500, 200)

		client.TrackEvent("~msg-1~")
		client.TrackEvent("~msg-2~")

		tm := time.Now()
		slowTick(10)

		req1 := transmitter.waitForRequest(t)
		if !strings.Contains(req1.payload, "~msg-1~") || !strings.Contains(req1.payload, "~msg-2~") || len(req1.items) != 2 {
			t.Error("Unexpected payload")
		}

		assertTimeApprox(t, req1.timestamp, tm.Add(ten_seconds))

		slowTick(30)

		req2 := transmitter.waitForRequest(t)
		if req2.payload != req1.payload || len(req2.items) != 2 {
			t.Error("Unexpected payload")
		}

		assertTimeApprox(t, req2.timestamp, tm.Add(ten_seconds).Add(client.MaxBatchInterval))
	})
}

func TestPartialRetry(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(nil)
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		client.TrackEvent("~ok-1~")
		client.TrackEvent("~retry-1~")
		client.TrackEvent("~ok-2~")
		client.TrackEvent("~bad-1~")
		client.TrackEvent("~retry-2~")

		transmitter.responses <- &transmissionResult{
			statusCode: 206,
			response: backendResponse{
				ItemsAccepted: 2,
				ItemsReceived: 5,
				Errors: []itemTransmissionResult{
					{Index: 1, StatusCode: 500, Message: "Server Error"},
					{Index: 2, StatusCode: 200, Message: "OK"},
					{Index: 3, StatusCode: 400, Message: "Bad Request"},
					{Index: 4, StatusCode: 408, Message: "Plz Retry"},
				},
			},
		}

		transmitter.prepResponse(200)

		tm := time.Now()
		slowTick(30)

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, tm.Add(ten_seconds))
		if len(req1.items) != 5 {
			t.Error("Unexpected payload")
		}

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, tm.Add(ten_seconds).Add(client.MaxBatchInterval))
		if len(req2.items) != 2 {
			t.Error("Unexpected payload")
		}

		if strings.Contains(req2.payload, "~ok-") || strings.Contains(req2.payload, "~bad-") || !strings.Contains(req2.payload, "~retry-") {
			t.Error("Unexpected payload")
		}
	})
}

func TestThrottleDropsMessages(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(&Client{MaxBatchSize: 4})
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		tm := time.Now()
		retryAfter := transmitter.prepThrottle(time.Minute)
		transmitter.prepResponse(200, 200)

		client.TrackEvent("~throttled~")
		slowTick(10)

		for i := range 20 {
			client.TrackEvent(fmt.Sprintf("~msg-%d~", i))
		}

		slowTick(60)

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, tm.Add(ten_seconds))
		if len(req1.items) != 1 || !strings.Contains(req1.payload, "~throttled~") || strings.Contains(req1.payload, "~msg-") {
			t.Error("Unexpected payload")
		}

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, retryAfter)
		if len(req2.items) != 4 || strings.Contains(req2.payload, "~throttled-") || !strings.Contains(req2.payload, "~msg-") {
			t.Error("Unexpected payload")
		}

		transmitter.assertNoRequest(t)
	})
}

func TestThrottleCannotFlush(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(&Client{MaxBatchSize: 4})
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		tm := time.Now()
		retryAfter := transmitter.prepThrottle(time.Minute)

		transmitter.prepResponse(200, 200)

		client.TrackEvent("~throttled~")
		slowTick(10)

		client.TrackEvent("~msg~")
		client.Flush()

		slowTick(60)

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, tm.Add(ten_seconds))

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, retryAfter)

		transmitter.assertNoRequest(t)
	})
}

func TestThrottleFlushesOnClose(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(&Client{MaxBatchSize: 4})
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		tm := time.Now()
		transmitter.prepThrottle(time.Minute)

		transmitter.prepResponse(200, 200)

		client.TrackEvent("~throttled~")
		slowTick(10)

		client.TrackEvent("~msg~")

		client.Close(t.Context())

		req1 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req1.timestamp, tm.Add(ten_seconds))
		if !strings.Contains(req1.payload, "~throttled~") || len(req1.items) != 1 {
			t.Error("Unexpected payload")
		}

		req2 := transmitter.waitForRequest(t)
		assertTimeApprox(t, req2.timestamp, tm.Add(ten_seconds))
		if !strings.Contains(req2.payload, "~msg~") || !strings.Contains(req2.payload, "~throttled~") || len(req2.items) != 2 {
			t.Error("Unexpected payload")
		}

		transmitter.assertNoRequest(t)
	})
}

func TestThrottleAbandonsMessageOnStop(t *testing.T) {
	synctest.Run(func() {
		client, transmitter := newTestChannelServer(&Client{MaxBatchSize: 4})
		defer func() {
			client.Close(t.Context())
			transmitter.Close()
		}()

		transmitter.prepThrottle(time.Minute)

		client.TrackEvent("~throttled~")
		slowTick(10)
		client.TrackEvent("~dropped~")
		client.Stop()

		req := transmitter.waitForRequest(t)
		if strings.Contains(req.payload, "~dropped~") || len(req.items) != 1 {
			t.Fatal("Dropped should have never been sent")
		}

		transmitter.assertNoRequest(t)
	})
}
