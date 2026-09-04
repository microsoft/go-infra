// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesexecution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

const (
	testCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	testDigest = "3edb53bc1411c2ec36149fd7015cb2efec1f8fb9428a17366d6a4d71c3c5c954"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

func TestQueueReleaseUsesModeDerivedPayload(t *testing.T) {
	for _, test := range []struct {
		mode          goimagesworkflow.Mode
		sourceBuildID string
		wantSource    string
		wantPrefix    string
	}{
		{mode: goimagesworkflow.ModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{mode: goimagesworkflow.ModeRollback, sourceBuildID: "3019035", wantSource: "3019035", wantPrefix: "public/"},
		{mode: goimagesworkflow.ModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/internal/_apis/build/builds" {
					t.Fatalf("request = %s %s", request.Method, request.URL)
				}
				if request.URL.Query().Get("definitionId") != "1023" || request.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatalf("query/header = %v / %q", request.URL.Query(), request.Header.Get("Authorization"))
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
				if body.Definition.ID != goimagesworkflow.DefinitionID || body.SourceBranch != goimagesworkflow.SourceBranch || body.SourceVersion != testCommit {
					t.Fatalf("identity = %#v", body)
				}
				if body.TemplateParameters["sourceBuildPipelineRunId"] != test.wantSource ||
					body.TemplateParameters["publishRepoPrefix"] != test.wantPrefix {

					t.Fatalf("template parameters = %#v", body.TemplateParameters)
				}
				var variables map[string]string
				if err := json.Unmarshal([]byte(body.Parameters), &variables); err != nil {
					t.Fatal(err)
				}
				if variables[modeVariable] != string(test.mode) || variables[sourceBuildVariable] != test.sourceBuildID ||
					variables[executionDigestVariable] != testDigest {

					t.Fatalf("variables = %#v", variables)
				}
				_ = json.NewEncoder(response).Encode(map[string]int{"id": 888})
			}))
			defer server.Close()
			client, err := NewHTTPQueueClient(server.URL, "internal", server.Client(), staticToken("test-token"))
			if err != nil {
				t.Fatal(err)
			}
			buildID, err := client.QueueRelease(context.Background(), QueueRequest{
				Mode: test.mode, SourceVersion: testCommit, SourceBuildID: test.sourceBuildID,
				SessionID: "session", ExecutionDigest: testDigest, VersionSet: `["1.26.5-2"]`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if buildID != 888 {
				t.Fatalf("build ID = %d", buildID)
			}
		})
	}
}

func TestQueueReleaseRejectsInvalidModeInputs(t *testing.T) {
	client, err := NewHTTPQueueClient("https://example.invalid", "internal", http.DefaultClient, staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	base := QueueRequest{
		Mode: goimagesworkflow.ModeNormal, SourceVersion: testCommit,
		SessionID: "session", ExecutionDigest: testDigest, VersionSet: `["1.26.5-2"]`,
	}
	base.SourceBuildID = "123"
	if _, err := client.QueueRelease(context.Background(), base); err == nil {
		t.Fatal("normal release accepted a source build")
	}
	base.Mode = goimagesworkflow.ModeRollback
	base.SourceBuildID = "invalid"
	if _, err := client.QueueRelease(context.Background(), base); err == nil {
		t.Fatal("rollback accepted an invalid source build")
	}
}

func TestQueueReleaseRedactsToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied test-token")
	}))
	defer server.Close()
	client, err := NewHTTPQueueClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.QueueRelease(context.Background(), QueueRequest{
		Mode: goimagesworkflow.ModeTest, SourceVersion: testCommit,
		SessionID: "session", ExecutionDigest: testDigest, VersionSet: `["1.26.5-2"]`,
	})
	if err == nil || strings.Contains(err.Error(), "test-token") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error = %v", err)
	}
}
