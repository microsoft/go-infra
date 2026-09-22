// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/internal/_apis/build/builds" ||
			request.URL.Query().Get("definitionId") != "1023" ||
			request.Header.Get("Authorization") != "Bearer test-token" {

			t.Fatalf("request = %s %s, authorization = %q", request.Method, request.URL, request.Header.Get("Authorization"))
		}
		var body struct {
			Definition struct {
				ID int `json:"id"`
			} `json:"definition"`
			SourceBranch       string         `json:"sourceBranch"`
			SourceVersion      string         `json:"sourceVersion"`
			TemplateParameters map[string]any `json:"templateParameters"`
			Parameters         string         `json:"parameters"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var variables map[string]string
		if err := json.Unmarshal([]byte(body.Parameters), &variables); err != nil {
			t.Fatal(err)
		}
		if body.Definition.ID != 1023 || body.SourceBranch != "refs/heads/main" ||
			body.SourceVersion != "abc123" || body.TemplateParameters["dryRun"] != true ||
			variables["correlation"] != "session" {

			t.Fatalf("body = %#v, variables = %#v", body, variables)
		}
		_, _ = io.WriteString(response, `{"id":888}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := client.Queue(context.Background(), QueueRequest{
		DefinitionID: 1023, SourceBranch: "refs/heads/main", SourceVersion: "abc123",
		TemplateParameters: map[string]any{"dryRun": true},
		Variables:          map[string]string{"correlation": "session"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if buildID != 888 {
		t.Fatalf("build ID = %d, want 888", buildID)
	}
}

func TestQueueRejectsInvalidDefinition(t *testing.T) {
	client, err := NewClient("https://example.invalid", "internal", http.DefaultClient, staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Queue(context.Background(), QueueRequest{}); err == nil {
		t.Fatal("queue accepted an invalid definition")
	}
}

func TestQueueRedactsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied test-token")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Queue(context.Background(), QueueRequest{DefinitionID: 1023})
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
}
