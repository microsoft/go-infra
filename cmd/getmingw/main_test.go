// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGetWithRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var delays []time.Duration
	resp, err := getWithRetry(server.Client(), server.URL, func(delay time.Duration) {
		delays = append(delays, delay)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %v, want %v", resp.StatusCode, http.StatusOK)
	}
	if attempts != 3 {
		t.Errorf("got %d attempts, want 3", attempts)
	}
	wantDelays := []time.Duration{time.Second, 2 * time.Second}
	if !reflect.DeepEqual(delays, wantDelays) {
		t.Errorf("got delays %v, want %v", delays, wantDelays)
	}
}

func TestGetWithRetryDoesNotRetryClientError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	resp, err := getWithRetry(server.Client(), server.URL, func(time.Duration) {
		t.Fatal("unexpected retry")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got status %v, want %v", resp.StatusCode, http.StatusNotFound)
	}
	if attempts != 1 {
		t.Errorf("got %d attempts, want 1", attempts)
	}
}

func TestGetWithRetryDrainsRetryResponse(t *testing.T) {
	drained := false
	closed := false
	requests := 0
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 1 {
				return &http.Response{
					Status:     "503 Service Unavailable",
					StatusCode: http.StatusServiceUnavailable,
					Body: &trackingBody{
						reader:  strings.NewReader("try again"),
						drained: &drained,
						closed:  &closed,
					},
				}, nil
			}
			return &http.Response{
				Status:     "200 OK",
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
			}, nil
		}),
	}

	resp, err := getWithRetry(client, "https://example.test/download", func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if !drained {
		t.Error("retry response body was not drained")
	}
	if !closed {
		t.Error("retry response body was not closed")
	}
	if requests != 2 {
		t.Errorf("got %d requests, want 2", requests)
	}
}

func TestCreateFreshChecksumRejectsNonOKResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	b := build{URL: server.URL}
	err := b.CreateFreshChecksum()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errBuildNotFound) {
		t.Fatalf("got %v, want non-not-found error", err)
	}
	if b.SHA512 != "" {
		t.Errorf("got SHA512 %q, want empty", b.SHA512)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingBody struct {
	reader  *strings.Reader
	drained *bool
	closed  *bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		*b.drained = true
	}
	return n, err
}

func (b *trackingBody) Close() error {
	*b.closed = true
	return nil
}
