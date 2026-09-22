// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v92/github"
	"golang.org/x/oauth2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeGitHubJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

// Exercise the real authentication factories with an injected HTTP client.
// Even token-refresh requests are required to stay on the local transport.
func githubTestContext(t *testing.T, handler http.HandlerFunc) context.Context {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := s.Client()
	base := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
			return nil, errors.New("unexpected GitHub API URL in test")
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return base.RoundTrip(r)
	})
	return context.WithValue(t.Context(), oauth2.HTTPClient, hc)
}

func TestNewClientTokenAndClone(t *testing.T) {
	var requests atomic.Int32
	ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer test-pat" {
			t.Error("PAT client sent an unexpected request or authorization")
		}
		if r.Header.Get("User-Agent") != "go-infra-test" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("cloned client lost its user agent or changed the REST API version")
		}
		writeGitHubJSON(t, w, `{"login":"test-user"}`)
	})
	client, err := NewClient(ctx, "test-pat")
	if err != nil {
		t.Fatal(err)
	}
	client, err = client.Clone(github.WithUserAgent("go-infra-test"))
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := client.Users.Get(ctx, "")
	if err != nil || user.GetLogin() != "test-user" {
		t.Fatalf("PAT request failed: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := client.Users.Get(canceled, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled request returned %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("canceled request reached the server")
	}
	if _, err := NewClient(ctx, ""); err == nil {
		t.Fatal("empty PAT accepted")
	}
}

func TestGitHubAppAuthenticationAndRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	var tokens, calls atomic.Int32
	checkJWT := func(r *http.Request) {
		t.Helper()
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			t.Error("missing JWT bearer authorization")
		}
		_, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("test-client"))
		if err != nil {
			t.Error("invalid app JWT")
		}
	}
	ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			checkJWT(r)
			writeGitHubJSON(t, w, `{"slug":"test-app"}`)
		case "/app/installations/42/access_tokens":
			if r.Method != http.MethodPost {
				t.Error("unexpected installation-token method")
			}
			checkJWT(r)
			n := tokens.Add(1)
			expires := time.Now().Add(time.Hour)
			if n == 1 {
				expires = time.Now().Add(-time.Hour)
			}
			writeGitHubJSON(t, w, fmt.Sprintf(`{"token":"installation-%d","expires_at":%q}`, n, expires.UTC().Format(time.RFC3339)))
		case "/user":
			n := calls.Add(1)
			want := "Bearer installation-2"
			if n == 1 {
				want = "Bearer installation-1"
			}
			if r.Header.Get("Authorization") != want {
				t.Error("installation token was not refreshed/reused")
			}
			writeGitHubJSON(t, w, `{"login":"test-app[bot]"}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	app, err := newAppClient(ctx, "test-client", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := app.Apps.Get(ctx, "")
	if err != nil || got.GetSlug() != "test-app" {
		t.Fatalf("app request failed: %v", err)
	}
	installation, err := newInstallationClient(ctx, "test-client", 42, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		user, _, err := installation.Users.Get(ctx, "")
		if err != nil || user.GetLogin() != "test-app[bot]" {
			t.Fatalf("installation request failed: %v", err)
		}
	}
	if tokens.Load() != 2 || calls.Load() != 3 {
		t.Fatalf("got %d token requests and %d API requests", tokens.Load(), calls.Load())
	}
}

func TestCreateBranchRequestAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(strconv.FormatBool(conflict), func(t *testing.T) {
			ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/git/ref/heads/main":
					writeGitHubJSON(t, w, `{"ref":"refs/heads/main","object":{"sha":"base-sha"}}`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/git/refs":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"ref": "refs/heads/dev/upgrade", "sha": "base-sha"}) {
						t.Errorf("wrong ref creation payload: %v", body)
					}
					if conflict {
						w.WriteHeader(http.StatusUnprocessableEntity)
						writeGitHubJSON(t, w, `{"message":"Validation Failed","errors":[{"resource":"Reference","code":"already_exists"}]}`)
					} else {
						writeGitHubJSON(t, w, `{"ref":"refs/heads/dev/upgrade","object":{"sha":"base-sha"}}`)
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			client, err := NewClient(ctx, "test-pat")
			if err != nil {
				t.Fatal(err)
			}
			err = CreateBranch(ctx, client, "o", "r", "dev/upgrade", "main")
			if conflict {
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Errorf("wrong conflict error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubContentAndNotFoundErrors(t *testing.T) {
	ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/o/r/contents/") && r.URL.Query().Get("ref") != "branch/with space" {
			t.Error("content ref was not preserved")
		}
		switch r.URL.Path {
		case "/repos/o/r/contents/file.txt":
			writeGitHubJSON(t, w, `{"type":"file","name":"file.txt","encoding":"base64","content":"aGVsbG8K"}`)
		case "/repos/o/r/contents/dir":
			writeGitHubJSON(t, w, `[{"type":"file","name":"a.go","size":7},{"type":"dir","name":"sub","size":0}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			writeGitHubJSON(t, w, `{"message":"Not Found"}`)
		}
	})
	client, err := NewClient(ctx, "test-pat")
	if err != nil {
		t.Fatal(err)
	}
	remote := NewRefFS(ctx, client, "o", "r", "branch/with space")
	b, err := remote.ReadFile("file.txt")
	if err != nil || string(b) != "hello\n" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	entries, err := remote.ReadDir("dir")
	if err != nil || len(entries) != 2 || entries[0].Name() != "a.go" || !entries[1].IsDir() {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	if _, err := remote.ReadFile("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := FetchRepository(ctx, client, "o", "missing"); !errors.Is(err, ErrRepositoryNotExists) {
		t.Errorf("missing repository: %v", err)
	}
}

