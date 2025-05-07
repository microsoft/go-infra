//go:build goexperiment.synctest

package appinsights

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"testing"
	"testing/synctest"
)

type fakeTransport struct {
	code int
	body []byte
	err  error
}

func (t fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.code,
		Body:       io.NopCloser(bytes.NewBuffer(t.body)),
	}, t.err
}

func BenchmarkClientBurstPerformance(b *testing.B) {
	client := &Client{
		InstrumentationKey: test_ikey,
		ErrorLog:           log.New(io.Discard, "", 0),
		HTTPClient: &http.Client{
			Transport: fakeTransport{
				code: http.StatusOK,
				body: []byte(`{"itemsReceived":4, "itemsAccepted":4, "errors":[]}`),
			},
		},
	}

	for b.Loop() {
		client.TrackEvent("A message")
	}

	client.Close(b.Context())
}

func TestEndToEnd(t *testing.T) {
	synctest.Run(func() {
		client, server := newTestClientServer(t)
		defer server.Close()

		// Set up server response
		server.responseData = []byte(`{"itemsReceived":4, "itemsAccepted":4, "errors":[]}`)
		server.responseHeaders["Content-type"] = "application/json"

		defer client.Close(t.Context())

		// Track directly off the client
		client.TrackEvent("client-event")

		// NOTE: A lot of this is covered elsewhere, so we won't duplicate
		// *too* much.

		// Wait for automatic transmit -- get the request
		slowTick(11)
		req := server.waitForRequest(t)

		// GZIP magic number
		if len(req.body) < 2 || req.body[0] != 0x1f || req.body[1] != 0x8b {
			t.Fatal("missing gzip magic number")
		}

		// Decompress
		reader, err := gzip.NewReader(bytes.NewReader(req.body))
		if err != nil {
			t.Fatalf("couldn't't create gzip reader: %s", err.Error())
		}

		// Read payload
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatalf("couldn't read compressed data: %s", err.Error())
		}

		// Check out payload
		j, err := parsePayload(body)
		if err != nil {
			t.Errorf("error parsing payload: %s", err.Error())
		}

		if len(j) != 1 {
			t.Fatal("unexpected event count, expected 1, got", len(j))
		}

		j[0].assertPath(t, "iKey", test_ikey)
		j[0].assertPath(t, "name", "Microsoft.ApplicationInsights.01234567000089abcdef000000000000.Event")
		j[0].assertPath(t, "time", "2000-01-01T00:00:00Z")
	})
}
