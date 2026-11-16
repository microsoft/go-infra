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

func TestLatestNixmanBuilds(t *testing.T) {
	for _, tt := range []struct {
		name     string
		versions []string
		source   string
		want     string
		count    int
		wantErr  string
	}{
		{name: "GCC version", versions: []string{"9.5.0-rt_v12-rev2", "15.1.0-rt_v11-rev0", "14.2.0-rt_v13-rev10"}, want: "15.1.0-rt_v11-rev0", count: 1},
		{name: "runtime version", versions: []string{"14.2.0-rt_v9-rev99", "14.2.0-rt_v12-rev2"}, want: "14.2.0-rt_v12-rev2", count: 1},
		{name: "revision", versions: []string{"14.2.0-rt_v12-rev2", "14.2.0-rt_v12-rev10"}, want: "14.2.0-rt_v12-rev10", count: 1},
		{name: "retain matching variants", versions: []string{"14.2.0-rt_v12-rev2", "13.2.0-rt_v11-rev0", "14.2.0-rt_v12-rev2"}, want: "14.2.0-rt_v12-rev2", count: 2},
		{name: "invalid version", versions: []string{"unknown"}, wantErr: "invalid Nixman version"},
		{name: "invalid numeric component", versions: []string{"14.2.0-rt_v12-rev999999999999999999999"}, wantErr: "invalid Nixman version"},
		{name: "unsupported source", versions: []string{"8.1.0"}, source: Sourceforge, wantErr: "-source nixman"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source := tt.source
			if source == "" {
				source = Nixman
			}
			var builds []*build
			for _, version := range tt.versions {
				builds = append(builds, &build{Source: source, Version: version})
			}
			got, err := latestNixmanBuilds(builds)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("latestNixmanBuilds() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.count {
				t.Fatalf("got %d builds, want %d", len(got), tt.count)
			}
			for _, b := range got {
				if b.Version != tt.want {
					t.Errorf("selected version %q, want %q", b.Version, tt.want)
				}
			}
		})
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
