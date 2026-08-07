// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdorepo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

func TestGetJSONFileAtCommit(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/internal/_apis/git/repositories/microsoft-go-images/items" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.URL.Query().Get("path") != "/src/microsoft/versions.json" ||
			request.URL.Query().Get("versionDescriptor.version") != commit ||
			request.URL.Query().Get("versionDescriptor.versionType") != "commit" {

			t.Fatalf("query = %v", request.URL.Query())
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(response).Encode(map[string]string{
			"content": `{"1.26":{"version":"1.26.5","revision":"2"}}`,
		})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	var model map[string]struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
	}
	if err := client.GetJSONFileAtCommit(context.Background(), "/src/microsoft/versions.json", commit, &model); err != nil {
		t.Fatal(err)
	}
	if model["1.26"].Version != "1.26.5" || model["1.26"].Revision != "2" {
		t.Fatalf("model = %#v", model)
	}
}

func TestGetFileAtBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("path") != "/eng/pipeline/go-docker-rolling-internal-pipeline.yml" ||
			request.URL.Query().Get("versionDescriptor.version") != "microsoft/main" ||
			request.URL.Query().Get("versionDescriptor.versionType") != "branch" {

			t.Fatalf("query = %v", request.URL.Query())
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"content": "parameters:\n"})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.GetFileAtBranch(
		context.Background(),
		"/eng/pipeline/go-docker-rolling-internal-pipeline.yml",
		"refs/heads/microsoft/main",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "parameters:\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestVerifyCommit(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/internal/_apis/git/repositories/microsoft-go-images/commits/"+commit {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.URL.Query().Get("api-version") != "7.1" {
			t.Fatalf("query = %v", request.URL.Query())
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"commitId": strings.ToUpper(commit)})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyCommit(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
}

func TestGetBranchTip(t *testing.T) {
	const commit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/internal/_apis/git/repositories/microsoft-go-images/refs" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.URL.Query().Get("filter") != "heads/microsoft/main" {
			t.Fatalf("query = %v", request.URL.Query())
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"value": []map[string]string{{
			"name": "refs/heads/microsoft/main", "objectId": strings.ToUpper(commit),
		}}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	tip, err := client.GetBranchTip(context.Background(), "refs/heads/microsoft/main")
	if err != nil {
		t.Fatal(err)
	}
	if tip.Name != "refs/heads/microsoft/main" || tip.ObjectID != commit {
		t.Fatalf("tip = %#v", tip)
	}
}

func TestGetBranchTipRejectsUnexpectedResponse(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "missing", value: []any{}},
		{name: "ambiguous", value: []map[string]string{
			{"name": "refs/heads/microsoft/main", "objectId": "81ce9afc2b75ec4e153dd15fc3c7539b12024945"},
			{"name": "refs/heads/microsoft/main-old", "objectId": "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"},
		}},
		{name: "wrong ref", value: []map[string]string{{
			"name": "refs/heads/main", "objectId": "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
		}}},
		{name: "malformed commit", value: []map[string]string{{
			"name": "refs/heads/microsoft/main", "objectId": "not-a-commit",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(response).Encode(map[string]any{"value": test.value})
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetBranchTip(context.Background(), "microsoft/main"); err == nil {
				t.Fatal("unexpected branch response was accepted")
			}
		})
	}
}

func TestGetJSONFileAtCommitRedactsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied test-token")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", "microsoft-go-images", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	err = client.GetJSONFileAtCommit(context.Background(), "/src/microsoft/versions.json", "81ce9afc2b75ec4e153dd15fc3c7539b12024945", &map[string]any{})
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
}
