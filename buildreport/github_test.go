// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildreport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
	"golang.org/x/oauth2"
)

type reportTransport func(*http.Request) (*http.Response, error)

func (f reportTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNotifyGitHubCommentRequest(t *testing.T) {
	var posted atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/issues/42/comments" {
			t.Errorf("unexpected comment request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-pat" {
			t.Error("comment request lost authentication")
		}
		var body struct{ Body string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.Contains(body.Body, "Build failed!") || !strings.Contains(body.Body, "https://example.invalid/build/123") {
			t.Errorf("wrong notification body: %q", body.Body)
		}
		posted.Store(true)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"id": 5, "html_url": "https://github.com/o/r/issues/42#issuecomment-5"}); err != nil {
			t.Error(err)
		}
	}))
	defer s.Close()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := s.Client()
	base := hc.Transport
	hc.Transport = reportTransport(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return base.RoundTrip(r)
	})
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, hc)
	flags := githubutil.GitHubAuthFlags{GitHubPat: new("test-pat")}
	state := State{ID: "123", Name: "test pipeline", URL: "https://example.invalid/build/123", Status: SymbolFailed}
	if err := Notify(ctx, "o", "r", flags, 42, state); err != nil {
		t.Fatal(err)
	}
	if !posted.Load() {
		t.Fatal("notification was not posted")
	}
}

func TestUpdateGitHubIssueBodyPreservesMetadata(t *testing.T) {
	tests := []struct {
		name            string
		state           State
		completeRelease bool
		wantClosed      bool
		pinned          bool
	}{
		{"completed-images", State{Name: releaseImagesPipelineName, Status: SymbolSucceeded}, true, true, true},
		{"completed-images-already-unpinned", State{Name: releaseImagesPipelineName, Status: SymbolSucceeded}, true, true, false},
		{"renamed-final-pipeline", State{Name: "renamed-final-pipeline", Status: SymbolSucceeded}, true, true, true},
		{"images-without-completion-signal", State{Name: releaseImagesPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"failed-images", State{Name: releaseImagesPipelineName, Status: SymbolFailed}, true, false, true},
		{"in-progress-images", State{Name: releaseImagesPipelineName, Status: SymbolInProgress}, true, false, true},
		{"not-started-images", State{Name: releaseImagesPipelineName, Status: SymbolNotStarted}, true, false, true},
		{"unknown-images-status", State{Name: releaseImagesPipelineName, Status: "?"}, true, false, true},
		{"completed-version-release", State{Name: releaseBuildPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-innerloop", State{Name: releaseInnerloopPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-release-start", State{Name: releaseStartPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-azure-linux", State{Name: releaseUpdateAzureLinuxPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-go-build", State{Name: microsoftGoPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-go-innerloop", State{Name: microsoftGoInnerloopPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"completed-go-images-build", State{Name: microsoftGoImagesPipelineName, Status: SymbolSucceeded}, false, false, true},
		{"unknown-pipeline", State{Name: "unknown", Status: SymbolSucceeded}, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := map[string]any{"body": "new report"}
			if tt.wantClosed {
				want["state"] = "closed"
				want["state_reason"] = "completed"
			}
			var updated atomic.Bool
			var pinned atomic.Bool
			pinned.Store(tt.pinned)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/graphql" {
					if !tt.wantClosed || !updated.Load() {
						t.Error("issue pin synchronized without closing the release tracker")
					}
					serveReportIssuePin(t, w, r, &pinned)
					return
				}
				if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/42" {
					t.Errorf("unexpected issue update: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, want) {
					t.Errorf("issue update = %v, want %v", body, want)
				}
				updated.Store(true)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"id": 42, "number": 42, "node_id": "I_tracker"}); err != nil {
					t.Error(err)
				}
			}))
			defer s.Close()
			client, err := github.NewClient(github.WithHTTPClient(s.Client()), github.WithURLs(new(s.URL+"/"), nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := updateGitHubIssueBody(t.Context(), client, "o", "r", 42, "new report", tt.state, tt.completeRelease); err != nil {
				t.Fatal(err)
			}
			if !updated.Load() {
				t.Fatal("issue was not updated")
			}
			if want := tt.pinned && !tt.wantClosed; pinned.Load() != want {
				t.Errorf("issue pinned = %t, want %t", pinned.Load(), want)
			}
		})
	}
}

