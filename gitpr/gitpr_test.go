// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitpr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type testAuther func(*http.Request) error

func (f testAuther) InsertHTTPAuth(r *http.Request) error { return f(r) }

func testPRServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := client
	t.Cleanup(func() { client = old })
	client = *s.Client()
	base := client.Transport
	client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" && r.URL.Host != u.Host {
			return nil, errors.New("unexpected host in PR test")
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return base.RoundTrip(r)
	})
	return s
}

func writePRResponse(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

func TestPostGitHubSDK(t *testing.T) {
	request := &GitHubRequest{
		Head: "bot:dev/update", Base: "main", Title: "Update Go", Body: "details",
		MaintainerCanModify: false, Draft: false,
	}
	var sent atomic.Bool
	testPRServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/pulls" {
			t.Errorf("unexpected PR request: %s %s", r.Method, r.URL.Path)
		}
		_, password, ok := r.BasicAuth()
		if !ok || password != "test-pat" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("PR request lost authentication or REST API version")
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		want := map[string]any{"head": request.Head, "base": request.Base, "title": request.Title, "body": request.Body, "maintainer_can_modify": false, "draft": false}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PR payload = %v, want %v", got, want)
		}
		sent.Store(true)
		writePRResponse(t, w, http.StatusCreated, `{"number":42,"node_id":"PR_node","html_url":"https://github.com/o/r/pull/42"}`)
	})
	got, err := PostGitHub("o/r", request, githubutil.GitHubPATAuther{PAT: "test-pat"})
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Load() || got.Number != 42 || got.NodeID != "PR_node" || got.HTMLURL != "https://github.com/o/r/pull/42" {
		t.Errorf("created PR response = %+v", got)
	}
}

func TestPostGitHubErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		duplicate bool
	}{
		{"duplicate", 422, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for bot:dev/update."}]}`, true},
		{"other validation", 422, `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"invalid","message":"Invalid base"}]}`, false},
		{"unauthorized", 401, `{"message":"Bad credentials"}`, false},
		{"forbidden", 403, `{"message":"Forbidden"}`, false},
		{"server error", 500, `{"message":"Unavailable"}`, false},
		{"invalid JSON", 201, `<html>sign in</html>`, false},
		{"missing PR", 201, `{}`, false},
		{"wrong success code", 200, `{"number":42}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testPRServer(t, func(w http.ResponseWriter, r *http.Request) { writePRResponse(t, w, tc.status, tc.body) })
			_, err := PostGitHub("o/r", &GitHubRequest{Head: "bot:dev/update", Base: "main", Title: "test"}, githubutil.GitHubPATAuther{PAT: "test-pat"})
			if err == nil || errors.Is(err, ErrPRAlreadyExists) != tc.duplicate {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.status >= 400 && !tc.duplicate {
				apiErr, ok := errors.AsType[*github.ErrorResponse](err)
				if !ok || apiErr.Response.StatusCode != tc.status {
					t.Errorf("SDK status error not preserved: %v", err)
				}
			}
		})
	}
}

func TestPostGitHubAuthError(t *testing.T) {
	testPRServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent after authentication failed")
		w.WriteHeader(http.StatusUnauthorized)
	})
	want := errors.New("authentication failed")
	_, err := PostGitHub("o/r", &GitHubRequest{}, testAuther(func(*http.Request) error { return want }))
	if !errors.Is(err, want) {
		t.Errorf("got %v, want authentication failure", err)
	}
}

func TestPostGitHubRedirectDoesNotForwardCredentials(t *testing.T) {
	var redirected atomic.Bool
	var server *httptest.Server
	server = testPRServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			redirected.Store(true)
			if r.Header.Get("Authorization") != "" {
				t.Error("redirect acquired API credentials")
			}
			writePRResponse(t, w, http.StatusCreated, `{"number":42}`)
			return
		}
		w.Header().Set("Location", server.URL+"/redirect")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	_, err := PostGitHub("o/r", &GitHubRequest{Head: "dev/update", Base: "main"}, githubutil.GitHubPATAuther{PAT: "test-pat"})
	if err != nil || !redirected.Load() {
		t.Fatalf("redirect test failed: %v", err)
	}
}

func TestPRAuthenticationScope(t *testing.T) {
	for _, tc := range []struct {
		url, want string
	}{
		{"https://api.github.com/repos/o/r/pulls", "Bearer fresh-token"},
		{"http://api.github.com/repos/o/r/pulls", ""},
		{"https://other.example/repos/o/r/pulls", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer previous-token")
			transport := authTransport{
				auther: testAuther(func(r *http.Request) error {
					r.Header.Set("Authorization", "Bearer fresh-token")
					return nil
				}),
				base: testTransport(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != tc.want {
						t.Error("credentials sent outside their trusted HTTPS API host")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}),
			}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if req.Header.Get("Authorization") != "Bearer previous-token" {
				t.Error("authentication transport mutated its caller's request")
			}
		})
	}
}
