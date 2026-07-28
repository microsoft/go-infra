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
