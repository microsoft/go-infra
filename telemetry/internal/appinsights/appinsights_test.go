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

func telemetryBuffer(items ...contracts.EventData) telemetryBufferItems {
	ctx := newTelemetryContext(test_ikey)
	ctx.iKey = test_ikey

	var result telemetryBufferItems
	for _, item := range items {
		result = append(result, ctx.envelop(item))
	}

	return result
}

func (buffer *telemetryBufferItems) add(items ...contracts.EventData) {
	*buffer = append(*buffer, telemetryBuffer(items...)...)
}

type nullTransmitter struct{}

func (transmitter *nullTransmitter) Transmit(payload []byte, items telemetryBufferItems) (*transmissionResult, error) {
	return &transmissionResult{statusCode: successResponse}, nil
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

func newTestClientServer(t *testing.T) (transmitter, *testServer) {
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
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return cliConn, nil
		},
	}

	client := newTransmitter(fmt.Sprintf("%s/v2/track", server.server.URL), &http.Client{
		Transport: tr,
	})

	return client, server
}

func newTestTlsClientServer(t *testing.T) (transmitter, *testServer) {
	server := &testServer{}
	server.server = httptest.NewTLSServer(server)
	server.notify = make(chan *testRequest, 1)
	server.responseCode = 200
	server.responseData = make([]byte, 0)
	server.responseHeaders = make(map[string]string)

	client := newTransmitter(fmt.Sprintf("https://%s/v2/track", server.server.Listener.Addr().String()), server.server.Client())
	t.Cleanup(func() {
		server.server.Close()
	})
	return client, server
}
