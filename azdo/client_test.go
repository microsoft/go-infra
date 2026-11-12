// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdo

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
)

func TestJSONResponseTransport(t *testing.T) {
	longHTML := "<html>\n" + strings.Repeat("x", 70000) + "\nreference: end</html>"
	for _, tt := range []struct {
		name        string
		status      int
		contentType string
		accept      string
		body        string
		wantError   bool
	}{
		{"JSON", 200, "application/json", "application/json", `{"status":"completed"}`, false},
		{"JSON with BOM", 200, "application/json", "application/json", "\xef\xbb\xbf" + `{"status":"completed"}`, false},
		{"HTML success", 200, "text/html; charset=utf-8", "application/json", longHTML, true},
		{"HTML outage", 503, "text/html", "application/json", "<html>service unavailable</html>", true},
		{"malformed JSON", 400, "application/json", "application/json", `{"message":`, true},
		{"empty success", 200, "application/json", "application/json", "", true},
		{"non-JSON request", 200, "text/plain", "text/plain", "not JSON", false},
		{"unfollowed redirect", 302, "text/html", "application/json", "<html>redirect</html>", true},
		{"redirect without location", 302, "text/html", "application/json", "<html>redirect error</html>", true},
		{"multiple choices", 300, "text/html", "application/json", "<html>unexpected response</html>", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				if tt.name == "unfollowed redirect" {
					w.Header().Set("Location", "/next")
				}
				w.WriteHeader(tt.status)
				if _, err := io.WriteString(w, tt.body); err != nil {
					t.Errorf("writing response: %v", err)
				}
			}))
			defer server.Close()

			client := &http.Client{
				Transport: &jsonResponseTransport{base: http.DefaultTransport},
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept", tt.accept)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(resp.Body)
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
			if !tt.wantError {
				if readErr != nil {
					t.Fatalf("reading response: %v", readErr)
				}
				return
			}
			responseErr, ok := errors.AsType[*NonJSONResponseError](readErr)
			if !ok {
				t.Fatalf("read error = %T %v, want NonJSONResponseError", readErr, readErr)
			}
			if responseErr.StatusCode != tt.status || responseErr.ContentType != tt.contentType || responseErr.Body != tt.body {
				t.Fatalf("response diagnostic = %+v", responseErr)
			}
			if _, ok := errors.AsType[*json.SyntaxError](readErr); !ok {
				t.Fatalf("read error does not wrap the JSON syntax error: %v", readErr)
			}
			if !strings.Contains(readErr.Error(), strconv.Quote(tt.body)) {
				t.Fatal("error did not include the complete response body")
			}
		})
	}
}

func TestJSONResponseTransportFollowsRedirect(t *testing.T) {
	const body = "<html>sign in</html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/next")
			w.WriteHeader(http.StatusFound)
			fmt.Fprint(w, "<html>redirect</html>")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := &http.Client{Transport: &jsonResponseTransport{base: http.DefaultTransport}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	responseErr, ok := errors.AsType[*NonJSONResponseError](err)
	if !ok || responseErr.StatusCode != http.StatusOK || responseErr.Body != body {
		t.Fatalf("read error = %v; want final non-JSON response", err)
	}
}

func TestNonJSONResponseErrorEscapesLoggingCommands(t *testing.T) {
	const body = "<html>##vso[task.setvariable variable=example]value\n##[warning]text</html>"
	responseErr := &NonJSONResponseError{
		Status:      "403 ##vso[example]status",
		StatusCode:  http.StatusForbidden,
		ContentType: "text/html",
		Body:        body,
		Err:         errors.New("invalid JSON"),
	}
	message := responseErr.Error()
	if strings.Contains(message, "##vso[") || strings.Contains(message, "##[") {
		t.Fatal("response diagnostics contain an Azure Pipelines logging command")
	}
	quotedBody := strings.NewReplacer("##vso[", `\x23#vso[`, "##[", `\x23#[`).Replace(strconv.Quote(body))
	if !strings.Contains(message, quotedBody) {
		t.Fatal("response diagnostics omitted the escaped body")
	}
	got, err := strconv.Unquote(quotedBody)
	if err != nil || got != body {
		t.Fatalf("escaped body does not preserve the response: %q, %v", got, err)
	}
}

type failingResponseBody struct {
	err    error
	closed bool
}

func (r *failingResponseBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), r.err
}

func (r *failingResponseBody) Close() error {
	r.closed = true
	return nil
}