func TestFetchEachPage(t *testing.T) {
	ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/labels" {
			t.Errorf("unexpected pagination request: %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<https://api.github.com/repos/o/r/labels?page=2>; rel="next"`)
			writeGitHubJSON(t, w, `[{"name":"first"}]`)
		} else if r.URL.Query().Get("page") == "2" {
			writeGitHubJSON(t, w, `[{"name":"second"}]`)
		} else {
			t.Errorf("unexpected page: %s", r.URL.RawQuery)
			http.Error(w, "bad page", http.StatusBadRequest)
		}
	})
	client, err := NewClient(ctx, "test-pat")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	err = FetchEachPage(func(opts github.ListOptions) (*github.Response, error) {
		labels, resp, err := client.Issues.ListLabels(ctx, "o", "r", &opts)
		for _, label := range labels {
			names = append(names, label.GetName())
		}
		return resp, err
	})
	if err != nil || !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("pagination = %v, %v", names, err)
	}
}

func TestForkAcceptedAndRateLimit(t *testing.T) {
	ctx := githubTestContext(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/upstream/r/forks":
			w.WriteHeader(http.StatusAccepted)
			writeGitHubJSON(t, w, `{"name":"r","owner":{"login":"bot"}}`)
		case "/repos/bot/r":
			writeGitHubJSON(t, w, `{"name":"r","owner":{"login":"bot"},"html_url":"https://github.com/bot/r"}`)
		default:
			w.Header().Set("X-RateLimit-Limit", "60")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			writeGitHubJSON(t, w, `{"message":"API rate limit exceeded"}`)
		}
	})
	client, err := NewClient(ctx, "test-pat")
	if err != nil {
		t.Fatal(err)
	}
	fork, err := FullyCreateFork(ctx, client, "upstream", "r")
	if err != nil || fork.GetHTMLURL() != "https://github.com/bot/r" {
		t.Fatalf("accepted fork = %v, %v", fork, err)
	}
	err = Retry(func() error {
		_, _, err := client.Users.Get(ctx, "")
		return err
	})
	if _, ok := errors.AsType[*github.RateLimitError](err); !ok {
		t.Errorf("rate-limit error type not preserved: %v", err)
	}
}
