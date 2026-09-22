// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/go-github/v92/github"
)

func TestListPullRequestFilesPaginates(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/7/files" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/pulls/7/files?page=2>; rel="next"`, server.URL))
			fmt.Fprint(w, `[{"filename":"first.go","additions":1,"deletions":2}]`)
		case "2":
			fmt.Fprint(w, `[{"filename":"second.go","additions":3,"deletions":4}]`)
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	api := testGitHubAPI(t, server)
	files, err := api.ListPullRequestFiles(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []changedFile{
		{Path: "first.go"},
		{Path: "second.go"},
	}
	if !slices.Equal(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
}

func TestRemoveLabelAcceptsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/repos/o/r/issues/7/labels/stale" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer server.Close()

	api := testGitHubAPI(t, server)
	if err := api.RemoveLabel(context.Background(), "o", "r", 7, "stale"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateLabelRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/labels" {
			t.Errorf("unexpected label request: %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Name, Color, Description string }
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name != "size/XS" || body.Color != "00ff00" || body.Description != "Small change" {
			t.Errorf("wrong label request: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"name":"size/XS","color":"00ff00","description":"Small change"}`)
	}))
	defer server.Close()
	api := testGitHubAPI(t, server)
	if err := api.CreateLabel(t.Context(), "o", "r", labelDefinition{Name: "size/XS", Color: "00ff00", Description: "Small change"}); err != nil {
		t.Fatal(err)
	}
}

func TestPullRequestLabelValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":7,"additions":3,"deletions":1,"changed_files":2,"labels":[{"name":"kind/bug"}]}`)
	}))
	defer server.Close()
	api := testGitHubAPI(t, server)
	pr, err := api.GetPullRequest(t.Context(), "o", "r", 7)
	if err != nil || pr.Additions != 3 || pr.Deletions != 1 || pr.ChangedFiles != 2 || !slices.Equal(pr.Labels, []string{"kind/bug"}) {
		t.Fatalf("pull request = %+v, %v", pr, err)
	}
}

func testGitHubAPI(t *testing.T, server *httptest.Server) *githubAPI {
	t.Helper()
	client, err := github.NewClient(
		github.WithHTTPClient(server.Client()),
		github.WithURLs(new(server.URL+"/"), nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &githubAPI{client: client}
}
