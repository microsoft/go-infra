// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestListenAndServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		result <- ListenAndServe(ctx, "127.0.0.1:0", func(launchURL string) {
			ready <- launchURL
		}, WithProcesses(&fakeProcess{definition: exampleProcessDefinition()}))
	}()

	var launchURL string
	select {
	case launchURL = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for release UI listener")
	}
	parsed, err := url.Parse(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "http" || parsed.Query().Get("token") == "" {
		t.Fatalf("launch URL = %q", launchURL)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("launch status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for release UI shutdown")
	}
}

func TestListenAndServeRejectsNonLoopbackAddress(t *testing.T) {
	err := ListenAndServe(
		context.Background(), "0.0.0.0:0", nil,
		WithProcesses(&fakeProcess{definition: exampleProcessDefinition()}),
	)
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("error = %v, want non-loopback rejection", err)
	}
}
