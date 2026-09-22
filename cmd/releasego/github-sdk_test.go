// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/subcmd"
)

type githubTestTransport func(*http.Request) (*http.Response, error)

func (f githubTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newReleaseGitHubServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing GitHub authorization")
		}
		if r.URL.Path != "/graphql" && r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Error("REST API version changed unexpectedly")
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := s.Client().Transport
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	// Commands construct their own clients. Intercept both REST API and
	// upload requests so no test can contact GitHub or publish anything.
	http.DefaultTransport = githubTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" && r.URL.Host != "uploads.github.com" {
			return nil, errors.New("unexpected external request in release test")
		}
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return base.RoundTrip(r)
	})
}

func releaseGitHubJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

func runGitHubCommand(t *testing.T, handler func(subcmd.ParseFunc) error, args ...string) error {
	t.Helper()
	old, usage := flag.CommandLine, flag.Usage
	defer func() { flag.CommandLine, flag.Usage = old, usage }()
	flag.CommandLine = flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	flag.Usage = func() {}
	return handler(func() error {
		return flag.CommandLine.Parse(append([]string{"-github-pat=test-token"}, args...))
	})
}

func TestGitHubTagRequest(t *testing.T) {
	var created atomic.Bool
	newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/git/refs" {
			t.Errorf("unexpected tag request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"ref": "refs/tags/v1.26.1-1", "sha": "commit-sha"}) {
			t.Errorf("wrong tag payload: %v", body)
		}
		created.Store(true)
		releaseGitHubJSON(t, w, http.StatusCreated, `{"ref":"refs/tags/v1.26.1-1","object":{"sha":"commit-sha"}}`)
	})
	if err := runGitHubCommand(t, handleTag, "-repo=o/r", "-tag=v1.26.1-1", "-commit=commit-sha"); err != nil {
		t.Fatal(err)
	}
	if !created.Load() {
		t.Fatal("tag was not created")
	}
}

func TestGitHubReleaseDayIssueRequest(t *testing.T) {
	var created atomic.Bool
	newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/issues" {
			t.Errorf("unexpected issue request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Title, Body string
			Labels      []string
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.HasSuffix(body.Title, " releases: 1.26.1-1, 1.27.0-1") || !strings.Contains(body.Body, "/cc @runner") || !reflect.DeepEqual(body.Labels, []string{"Area-Release"}) {
			t.Errorf("wrong release issue payload: %+v", body)
		}
		created.Store(true)
		releaseGitHubJSON(t, w, http.StatusCreated, `{"number":42,"html_url":"https://github.com/o/r/issues/42"}`)
	})
	if err := runGitHubCommand(t, handleCreateReleaseDayIssue, "-repo=o/r", "-releases=1.27.0-1,1.26.1-1", "-notify=runner"); err != nil {
		t.Fatal(err)
	}
	if !created.Load() {
		t.Fatal("issue was not created")
	}
}

func TestGitHubCreatePatchRelease(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			var created atomic.Bool
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
					releaseGitHubJSON(t, w, 200, `{"default_branch":"main"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/latest":
					releaseGitHubJSON(t, w, 200, `{"tag_name":"v0.0.17"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/releases":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{
						"tag_name": "v0.0.18", "name": "v0.0.18", "target_commitish": "main", "generate_release_notes": true,
					}) {

						t.Errorf("wrong patch release payload: %v", body)
					}
					created.Store(true)
					releaseGitHubJSON(t, w, 201, `{"id":8,"tag_name":"v0.0.18","html_url":"https://github.com/o/r/releases/tag/v0.0.18"}`)
				default:
					t.Errorf("unexpected release request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := runGitHubCommand(t, handleCreateGoInfraPatch, "-repo=o/r", fmt.Sprintf("-dry-run=%v", dryRun))
			if err != nil || created.Load() == dryRun {
				t.Fatalf("release created=%v, dryRun=%v, error=%v", created.Load(), dryRun, err)
			}
		})
	}
}

