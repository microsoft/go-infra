package appinsights

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

func envelopes(names ...string) []*contracts.Envelope {
	ctx := setupContext(test_ikey, nil)
	ctx.iKey = test_ikey

	var result []*contracts.Envelope
	for _, name := range names {
		result = append(result, ctx.envelop(
			contracts.EventData{
				Name: name,
				Ver:  2,
			},
		))
	}

	return result
}

func batchItems(names ...string) []batchItem {
	events := envelopes(names...)
	items := make([]batchItem, len(events))
	for i, event := range events {
		items[i] = batchItem{item: event}
	}
	return items
}

type serverResponse struct {
	statusCode int
	retryAfter time.Time
	body       backendResponse
	want       []*contracts.Envelope
}

type testServer struct {
	t      *testing.T
	server *httptest.Server

	responses []serverResponse
}

func newTestServer(t *testing.T, responses ...serverResponse) *testServer {
	t.Helper()
	server := &testServer{
		t:         t,
		responses: responses,
	}
	return server
}

func (server *testServer) Close() {
	if len(server.responses) > 0 {
		server.t.Errorf("not all responses were consumed, remaining: %d", len(server.responses))
	}
	// Close the server to prevent leaks
	server.server.Close()
}

func (srv *testServer) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	var resp serverResponse
	resp, srv.responses = srv.responses[0], srv.responses[1:]

	if req.Method != http.MethodPost {
		srv.t.Errorf("request.Method want POST, got %s", req.Method)
	}

	if encoding := req.Header.Get("Content-Encoding"); encoding != "gzip" {
		srv.t.Errorf("request.Content-Encoding want gzip, got %s", encoding)
	}

	// Decompress payload
	reader, err := gzip.NewReader(req.Body)
	if err != nil {
		srv.t.Errorf("couldn't create gzip reader: %v", err)
	}
	defer reader.Close()
	dec := json.NewDecoder(reader)
	var items []*contracts.Envelope
	for {
		var item contracts.Envelope
		if err := dec.Decode(&item); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			srv.t.Errorf("couldn't decode payload: %v", err)
			break
		}
		items = append(items, &item)
	}

	if len(items) != len(resp.want) {
		srv.t.Errorf("request body length mismatch, want %d, got %d", len(resp.want), len(items))
	} else {
		for i, item := range items {
			compareEnvelopes(srv.t, item, resp.want[i])
		}
	}

	body, err := json.Marshal(resp.body)
	if err != nil {
		srv.t.Errorf("couldn't encode response: %v", err)
	}

	writer.Header().Set("Content-Type", "application/json")
	if !resp.retryAfter.IsZero() {
		writer.Header().Set("Retry-After", resp.retryAfter.Format(http.TimeFormat))
	}

	writer.WriteHeader(resp.statusCode)
	writer.Write(body)
}

type fakeListener struct {
	ch   chan net.Conn
	addr net.Addr
}

func (li *fakeListener) Accept() (net.Conn, error) {
	conn := <-li.ch
	if conn == nil {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (li *fakeListener) Close() error   { return nil }
func (li *fakeListener) Addr() net.Addr { return li.addr }

func newTestClientServer(t *testing.T, responses ...serverResponse) (*Client, *testServer) {
	server := newTestServer(t, responses...)

	srvConn, cliConn := net.Pipe()
	li := &fakeListener{
		ch:   make(chan net.Conn, 1),
		addr: srvConn.LocalAddr(),
	}
	li.ch <- srvConn
	close(li.ch)
	server.server = &httptest.Server{
		Config: &http.Server{
			Handler: server,
		},
		Listener: li,
	}
	server.server.Start()
	t.Cleanup(func() {
		srvConn.Close()
		cliConn.Close()
		server.server.Close()
	})
	client := &Client{
		InstrumentationKey: test_ikey,
		Endpoint:           fmt.Sprintf("%s/v2/track", server.server.URL),
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					return cliConn, nil
				},
			},
		},
	}
	return client, server
}

func check(t *testing.T, name string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s mismatch, want %v, got %v", name, want, got)
	}
}

func compareEnvelopes(t *testing.T, got, want *contracts.Envelope) {
	t.Helper()
	check(t, "iKey", got.IKey, want.IKey)
	check(t, "name", got.Name, want.Name)
	check(t, "time", got.Time, want.Time)
	check(t, "sampleRate", got.SampleRate, want.SampleRate)
	check(t, "ver", got.Ver, want.Ver)
	check(t, "seq", got.Seq, want.Seq)
	check(t, "tags", got.Tags, want.Tags)
	check(t, "data.baseType", got.Data.BaseType, want.Data.BaseType)
	check(t, "data.baseData", got.Data.BaseData, want.Data.BaseData)
}
