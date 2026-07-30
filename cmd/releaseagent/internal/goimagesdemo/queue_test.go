// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesdemo

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

func TestHTTPQueueClientSendsOnlyProductionPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Query().Get("definitionId") != "1023" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body struct {
			Definition struct {
				ID int `json:"id"`
			} `json:"definition"`
			SourceBranch       string            `json:"sourceBranch"`
			SourceVersion      string            `json:"sourceVersion"`
			TemplateParameters map[string]string `json:"templateParameters"`
			Parameters         string            `json:"parameters"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Definition.ID != DefinitionID || body.SourceBranch != SourceBranch || body.SourceVersion != testCommit {
			t.Fatalf("body target = %#v", body)
		}
		if body.TemplateParameters["publishRepoPrefix"] != "public/" ||
			body.TemplateParameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" {

			t.Fatalf("template parameters = %#v", body.TemplateParameters)
		}
		var variables map[string]string
		if err := json.Unmarshal([]byte(body.Parameters), &variables); err != nil {
			t.Fatal(err)
		}
		if variables[correlationVariable] != "session-1" || variables[executionDigestVariable] != testDigest ||
			variables[versionsVariable] != `["1.26.5-2"]` || variables[sourceBuildVariable] != "3019035" {

			t.Fatalf("variables = %#v", variables)
		}
		_, _ = io.WriteString(response, `{"id":321}`)
	}))
	defer server.Close()
	client, err := NewHTTPQueueClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := client.QueueProductionDemo(context.Background(), QueueRequest{
		SourceVersion:   testCommit,
		SessionID:       "session-1",
		ExecutionDigest: testDigest,
		VersionSet:      `["1.26.5-2"]`,
		SourceBuildID:   "3019035",
	})
	if err != nil {
		t.Fatal(err)
	}
	if buildID != 321 {
		t.Fatalf("build ID = %d", buildID)
	}
}

func TestHTTPQueueClientRedactsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied test-token")
	}))
	defer server.Close()
	client, err := NewHTTPQueueClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.QueueProductionDemo(context.Background(), QueueRequest{
		SourceVersion:   testCommit,
		SessionID:       "session-1",
		ExecutionDigest: testDigest,
		VersionSet:      `["1.26.5-2"]`,
		SourceBuildID:   "3019035",
	})
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPQueueClientRejectsInvalidRequestBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client, err := NewHTTPQueueClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueueProductionDemo(context.Background(), QueueRequest{}); err == nil {
		t.Fatal("invalid request was accepted")
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
}
