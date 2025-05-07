//go:build goexperiment.synctest

package appinsights

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"testing"
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
