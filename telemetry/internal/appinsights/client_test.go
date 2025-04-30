package appinsights

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"
	"time"
)

func BenchmarkClientBurstPerformance(b *testing.B) {
	client := NewTelemetryClient("fake", "fake", nil)
	client.channel.transmitter = &nullTransmitter{}

	for i := 0; i < b.N; i++ {
		client.TrackNewEvent("A message")
	}

	<-client.channel.Close(time.Minute)
}

func TestClientProperties(t *testing.T) {
	client := NewTelemetryClient(test_ikey, "fake", nil)
	defer client.channel.Close(0)

	if ikey := client.context.instrumentationKey(); ikey != test_ikey {
		t.Error("Client's InstrumentationKey is not expected")
	}

	if ikey := client.context.instrumentationKey(); ikey != test_ikey {
		t.Error("Context's InstrumentationKey is not expected")
	}

	if client.context == nil {
		t.Error("Client.Context == nil")
	}

	if client.Enabled() == false {
		t.Error("Client.IsEnabled == false")
	}

	client.SetEnabled(false)
	if client.Enabled() == true {
		t.Error("Client.SetIsEnabled had no effect")
	}
}

func TestEndToEnd(t *testing.T) {
	mockClockAt(time.Unix(1511001321, 0))
	defer resetClock()
	xmit, server := newTestClientServer()
	defer server.Close()

	client := NewTelemetryClient(test_ikey, xmit.(*httpTransmitter).endpoint, nil)
	defer client.Close(context.Background())

	// Track directly off the client
	client.TrackNewEvent("client-event")

	// NOTE: A lot of this is covered elsewhere, so we won't duplicate
	// *too* much.

	// Set up server response
	server.responseData = []byte(`{"itemsReceived":4, "itemsAccepted":4, "errors":[]}`)
	server.responseHeaders["Content-type"] = "application/json"

	// Wait for automatic transmit -- get the request
	slowTick(11)
	req := server.waitForRequest(t)

	// GZIP magic number
	if len(req.body) < 2 || req.body[0] != 0x1f || req.body[1] != 0x8b {
		t.Fatal("Missing gzip magic number")
	}

	// Decompress
	reader, err := gzip.NewReader(bytes.NewReader(req.body))
	if err != nil {
		t.Fatalf("Coudln't create gzip reader: %s", err.Error())
	}

	// Read payload
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatalf("Couldn't read compressed data: %s", err.Error())
	}

	// Check out payload
	j, err := parsePayload(body)
	if err != nil {
		t.Errorf("Error parsing payload: %s", err.Error())
	}

	if len(j) != 1 {
		t.Fatal("Unexpected event count")
	}

	j[0].assertPath(t, "iKey", test_ikey)
	j[0].assertPath(t, "name", "Microsoft.ApplicationInsights.01234567000089abcdef000000000000.Event")
	j[0].assertPath(t, "time", "2017-11-18T10:35:21Z")
}