func TestGitHubReleaseLifecycle(t *testing.T) {
	for _, failUpload := range []bool{false, true} {
		t.Run(fmt.Sprint(failUpload), func(t *testing.T) {
			dir := t.TempDir()
			assets := buildassets.BuildAssets{GoSrcURL: "https://example.invalid/go.src.tar.gz"}
			paths := assetPaths(dir, assets.GoSrcURL)
			for _, p := range paths {
				if err := os.WriteFile(p, []byte("artifact data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			assetFile := filepath.Join(dir, "assets.json")
			data, err := json.Marshal(assets)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(assetFile, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var created, published, deleted atomic.Bool
			var uploads atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/releases":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"tag_name": "v1.26.1-1", "name": "v1.26.1-1", "body": "Microsoft build of Go v1.26.1-1", "draft": true}) {
						t.Errorf("wrong draft payload: %v", body)
					}
					created.Store(true)
					releaseGitHubJSON(t, w, 201, `{"id":8,"draft":true}`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/releases/8/assets":
					if !created.Load() || published.Load() || r.URL.Query().Get("name") == "" {
						t.Error("asset not uploaded to a draft release")
					}
					if failUpload {
						releaseGitHubJSON(t, w, 500, `{"message":"upload failed"}`)
						return
					}
					uploads.Add(1)
					releaseGitHubJSON(t, w, 201, `{"id":9}`)
				case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/releases/8":
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"draft": false}) {
						t.Errorf("publish changed more than the draft flag: %v", body)
					}
					if int(uploads.Load()) != len(paths)+1 {
						t.Error("release published before all assets were uploaded")
					}
					published.Store(true)
					releaseGitHubJSON(t, w, 200, `{"id":8,"draft":false,"html_url":"https://github.com/o/r/releases/tag/v1.26.1-1"}`)
				case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/releases/8":
					deleted.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected release request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err = runGitHubCommand(t, handleRepoRelease, "-repo=o/r", "-tag=v1.26.1-1", "-build-dir="+dir, "-build-asset-json="+assetFile)
			if (err != nil) != failUpload || !created.Load() || published.Load() == failUpload || deleted.Load() != failUpload {
				t.Fatalf("release state: created=%v published=%v deleted=%v error=%v", created.Load(), published.Load(), deleted.Load(), err)
			}
		})
	}
}

func TestGitHubUpdateDLRequests(t *testing.T) {
	var tree, commit, ref, pull atomic.Bool
	var mutations atomic.Int32
	assetJSON := `{"goVersion":"1.26.1-1"}`
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(assetJSON)))
	newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/releases/releases/tags/v1.26.1-1":
			releaseGitHubJSON(t, w, 200, `{"assets":[{"id":9,"name":"assets.json"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/releases/releases/assets/9":
			if _, err := io.WriteString(w, assetJSON); err != nil {
				t.Error(err)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/lab/contents/dl/msgo1.26.1-1/main.go":
			releaseGitHubJSON(t, w, 404, `{"message":"Not Found"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/lab/git/ref/heads/main":
			releaseGitHubJSON(t, w, 200, `{"object":{"sha":"base-sha"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/lab/git/commits/base-sha":
			releaseGitHubJSON(t, w, 200, `{"sha":"base-sha","tree":{"sha":"base-tree"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/lab/git/trees":
			var body struct {
				BaseTree string `json:"base_tree"`
				Tree     []struct{ Path, Content, Mode string }
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.BaseTree != "base-tree" || len(body.Tree) != 1 || body.Tree[0].Path != "dl/msgo1.26.1-1/main.go" || body.Tree[0].Mode != "100644" || !strings.Contains(body.Tree[0].Content, hash) {
				t.Errorf("wrong generated tree: %+v", body)
			}
			tree.Store(true)
			releaseGitHubJSON(t, w, 201, `{"sha":"new-tree"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/lab/git/commits":
			var body struct {
				Message, Tree string
				Parents       []string
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Tree != "new-tree" || body.Message == "" || !reflect.DeepEqual(body.Parents, []string{"base-sha"}) {
				t.Errorf("wrong commit payload: %+v", body)
			}
			commit.Store(true)
			releaseGitHubJSON(t, w, 201, `{"sha":"new-commit"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/lab/git/refs":
			var body struct{ Ref, SHA string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || !strings.HasPrefix(body.Ref, "refs/heads/dev/dl/msgo-1.26.1-1/") || body.SHA != "new-commit" {
				t.Errorf("wrong ref payload: %+v", body)
			}
			ref.Store(true)
			releaseGitHubJSON(t, w, 201, `{"ref":"created"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/lab/pulls":
			var body struct{ Head, Base, Title, Body string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Base != "main" || !strings.HasPrefix(body.Head, "dev/dl/msgo-1.26.1-1/") || body.Title == "" || body.Body == "" {
				t.Errorf("wrong PR payload: %+v", body)
			}
			pull.Store(true)
			releaseGitHubJSON(t, w, 201, `{"number":42,"node_id":"PR_node","html_url":"https://github.com/o/lab/pull/42"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			mutations.Add(1)
			releaseGitHubJSON(t, w, 200, `{"data":{}}`)
		default:
			t.Errorf("unexpected update-dl request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := runGitHubCommand(t, updateDL, "-repo=o/lab", "-go-repo=o/releases", "-versions=1.26.1-1", "-github-reviewer-pat=reviewer-token"); err != nil {
		t.Fatal(err)
	}
	if !tree.Load() || !commit.Load() || !ref.Load() || !pull.Load() || mutations.Load() != 2 {
		t.Fatal("update-dl did not complete its existing release workflow")
	}
}
