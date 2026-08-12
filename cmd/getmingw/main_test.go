// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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
