// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

func TestListRecent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Query().Get("definitions") != "1023" || request.URL.Query().Get("$top") != "50" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(`{"value":[` +
			`{"id":11,"status":"notStarted","queueTime":"2026-07-28T12:00:00Z",` +
			`"definition":{"id":1023},"sourceBranch":"refs/heads/microsoft/main",` +
			`"sourceVersion":"81ce9afc2b75ec4e153dd15fc3c7539b12024945",` +
			`"templateParameters":{"publishRepoPrefix":"public/"}}` +
			`]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	builds, err := client.ListRecent(context.Background(), 1023)
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0].ID != 11 {
		t.Fatalf("builds = %#v, want ID 11", builds)
	}
	build := builds[0]
	if build.DefinitionID != 1023 || build.TemplateParameters["publishRepoPrefix"] != "public/" || build.QueueTime.IsZero() ||
		build.SourceBranch != "refs/heads/microsoft/main" || build.SourceVersion == "" {

		t.Fatalf("build metadata = %#v", build)
	}
}

func TestGetDefinition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/_apis/build/definitions/1023" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		_, _ = response.Write([]byte(`{
			"id":1023,
			"name":"microsoft-go-images (official)",
			"queueStatus":"enabled",
			"process":{"yamlFilename":"eng/pipeline/go-docker-rolling-internal-pipeline.yml"},
			"repository":{"defaultBranch":"refs/heads/microsoft/main","name":"microsoft-go-images"}
		}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := client.GetDefinition(context.Background(), 1023)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != 1023 || definition.QueueStatus != "enabled" ||
		definition.DefaultBranch != "refs/heads/microsoft/main" || definition.Repository != "microsoft-go-images" ||
		definition.YAMLPath != "eng/pipeline/go-docker-rolling-internal-pipeline.yml" {

		t.Fatalf("definition = %#v", definition)
	}
}

func TestGetTimeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/internal/_apis/build/builds/888/timeline" ||
			request.URL.Query().Get("api-version") != "7.1" {

			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		_, _ = response.Write([]byte(`{"records":[
			{"id":"stage","type":"Stage","name":"Build","state":"inProgress","order":1},
			{"id":"job","parentId":"stage","type":"Job","name":"linux-amd64","state":"inProgress","order":2},
			{"id":"task","parentId":"job","type":"Task","name":"Build image","state":"inProgress","order":3}
		]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := client.GetTimeline(context.Background(), 888)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Records) != 3 || timeline.Records[2].Name != "Build image" ||
		timeline.Records[2].ParentID != "job" || timeline.Records[0].Type != "Stage" {

		t.Fatalf("timeline = %#v", timeline)
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

func TestGetBadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"message":"invalid build"}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "internal", server.Client(), staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), 123)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %v, want HTTP 400", err)
	}
	if !strings.Contains(httpErr.Body, "invalid build") {
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
