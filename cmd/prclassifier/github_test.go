// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/google/go-github/v65/github"
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

func testGitHubAPI(t *testing.T, server *httptest.Server) *githubAPI {
	t.Helper()
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client := github.NewClient(server.Client())
	client.BaseURL = baseURL
	return &githubAPI{client: client}
}
