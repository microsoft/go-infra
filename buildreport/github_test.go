// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildreport

import (
	"context"
	"encoding/json"
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
	var updated atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/42" {
			t.Errorf("unexpected issue update: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"body": "new report"}) {
			t.Errorf("body update would change issue metadata: %v", body)
		}
		updated.Store(true)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"id": 42}); err != nil {
			t.Error(err)
		}
	}))
	defer s.Close()
	client, err := github.NewClient(github.WithHTTPClient(s.Client()), github.WithURLs(new(s.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := updateGitHubIssueBody(t.Context(), client, "o", "r", 42, "new report"); err != nil {
		t.Fatal(err)
	}
	if !updated.Load() {
		t.Fatal("issue was not updated")
	}
}
