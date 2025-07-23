// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build goexperiment.synctest || go1.25

// This package contains test utilities for the Application Insights SDK.
package appinsightstest

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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

const test_ikey = "01234567-0000-89ab-cdef-000000000000"

var envelopeName = "Microsoft.ApplicationInsights.Event"

// ActionType represents the type of action to be performed by the client.
type ActionType int

const (
	TrackAction ActionType = iota
	FlushAction
	StopAction
	CloseAction
	SleepAction
)

// Action represents an action to be performed by the client.
type Action struct {
	Type    ActionType
	Delay   time.Duration
	Context context.Context
	Error   string // Expected error to be returned by the action, if any.
}

func (a Action) do(c *Client) {
	if a.Delay != 0 {
		time.Sleep(a.Delay)
		synctest.Wait()
	}

	c.n++
	var err error
	switch a.Type {
	case TrackAction:
		name := "msg_" + strconv.Itoa(c.n)
		c.server.mu.Lock()
		c.server.events = append(c.server.events, newEvent(name))
		c.server.mu.Unlock()
		c.client.TrackEvent(name, nil)
	case FlushAction:
		c.client.Flush()
	case StopAction:
		err = c.client.Stop()
	case CloseAction:
		if a.Context == nil {
			a.Context = context.Background()
		}
		err = c.client.Close(a.Context)
	case SleepAction:
		// Slept above.
	default:
		panic(fmt.Sprintf("unknown action type: %d", a.Type))
	}
	var errStr string
	if err != nil {
		errStr = err.Error()
	}
	if errStr != a.Error {
		c.t.Errorf("action %d: expected error %v, got %v", c.n, a.Error, err)
	}
}

func newEvent(name string) *contracts.Envelope {
	return &contracts.Envelope{
		IKey: test_ikey,
		Name: envelopeName,
		Time: time.Now().UTC(),
		Tags: map[string]string{
			"ai.internal.sdkVersion": "go-infra/telemetry:" + appinsights.Version,
		},
		Ver:        1,
		SampleRate: 100.0,
		Data: contracts.Data{
			BaseType: "EventData",
			BaseData: contracts.EventData{
				Name: name,
				Ver:  2,
			},
		},
	}
}

// Client is a test client for the Application Insights SDK.
type Client struct {
	t       *testing.T
	client  *appinsights.Client
	server  *testServer
	actions []Action

	n      int
	closed bool
}

// New creates a new test client with the specified actions and responses.
// The client will be configured with a test server that will respond to the
// actions with the specified responses. The client will also be configured
// with a buffer to capture any errors that occur during the test.
// The client max batch size and max batch interval are disabled by default
// to avoid unintended uploads during the test. Use [Client.SetMaxBatchSize]
// and [Client.SetMaxBatchInterval] to set them to a specific value.
func New(t *testing.T, actions []Action, responses ...ServerResponse) *Client {
	server := newTestServer(t, responses...)
	client := &appinsights.Client{
		InstrumentationKey: test_ikey,
		Endpoint:           fmt.Sprintf("%s/v2/track", server.srv.URL),
		HTTPClient:         server.srv.Client(),
		MaxBatchSize:       1024,
		MaxBatchInterval:   100 * time.Hour,
	}
	return &Client{
		t:       t,
		client:  client,
		server:  server,
		actions: actions,
	}
}

// SetMaxBatchSize sets the maximum number of telemetry items that can be
// submitted in each request.
func (c *Client) SetMaxBatchSize(size int) {
	if size > 0 {
		c.client.MaxBatchSize = size
	}
}

// SetMaxBatchInterval sets the maximum time to wait before sending a batch
// of telemetry items.
func (c *Client) SetMaxBatchInterval(interval time.Duration) {
	if interval > 0 {
		c.client.MaxBatchInterval = interval
	}
}

// Act executes all the client actions.
func (c *Client) Act() {
	for _, action := range c.actions {
		action.do(c)
	}
}

// Close closes the client and waits for all actions to be executed.
func (c *Client) Close(ctx context.Context) error {
	if c.closed {
		return nil
	}
	rem := len(c.actions) - c.n
	if rem > 0 {
		c.t.Errorf("not all actions were executed, remaining: %d, total: %d", rem, len(c.actions))
	}
	// Close the server before the client to ensure that we detect outstanding requests as errors.
	c.server.close()
	err := c.client.Close(ctx)
	c.closed = true
	return err
}

// ServerResponse represents a response from the test server.
type ServerResponse struct {
	StatusCode int
	RetryDelay time.Duration
	Errors     []contracts.BackendResponseError

	// indices of the testServer.eventIndices that should be sent by the client.
	EventIndices []int
}

