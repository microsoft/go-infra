// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/go-infra/subcmd"
)

const sdkTestPAT = "test-only-azure-pat"

// Exercise generated clients, including authenticated API discovery, instead
// of mocking SDK interfaces. No request leaves this local server.
func newAzureSDKServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	locations, err := os.ReadFile("testdata/azure-sdk-locations.json")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || password != sdkTestPAT {
			t.Error("missing or incorrect SDK authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-TFS-FedAuthRedirect") != "Suppress" {
			t.Error("SDK did not suppress federated sign-in redirects")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodOptions && r.URL.Path == "/collection/_apis":
			w.Write(locations)
		case r.Method == http.MethodGet && r.URL.Path == "/collection/_apis/resourceAreas":
			// An empty area list uses the configured collection URL.
			io.WriteString(w, `{"count":0,"value":[]}`)
		default:
			if !strings.Contains(r.Header.Get("Accept"), "api-version=7.1") {
				t.Errorf("request did not use the v7.1 API: %s", r.Header.Get("Accept"))
			}
			handler(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func runAzureCommand(t *testing.T, handler func(subcmd.ParseFunc) error, args ...string) error {
	t.Helper()
	oldFlags, oldUsage := flag.CommandLine, flag.Usage
	defer func() { flag.CommandLine, flag.Usage = oldFlags, oldUsage }()
	flag.CommandLine = flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	flag.Usage = func() {}
	return handler(func() error { return flag.CommandLine.Parse(args) })
}

func azureCommandArgs(s *httptest.Server, args ...string) []string {
	// No trailing organization slash, and a project with a space: route
	// construction and escaping should be left to the SDK.
	return append([]string{"-org", s.URL + "/collection", "-proj", "My Project", "-azdopat", sdkTestPAT}, args...)
}

func captureCommandOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(&output, r)
		done <- err
	}()
	runErr := run()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return output.String(), runErr
}

func TestAzureSDKGetBuildInfo(t *testing.T) {
	s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/collection/My Project/_apis/build/builds/123" {
			t.Errorf("unexpected build request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"id":123,"buildNumber":"20260922.1","sourceBranch":"refs/heads/main","sourceVersion":"abc123"}`)
	})
	out, err := captureCommandOutput(t, func() error {
		return runAzureCommand(t, handleGetBuildInfo, azureCommandArgs(s, "-id=123", "-prefix=Next")...)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"##vso[task.setvariable variable=NextBuildNumber]20260922.1",
		"##vso[task.setvariable variable=NextSourceVersion]abc123",
		"##vso[task.setvariable variable=NextSourceBranch]refs/heads/main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in command output %q", want, out)
		}
	}
}

func TestAzureSDKRetainBuild(t *testing.T) {
	var retained atomic.Bool
	s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/collection/My Project/_apis/build/builds/123" {
			t.Errorf("unexpected retention request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var update struct{ KeepForever *bool }
		if json.NewDecoder(r.Body).Decode(&update) != nil || update.KeepForever == nil || !*update.KeepForever {
			t.Error("request did not enable permanent retention")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		retained.Store(true)
		io.WriteString(w, `{"id":123,"keepForever":true}`)
	})
	for range 2 {
		if err := runAzureCommand(t, handleRetainBuild, azureCommandArgs(s, "-id=123")...); err != nil {
			t.Fatal(err)
		}
		if !retained.Load() {
			t.Fatal("build was not retained")
		}
	}
}

func TestAzureSDKWaitBuild(t *testing.T) {
	for _, result := range []string{"succeeded", "partiallySucceeded", "failed"} {
		t.Run(result, func(t *testing.T) {
			s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/collection/My Project/_apis/build/builds/123" {
					t.Errorf("unexpected build request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				fmt.Fprintf(w, `{"id":123,"status":"completed","result":%q}`, result)
			})
			err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(s, "-id=123")...)
			if result == "failed" {
				if err == nil || !strings.Contains(err.Error(), "not successful") {
					t.Fatalf("expected failed build error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAzureSDKWaitCommit(t *testing.T) {
	s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/collection/My Project/_apis/git/repositories/go/commits/abc123" {
			t.Errorf("unexpected commit request: %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"commitId":"abc123"}`)
	})
	if err := runAzureCommand(t, handleWaitAzDOCommit, azureCommandArgs(s, "-name=go", "-commit=abc123")...); err != nil {
		t.Fatal(err)
	}
}

func TestAzureSDKWaitCITrigger(t *testing.T) {
	s := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected request method: %s", r.Method)
		}
		switch r.URL.Path {
		case "/collection/My Project/_apis/pipelines/191":
			io.WriteString(w, `{"id":191,"name":"Go CI"}`)
		case "/collection/My Project/_apis/git/repositories/go/commits/abc123/statuses":
			if r.URL.Query().Get("latestOnly") != "true" {
				t.Error("expected latest commit statuses")
			}
			io.WriteString(w, `{"count":1,"value":[{"context":{"name":"build/Go CI"},"targetUrl":"vstfs:///Build/Build/123"}]}`)
		default:
			t.Errorf("unexpected trigger request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	out, err := captureCommandOutput(t, func() error {
		return runAzureCommand(t, handleWaitCITrigger, azureCommandArgs(s, "-pipeline-id=191", "-repository-id=go", "-commit=abc123", "-set-azdo-variable=NextBuild")...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "##vso[task.setvariable variable=NextBuild]123") {
		t.Errorf("missing discovered build ID: %q", out)
	}
}
