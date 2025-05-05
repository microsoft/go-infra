//go:build goexperiment.synctest

package appinsights

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

func BenchmarkClientBurstPerformance(b *testing.B) {
	client := &Client{InstrumentationKey: test_ikey}
	client.channel.transmitter = &nullTransmitter{}

	for i := 0; i < b.N; i++ {
		client.TrackNewEvent("A message")
	}

	<-client.channel.Close(time.Minute)
}

func TestEndToEnd(t *testing.T) {
	synctest.Run(func() {
		xmit, server := newTestClientServer(t)
		defer server.Close()

		// Set up server response
		server.responseData = []byte(`{"itemsReceived":4, "itemsAccepted":4, "errors":[]}`)
		server.responseHeaders["Content-type"] = "application/json"

		client := &Client{InstrumentationKey: test_ikey, Endpoint: xmit.(*httpTransmitter).endpoint, HttpClient: xmit.(*httpTransmitter).client}
		defer client.Close(context.Background())

		// Track directly off the client
		client.TrackNewEvent("client-event")

		// NOTE: A lot of this is covered elsewhere, so we won't duplicate
		// *too* much.

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
		j[0].assertPath(t, "time", "2000-01-01T00:00:00Z")
	})
}
