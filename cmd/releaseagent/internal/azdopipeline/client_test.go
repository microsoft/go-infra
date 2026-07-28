// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

func TestQueueRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/internal/_apis/build/builds" {
			t.Fatalf("request = %s %s, want POST /internal/_apis/build/builds", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.URL.Query().Get("definitionId") != "1151" {
			t.Fatalf("definitionId = %q, want 1151", request.URL.Query().Get("definitionId"))
		}
		var body struct {
			Definition struct {
				ID int `json:"id"`
			} `json:"definition"`
			TemplateParameters map[string]string `json:"templateParameters"`
			Parameters         string            `json:"parameters"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Definition.ID != 1151 || body.TemplateParameters["runGoImagesBuild"] != "true" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		var variables map[string]string
		if err := json.Unmarshal([]byte(body.Parameters), &variables); err != nil {
			t.Fatal(err)
		}
		if variables["ReleaseUISessionID"] != "session-1" {
			t.Fatalf("variables = %#v", variables)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":321,"status":"notStarted","parameters":"{\"ReleaseUISessionID\":\"session-1\"}","_links":{"web":{"href":"https://example/build/321"}}}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.Queue(context.Background(), QueueRequest{
		DefinitionID: 1151,
		Parameters:   map[string]string{"runGoImagesBuild": "true"},
		Variables:    map[string]string{"ReleaseUISessionID": "session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if build.ID != 321 || build.WebURL != "https://example/build/321" {
		t.Fatalf("build = %#v", build)
	}
}

func TestFindLatestByVariable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("definitions") != "1151" || request.URL.Query().Get("$top") != "50" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(`{"value":[` +
			`{"id":12,"status":"inProgress","parameters":"{\"ReleaseUISessionID\":\"other\"}"},` +
			`{"id":11,"status":"notStarted","parameters":"{\"ReleaseUISessionID\":{\"value\":\"session-1\"}}"}` +
			`]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	build, err := client.FindLatestByVariable(context.Background(), 1151, "ReleaseUISessionID", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if build == nil || build.ID != 11 {
		t.Fatalf("build = %#v, want ID 11", build)
	}
}

func TestGetDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/_apis/build/definitions/1151" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{
			"id":1151,
			"name":"microsoft-go-infra-release-go-images (official)",
			"queueStatus":"enabled",
			"process":{"yamlFilename":"eng/pipelines/release-go-images-pipeline.yml"},
			"repository":{"defaultBranch":"refs/heads/main"}
		}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := client.GetDefinition(context.Background(), 1151)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != 1151 || definition.QueueStatus != "enabled" ||
		definition.DefaultBranch != "refs/heads/main" ||
		definition.YAMLPath != "eng/pipelines/release-go-images-pipeline.yml" {

		t.Fatalf("definition = %#v", definition)
	}
}

func TestRunState(t *testing.T) {
	for _, test := range []struct {
		status string
		result string
		want   RunState
	}{
		{status: "notStarted", want: RunStateWaiting},
		{status: "postponed", want: RunStateWaiting},
		{status: "inProgress", want: RunStateRunning},
		{status: "completed", result: "succeeded", want: RunStateSucceeded},
		{status: "completed", result: "partiallySucceeded", want: RunStateSucceeded},
		{status: "completed", result: "failed", want: RunStateFailed},
		{status: "completed", result: "canceled", want: RunStateCanceled},
	} {
		t.Run(test.status+"/"+test.result, func(t *testing.T) {
			got, err := (&Build{Status: test.status, Result: test.result}).State()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("State = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHTTPErrorDoesNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "denied", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("secret-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), 123)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want HTTP 401", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposed token: %v", err)
	}
}

func TestQueueBadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"message":"Unexpected parameter 'runSomething'"}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Queue(context.Background(), QueueRequest{DefinitionID: 1151})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %v, want HTTP 400", err)
	}
	if !strings.Contains(httpErr.Body, "Unexpected parameter") {
		t.Fatalf("error body = %q", httpErr.Body)
	}
}

func TestMalformedSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"id":`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), 123); err == nil || !strings.Contains(err.Error(), "decode Azure DevOps response") {
		t.Fatalf("error = %v, want malformed response error", err)
	}
}
