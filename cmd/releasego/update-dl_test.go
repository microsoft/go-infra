// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/go-infra/githubutil"
)

type dlTestAuther func(*http.Request) error

func (a dlTestAuther) InsertHTTPAuth(req *http.Request) error {
	return a(req)
}

func TestDLGitAuthentication(t *testing.T) {
	const token = "test-dl-token"
	for _, tt := range []struct {
		name   string
		auther githubutil.HTTPRequestAuther
		user   string
	}{
		{name: "anonymous"},
		{name: "PAT", auther: githubutil.GitHubPATAuther{PAT: token}, user: "_"},
		{
			name: "App",
			auther: dlTestAuther(func(req *http.Request) error {
				req.Header.Set("Authorization", "token "+token)
				return nil
			}),
			user: "x-access-token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requested atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested.Store(true)
				user, password, ok := r.BasicAuth()
				if tt.auther == nil {
					if r.Header.Get("Authorization") != "" {
						t.Error("anonymous Git request included authentication")
					}
				} else if !ok || user != tt.user || password != token {
					t.Error("Git request did not use the supplied credentials")
				}
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			update := dlRepoUpdate{RepoURL: server.URL + "/repo.git", Auther: tt.auther}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_TERMINAL_PROMPT", "0")
			for _, args := range [][]string{
				{"clone", update.RepoURL, filepath.Join(t.TempDir(), "clone")},
				{"push", update.RepoURL, "HEAD:refs/heads/test"},
			} {
				cmd, err := update.gitCommand("", args...)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(strings.Join(cmd.Args, " "), token) {
					t.Fatal("Git arguments contain the token")
				}
				for _, env := range cmd.Env {
					if header, ok := strings.CutPrefix(env, "GO_DL_GIT_AUTH="); ok &&
						strings.Contains(strings.Join(cmd.Args, " "), header) {
						t.Fatal("Git arguments contain the authorization header")
					}
				}
			}
			cmd, err := update.gitCommand("", "clone", update.RepoURL, filepath.Join(t.TempDir(), "clone"))
			if err != nil {
				t.Fatal(err)
			}
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatal("Git unexpectedly succeeded against a forbidden repository")
			} else if strings.Contains(string(out), token) {
				t.Fatal("Git output contains the token")
			}
			if !requested.Load() {
				t.Fatal("Git did not contact the repository")
			}
		})
	}
}

func TestDLGitAuthenticationError(t *testing.T) {
	authErr := errors.New("authentication unavailable")
	update := dlRepoUpdate{
		RepoURL: "https://github.com/o/dl.git",
		Auther: dlTestAuther(func(*http.Request) error {
			return authErr
		}),
	}
	if _, err := update.gitCommand("", "clone", update.RepoURL); !errors.Is(err, authErr) {
		t.Fatalf("got %v, want authentication error", err)
	}
}

func TestWriteDLManifests(t *testing.T) {
	repoDir := t.TempDir()
	assetsJSON := []byte("{\n  \"version\": \"1.25.8-1\"\n}\n")
	releases := []dlRelease{
		{Version: "1.25.8-1", AssetsJSON: assetsJSON},
	}

	if err := writeDLManifests(repoDir, releases); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, dlManifestPath("1.25.8-1"))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(assetsJSON) {
		t.Fatalf("manifest content changed:\ngot:  %q\nwant: %q", got, assetsJSON)
	}
	if err := writeDLManifests(repoDir, releases); err == nil {
		t.Fatal("writeDLManifests overwrote an existing manifest")
	}
}
