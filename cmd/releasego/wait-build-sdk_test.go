// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/go-infra/azdo"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	return &logs
}

func TestWaitBuildAPIResponses(t *testing.T) {
	html := "<html>\n" + strings.Repeat("outage ", 10000) + "\nreference: end</html>"
	for _, tt := range []struct {
		name, contentType, body string
		status, wantCalls       int
		wantError, nonJSON      bool
	}{
		{"HTML success", "text/html; charset=utf-8", html, 200, 2, false, true},
		{"HTML outage", "text/html", html, 503, 2, false, true},
		{"plain text outage", "text/plain", "service unavailable", 502, 2, false, true},
		{"rate limit", "application/json", `{"message":"too many requests"}`, 429, 2, false, false},
		{"JSON server error", "application/json", `{"message":"service unavailable"}`, 500, 2, false, false},
		{"HTML unauthorized", "text/html", html, 401, 1, true, true},
		{"JSON forbidden", "application/json", `{"message":"forbidden"}`, 403, 1, true, false},
		{"HTML bad request", "text/html", html, 400, 1, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			var calls atomic.Int32
			server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/collection/My Project/_apis/build/builds/123" {
					t.Errorf("unexpected build request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Type", tt.contentType)
					w.WriteHeader(tt.status)
					writeAzureSDKResponse(t, w, tt.body)
					return
				}
				writeAzureSDKResponse(t, w, `{"id":123,"status":"completed","result":"succeeded"}`)
			})
			err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(server, "-id=123", "-poll-delay=0")...)
			if (err != nil) != tt.wantError || int(calls.Load()) != tt.wantCalls {
				t.Fatalf("error = %v, calls = %d; want error = %v, calls = %d", err, calls.Load(), tt.wantError, tt.wantCalls)
			}
			output := logs.String()
			if err != nil {
				output += err.Error()
			}
			if tt.nonJSON {
				if !strings.Contains(output, strconv.Quote(tt.body)) ||
					!strings.Contains(output, fmt.Sprintf("HTTP %d", tt.status)) ||
					!strings.Contains(output, tt.contentType) {
					t.Fatal("diagnostics omitted the status, content type, or complete body")
				}
				if err != nil {
					responseErr, ok := errors.AsType[*azdo.NonJSONResponseError](err)
					if !ok || responseErr.StatusCode != tt.status || responseErr.Body != tt.body {
						t.Fatalf("error did not preserve response: %v", err)
					}
				}
			}
			if strings.Contains(output, sdkTestPAT) {
				t.Fatal("diagnostics exposed the request PAT")
			}
		})
	}
}

func TestWaitBuildRetryLimit(t *testing.T) {
	logs := captureLogs(t)
	const body = "<html>persistent outage</html>"
	var calls atomic.Int32
	server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		writeAzureSDKResponse(t, w, body)
	})
	err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(server, "-id=123", "-poll-delay=0")...)
	if err == nil || !strings.Contains(err.Error(), "too many sequential flaky errors") ||
		!strings.Contains(err.Error(), strconv.Quote(body)) || calls.Load() != 11 {
		t.Fatalf("error = %v, calls = %d; want failure after 11 calls with the response", err, calls.Load())
	}
	if got := strings.Count(logs.String(), strconv.Quote(body)); got != 10 {
		t.Fatalf("logged %d retry responses, want 10", got)
	}
}

func TestWaitBuildRetryCounterResets(t *testing.T) {
	captureLogs(t)
	var calls atomic.Int32
	server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch call := calls.Add(1); {
		case call <= 10 || call >= 12 && call <= 21:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			writeAzureSDKResponse(t, w, "<html>outage</html>")
		case call == 11:
			writeAzureSDKResponse(t, w, `{"id":123,"status":"inProgress"}`)
		default:
			writeAzureSDKResponse(t, w, `{"id":123,"status":"completed","result":"succeeded"}`)
		}
	})
	err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(server, "-id=123", "-poll-delay=0")...)
	if err != nil || calls.Load() != 22 {
		t.Fatalf("error = %v, calls = %d; want success after 22 calls", err, calls.Load())
	}
}

