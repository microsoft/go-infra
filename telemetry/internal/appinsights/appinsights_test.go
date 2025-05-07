package appinsights

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

func telemetryBuffer(items ...contracts.EventData) []*contracts.Envelope {
	ctx := newTelemetryContext(test_ikey)
	ctx.iKey = test_ikey

	var result []*contracts.Envelope
	for _, item := range items {
		result = append(result, ctx.envelop(item))
	}

	return result
}

func addEventData(buffer *[]*contracts.Envelope, items ...contracts.EventData) {
	*buffer = append(*buffer, telemetryBuffer(items...)...)
}

type testServer struct {
	server *httptest.Server
	notify chan *testRequest

	responseData    []byte
	responseCode    int
	responseHeaders map[string]string
}

type testRequest struct {
	request *http.Request
	body    []byte
}

func (server *testServer) Close() {
	server.server.Close()
	close(server.notify)
}

func (server *testServer) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	hdr := writer.Header()
	for k, v := range server.responseHeaders {
		hdr[k] = []string{v}
	}

	writer.WriteHeader(server.responseCode)
	writer.Write(server.responseData)

	server.notify <- &testRequest{
		request: req,
		body:    body,
	}
}

func (server *testServer) waitForRequest(t *testing.T) *testRequest {
	t.Helper()
	select {
	case req := <-server.notify:
		return req
	case <-time.After(time.Second):
		t.Fatal("Server did not receive request within a second")
		return nil /* not reached */
	}
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

func newTestClientServer(t *testing.T) (*Client, *testServer) {
	server := &testServer{}
	server.notify = make(chan *testRequest, 1)
	server.responseCode = 200
	server.responseData = make([]byte, 0)
	server.responseHeaders = make(map[string]string)

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