func TestUpdateGitHubIssueBodyRetries(t *testing.T) {
	tests := []struct {
		name       string
		stage      string
		persistent bool
	}{
		{"transient-update-error", "update", false},
		{"persistent-update-error", "update", true},
		{"transient-pin-error", "pin", false},
		{"persistent-pin-error", "pin", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			var pinAttempts atomic.Int32
			var pinned atomic.Bool
			pinned.Store(true)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/graphql" {
					attempt := pinAttempts.Add(1)
					if tt.stage == "pin" && (attempt == 1 || tt.persistent) {
						http.Error(w, "issue pin update failed", http.StatusInternalServerError)
						return
					}
					serveReportIssuePin(t, w, r, &pinned)
					return
				}
				if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/42" {
					t.Errorf("unexpected issue update: %s %s", r.Method, r.URL.Path)
				}
				want := map[string]any{"body": "new report", "state": "closed", "state_reason": "completed"}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, want) {
					t.Errorf("issue update = %v, want %v", body, want)
				}
				attempt := attempts.Add(1)
				if tt.stage == "update" && (attempt == 1 || tt.persistent) {
					http.Error(w, "issue update failed", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"id": 42, "number": 42, "node_id": "I_tracker"}); err != nil {
					t.Error(err)
				}
			}))
			defer s.Close()
			client, err := github.NewClient(github.WithHTTPClient(s.Client()), github.WithURLs(new(s.URL+"/"), nil))
			if err != nil {
				t.Fatal(err)
			}
			state := State{Name: releaseImagesPipelineName, Status: SymbolSucceeded}
			err = updateGitHubIssueBody(t.Context(), client, "o", "r", 42, "new report", state, true)
			if (err != nil) != tt.persistent {
				t.Errorf("updateGitHubIssueBody() error = %v, wantErr %v", err, tt.persistent)
			}
			if tt.stage == "update" {
				if attempts.Load() < 2 {
					t.Error("issue update was not retried")
				}
				if tt.persistent && pinAttempts.Load() != 0 {
					t.Error("issue pin synchronized after failed issue update")
				}
			} else {
				if attempts.Load() != 1 {
					t.Errorf("issue updated %d times while retrying pin synchronization, want 1", attempts.Load())
				}
				if pinAttempts.Load() < 2 {
					t.Error("issue pin synchronization was not retried")
				}
				if tt.persistent && err != nil && !strings.Contains(err.Error(), "sync completed release tracker pin") {
					t.Errorf("pin synchronization error lost context: %v", err)
				}
			}
			if !tt.persistent && pinned.Load() {
				t.Error("completed release tracker remains pinned")
			}
		})
	}
}

func serveReportIssuePin(t *testing.T, w http.ResponseWriter, r *http.Request, pinned *atomic.Bool) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
		t.Errorf("unexpected issue pin request: %s %s", r.Method, r.URL.Path)
	}
	var request struct {
		Query     string
		Variables map[string]string
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
		http.Error(w, "invalid GraphQL request", http.StatusBadRequest)
		return
	}
	if !reflect.DeepEqual(request.Variables, map[string]string{"issueID": "I_tracker"}) {
		t.Errorf("wrong issue pin variables: %v", request.Variables)
	}
	var response string
	switch {
	case strings.Contains(request.Query, "updateIssuePin: unpinIssue("):
		if !pinned.Swap(false) {
			t.Error("already-unpinned issue was unpinned again")
		}
		response = `{"data":{"updateIssuePin":{"issue":{"id":"I_tracker","isPinned":false}}}}`
	case strings.Contains(request.Query, "node(id: $issueID)"):
		response = fmt.Sprintf(`{"data":{"node":{"id":"I_tracker","state":"CLOSED","isPinned":%t}}}`, pinned.Load())
	default:
		t.Errorf("unexpected issue pin operation: %s", request.Query)
		http.Error(w, "unexpected GraphQL operation", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := fmt.Fprint(w, response); err != nil {
		t.Error(err)
	}
}