// testServer is a test server that simulates the Application Insights
// backend. It can be used to test the client by sending requests and
// verifying the responses.
type testServer struct {
	t   *testing.T
	srv *httptest.Server

	done      chan struct{}
	events    []*contracts.Envelope
	responses []ServerResponse
	n         int
	closed    atomic.Bool
	mu        sync.Mutex // protects server properties
}

func newTestServer(t *testing.T, responses ...ServerResponse) *testServer {
	server := &testServer{
		t:         t,
		responses: responses,
		done:      make(chan struct{}),
	}
	if len(responses) == 0 {
		// No responses, close the done channel immediately.
		close(server.done)
	}

	srvConn, cliConn := net.Pipe()
	li := &fakeListener{
		ch:   make(chan net.Conn, 1),
		addr: srvConn.LocalAddr(),
	}
	li.ch <- srvConn
	close(li.ch)
	server.srv = &httptest.Server{
		Config: &http.Server{
			Handler: server,
		},
		Listener: li,
	}
	// Start the server
	server.srv.Start()
	server.srv.Client().Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return cliConn, nil
	}
	t.Cleanup(func() {
		srvConn.Close()
		cliConn.Close()
		server.srv.Close()
	})
	return server
}

func (srv *testServer) close() {
	select {
	case <-srv.done:
	case <-time.After(1 * time.Second):
		srv.mu.Lock()
		rem := len(srv.responses) - srv.n
		if rem > 0 {
			srv.t.Errorf("not all responses were consumed, remaining: %d, total: %d", rem, len(srv.responses))
		}
		srv.mu.Unlock()
	}
	srv.srv.Close()
	srv.closed.Store(true)
}

func (srv *testServer) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	if srv.closed.Load() {
		// Can happen if the client is closed with an outstanding request.
		srv.t.Log("server closed, ignoring request")
		return
	}
	defer func() {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		if srv.n >= len(srv.responses) {
			srv.done <- struct{}{}
		} else {
			srv.n++
		}
	}()
	if srv.n >= len(srv.responses) {
		srv.t.Errorf("unexpected request, no more responses available")
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Check if the request is a POST
	resp := srv.responses[srv.n]

	if req.Method != http.MethodPost {
		srv.t.Errorf("[%d] request.Method want POST, got %s", srv.n, req.Method)
	}

	if encoding := req.Header.Get("Content-Encoding"); encoding != "gzip" {
		srv.t.Errorf("[%d] request.Content-Encoding want gzip, got %s", srv.n, encoding)
	}

	// Decompress payload
	reader, err := gzip.NewReader(req.Body)
	if err != nil {
		srv.t.Errorf("[%d] couldn't create gzip reader: %v", srv.n, err)
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
			srv.t.Errorf("[%d] couldn't decode payload: %v", srv.n, err)
			break
		}
		items = append(items, &item)
	}

	if len(items) != len(resp.EventIndices) {
		srv.t.Errorf("[%d] request body length mismatch, want %d, got %d", srv.n, len(resp.EventIndices), len(items))
	} else {
		srv.mu.Lock()
		for i, item := range items {
			if i >= len(resp.EventIndices) {
				srv.t.Errorf("[%d] item %d index out of range, want %d", srv.n, i, len(resp.EventIndices))
				continue
			}
			prefix := fmt.Sprintf("[%d] item %d", srv.n, i)
			compareEnvelopes(srv.t, prefix, item, srv.events[resp.EventIndices[i]])
		}
		srv.mu.Unlock()
	}

	result := contracts.BackendResponse{
		ItemsReceived: len(items),
		ItemsAccepted: len(items) - len(resp.Errors),
		Errors:        resp.Errors,
	}
	body, err := json.Marshal(result)
	if err != nil {
		srv.t.Errorf("[%d] couldn't encode response: %v", srv.n, err)
	}

	writer.Header().Set("Content-Type", "application/json")
	if resp.RetryDelay != 0 {
		writer.Header().Set("Retry-After", time.Now().Add(resp.RetryDelay).Format(http.TimeFormat))
	}

	writer.WriteHeader(resp.StatusCode)
	writer.Write(body)
}

func check(t *testing.T, prefix string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s mismatch, want %v, got %v", prefix, want, got)
	}
}

func compareEnvelopes(t *testing.T, prefix string, got, want *contracts.Envelope) {
	t.Helper()
	check(t, prefix+" iKey", got.IKey, want.IKey)
	check(t, prefix+" name", got.Name, want.Name)
	check(t, prefix+" time", got.Time, want.Time)
	check(t, prefix+" sampleRate", got.SampleRate, want.SampleRate)
	check(t, prefix+" ver", got.Ver, want.Ver)
	check(t, prefix+" seq", got.Seq, want.Seq)
	check(t, prefix+" tags", got.Tags, want.Tags)
	check(t, prefix+" data.baseType", got.Data.BaseType, want.Data.BaseType)
	check(t, prefix+" data.baseData", got.Data.BaseData, want.Data.BaseData)
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