func TestWaitBuildDiscoveryFailures(t *testing.T) {
	locations, err := os.ReadFile("testdata/azure-sdk-locations.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, method, path string
		status, wantCalls  int
		wantError          bool
	}{
		{"OPTIONS outage", http.MethodOptions, "/collection/_apis", 503, 2, false},
		{"resource area HTML success", http.MethodGet, "/collection/_apis/resourceAreas", 200, 2, false},
		{"resource area unauthorized", http.MethodGet, "/collection/_apis/resourceAreas", 401, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			const body = "<html>discovery failed\nreference: 123</html>"
			var failedCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, password, ok := r.BasicAuth(); !ok || password != sdkTestPAT {
					t.Error("missing SDK authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == tt.method && r.URL.Path == tt.path && failedCalls.Add(1) == 1 {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(tt.status)
					writeAzureSDKResponse(t, w, body)
					return
				}
				switch {
				case r.Method == http.MethodOptions && r.URL.Path == "/collection/_apis":
					writeAzureSDKResponse(t, w, string(locations))
				case r.Method == http.MethodGet && r.URL.Path == "/collection/_apis/resourceAreas":
					writeAzureSDKResponse(t, w, `{"count":0,"value":[]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/collection/My Project/_apis/build/builds/123":
					writeAzureSDKResponse(t, w, `{"id":123,"status":"completed","result":"succeeded"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(server, "-id=123", "-poll-delay=0")...)
			if (err != nil) != tt.wantError || int(failedCalls.Load()) != tt.wantCalls {
				t.Fatalf("error = %v, calls = %d; want error = %v, calls = %d", err, failedCalls.Load(), tt.wantError, tt.wantCalls)
			}
			output := logs.String()
			if err != nil {
				output += err.Error()
			}
			if !strings.Contains(output, strconv.Quote(body)) || !strings.Contains(output, fmt.Sprintf("HTTP %d", tt.status)) {
				t.Fatal("discovery error omitted the response body or status")
			}
		})
	}
}

func TestWaitBuildMissingStatusOrResult(t *testing.T) {
	for _, tt := range []struct {
		body, want string
	}{
		{`{"id":123}`, "did not contain a status"},
		{`{"id":123,"status":"completed"}`, "did not contain a result"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeAzureSDKResponse(t, w, tt.body)
			})
			err := runAzureCommand(t, handleWaitBuild, azureCommandArgs(server, "-id=123", "-poll-delay=0")...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRetainBuildReportsNonJSONResponse(t *testing.T) {
	for _, phase := range []string{"get build", "add lease"} {
		t.Run(phase, func(t *testing.T) {
			const body = "<html>retention service unavailable</html>"
			var failures atomic.Int32
			server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
				if phase == "add lease" && r.Method == http.MethodGet {
					writeAzureSDKResponse(t, w, `{"id":123,"definition":{"id":456}}`)
					return
				}
				failures.Add(1)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusServiceUnavailable)
				writeAzureSDKResponse(t, w, body)
			})
			err := runAzureCommand(t, handleRetainBuild, azureCommandArgs(server, "-id=123")...)
			if err == nil || !strings.Contains(err.Error(), strconv.Quote(body)) || failures.Load() != 1 {
				t.Fatalf("error = %v, failed requests = %d; want one failure with response body", err, failures.Load())
			}
		})
	}
}

func TestWaitCommitReportsNonJSONResponse(t *testing.T) {
	logs := captureLogs(t)
	const body = "<html>git service unavailable</html>"
	var calls atomic.Int32
	server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			writeAzureSDKResponse(t, w, body)
			return
		}
		writeAzureSDKResponse(t, w, `{"commitId":"abc123"}`)
	})
	err := runAzureCommand(t, handleWaitAzDOCommit, azureCommandArgs(server, "-name=go", "-commit=abc123", "-poll-delay=0")...)
	if err != nil || calls.Load() != 2 || !strings.Contains(logs.String(), strconv.Quote(body)) {
		t.Fatalf("error = %v, calls = %d; want retry with response body in log", err, calls.Load())
	}
}

func TestWaitCITriggerReportsNonJSONResponse(t *testing.T) {
	const body = "<html>access denied</html>"
	server := newAzureSDKServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		writeAzureSDKResponse(t, w, body)
	})
	err := runAzureCommand(t, handleWaitCITrigger, azureCommandArgs(server, "-pipeline-id=191", "-repository-id=go", "-commit=abc123")...)
	if err == nil || !strings.Contains(err.Error(), strconv.Quote(body)) || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error = %v; want complete forbidden response", err)
	}
}