func TestJSONResponseBodyPreservesReadErrors(t *testing.T) {
	wantErr := errors.New("connection failed while reading")
	original := &failingResponseBody{err: wantErr}
	body := &jsonResponseBody{ReadCloser: original}
	got, err := io.ReadAll(body)
	if !errors.Is(err, wantErr) || string(got) != "partial" {
		t.Fatalf("ReadAll = %q, %v; want partial data and original error", got, err)
	}
	if err := body.Close(); err != nil || !original.closed {
		t.Fatalf("Close = %v, underlying body closed = %v", err, original.closed)
	}
}

const testResourceLocations = `{"count":2,"value":[
	{"id":"e81700f7-3be2-46de-8624-2eb35882fcaa","area":"location","resourceName":"resourceAreas","routeTemplate":"_apis/resourceAreas/{areaId}","minVersion":"1.0","maxVersion":"7.1","releasedVersion":"7.1","resourceVersion":1},
	{"id":"0cd358e1-9217-4d94-8269-1c1ee6f93dcf","area":"build","resourceName":"Builds","routeTemplate":"{project}/_apis/build/builds/{buildId}","minVersion":"2.0","maxVersion":"7.1","releasedVersion":"7.1","resourceVersion":7}
]}`

func TestBuildClientUsesDiscoveredResourceArea(t *testing.T) {
	const pat = "test-only-pat"
	checkAuth := func(r *http.Request) {
		t.Helper()
		if _, password, ok := r.BasicAuth(); !ok || password != pat {
			t.Error("request did not use the configured PAT")
		}
		if r.Header.Get("X-TFS-FedAuthRedirect") != "Suppress" {
			t.Error("request did not suppress sign-in redirects")
		}
	}
	var buildCalls atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodOptions && r.URL.Path == "/_apis":
			fmt.Fprint(w, testResourceLocations)
		case r.Method == http.MethodGet && r.URL.Path == "/project/_apis/build/builds/42":
			buildCalls.Add(1)
			fmt.Fprint(w, `{"id":42,"status":"completed","result":"succeeded"}`)
		default:
			t.Errorf("unexpected build service request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	var discoveryCalls atomic.Int32
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkAuth(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodOptions && r.URL.Path == "/_apis":
			fmt.Fprint(w, testResourceLocations)
		case r.Method == http.MethodGet && r.URL.Path == "/_apis/resourceAreas":
			discoveryCalls.Add(1)
			fmt.Fprintf(w, `{"count":1,"value":[{"id":%q,"locationUrl":%q}]}`, build.ResourceAreaId.String(), service.URL)
		default:
			t.Errorf("unexpected organization request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer org.Close()

	client, err := NewBuildClient(t.Context(), azuredevops.NewPatConnection(org.URL, pat))
	if err != nil {
		t.Fatal(err)
	}
	project, id := "project", 42
	b, err := client.GetBuild(context.Background(), build.GetBuildArgs{Project: &project, BuildId: &id})
	if err != nil {
		t.Fatal(err)
	}
	if b.Status == nil || *b.Status != build.BuildStatusValues.Completed || discoveryCalls.Load() != 1 || buildCalls.Load() != 1 {
		t.Fatalf("build = %+v, discovery calls = %d, build calls = %d", b, discoveryCalls.Load(), buildCalls.Load())
	}
}

func TestBuildClientInvalidResourceAreas(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
	}{
		{"missing area", `{"count":1,"value":[{"id":"00000000-0000-0000-0000-000000000001","locationUrl":"https://example.com"}]}`, "is not registered"},
		{"missing ID", `{"count":1,"value":[{"locationUrl":"https://example.com"}]}`, "has no ID"},
		{"missing URL", `{"count":1,"value":[{"id":"965220d5-5bb9-42cf-8d67-9b146df2a5a4"}]}`, "has no location URL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodOptions && r.URL.Path == "/_apis":
					fmt.Fprint(w, testResourceLocations)
				case r.Method == http.MethodGet && r.URL.Path == "/_apis/resourceAreas":
					fmt.Fprint(w, tt.body)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			_, err := NewBuildClient(t.Context(), azuredevops.NewAnonymousConnection(server.URL))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewBuildClient error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHTTPClientPreservesConnectionSettings(t *testing.T) {
	timeout := 3 * time.Second
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	connection := azuredevops.NewAnonymousConnection("https://example.com")
	connection.Timeout = &timeout
	connection.TlsConfig = tlsConfig
	client := newHTTPClient(connection)
	diagnosticTransport, ok := client.Transport.(*jsonResponseTransport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	base, ok := diagnosticTransport.base.(*http.Transport)
	if !ok || base.TLSClientConfig != tlsConfig || client.Timeout != timeout {
		t.Fatalf("transport = %#v, timeout = %v", diagnosticTransport.base, client.Timeout)
	}
}
