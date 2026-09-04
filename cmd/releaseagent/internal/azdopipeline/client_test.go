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

	azdobuild "github.com/microsoft/azure-devops-go-api/azuredevops/build"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

type definitionClientFunc func(context.Context, azdobuild.GetDefinitionArgs) (*azdobuild.BuildDefinition, error)

func (f definitionClientFunc) GetDefinition(ctx context.Context, args azdobuild.GetDefinitionArgs) (*azdobuild.BuildDefinition, error) {
	return f(ctx, args)
}

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
	if build.DefinitionID != 1023 || build.TemplateParameters["publishRepoPrefix"] != "public/" ||
		build.SourceBranch != "refs/heads/microsoft/main" || build.SourceVersion == "" {

		t.Fatalf("build metadata = %#v", build)
	}
}

func TestGetDefinition(t *testing.T) {
	client, err := NewClient("https://example.invalid", "internal", http.DefaultClient, staticToken("test-token"))
	if err != nil {
		t.Fatal(err)
	}
	client.newDefinitionClient = func(context.Context) (definitionClient, string, error) {
		return definitionClientFunc(func(_ context.Context, args azdobuild.GetDefinitionArgs) (*azdobuild.BuildDefinition, error) {
			if args.Project == nil || *args.Project != "internal" || args.DefinitionId == nil || *args.DefinitionId != 1023 {
				t.Fatalf("definition args = %#v", args)
			}
			id := 1023
			name := "microsoft-go-images (official)"
			queueStatus := azdobuild.DefinitionQueueStatusValues.Enabled
			defaultBranch := "refs/heads/microsoft/main"
			repositoryName := "microsoft-go-images"
			return &azdobuild.BuildDefinition{
				Id: idPointer(id), Name: &name, QueueStatus: &queueStatus,
				Process:    map[string]any{"yamlFilename": "eng/pipeline/go-docker-rolling-internal-pipeline.yml"},
				Repository: &azdobuild.BuildRepository{DefaultBranch: &defaultBranch, Name: &repositoryName},
			}, nil
		}), "test-token", nil
	}
	definition, err := client.GetDefinition(context.Background(), 1023)
	if err != nil {
		t.Fatal(err)
	}
	if definition.QueueStatus != "enabled" ||
		definition.DefaultBranch != "refs/heads/microsoft/main" || definition.Repository != "microsoft-go-images" ||
		definition.YAMLPath != "eng/pipeline/go-docker-rolling-internal-pipeline.yml" {

		t.Fatalf("definition = %#v", definition)
	}
}

func idPointer(value int) *int { return &value }

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
