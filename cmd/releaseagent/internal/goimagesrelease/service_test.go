// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesrelease

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

type fakePipelineClient struct {
	queueRequest *azdopipeline.QueueRequest
	queueBuild   *azdopipeline.Build
	queueErr     error
	existing     *azdopipeline.Build
	findErr      error
	getBuilds    []*azdopipeline.Build
	recentBuilds []*azdopipeline.Build
	getErr       error
	getCalls     int
}

func (c *fakePipelineClient) Queue(_ context.Context, request azdopipeline.QueueRequest) (*azdopipeline.Build, error) {
	c.queueRequest = &request
	return c.queueBuild, c.queueErr
}

func (c *fakePipelineClient) FindLatestByVariable(_ context.Context, _ int, _, _ string) (*azdopipeline.Build, error) {
	return c.existing, c.findErr
}

func (c *fakePipelineClient) Get(_ context.Context, _ int) (*azdopipeline.Build, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	index := c.getCalls
	c.getCalls++
	if index >= len(c.getBuilds) {
		index = len(c.getBuilds) - 1
	}
	return c.getBuilds[index], nil
}

func (c *fakePipelineClient) ListRecent(_ context.Context, _ int) ([]*azdopipeline.Build, error) {
	return c.recentBuilds, nil
}

func TestTriggerReconcilesExistingRun(t *testing.T) {
	client := &fakePipelineClient{existing: &azdopipeline.Build{ID: 123}}
	service := newTestService(t, client)
	buildID, err := service.TriggerBuildPipeline(context.Background(), 1151, map[string]string{"a": "b"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "123" {
		t.Fatalf("build ID = %q, want 123", buildID)
	}
	if client.queueRequest != nil {
		t.Fatal("reconciled run was queued again")
	}
}

func TestTriggerRejectsConflictingCorrelatedRun(t *testing.T) {
	client := &fakePipelineClient{existing: &azdopipeline.Build{
		ID: 123,
		Parameters: map[string]string{
			VersionsVariable: `["1.25.6-1"]`,
		},
	}}
	service := newTestService(t, client)
	if _, err := service.TriggerBuildPipeline(context.Background(), 1151, nil, nil, nil); err == nil {
		t.Fatal("conflicting correlated run was accepted")
	}
	if client.queueRequest != nil {
		t.Fatal("a new run was queued after reconciliation conflict")
	}
}

func TestTriggerQueuesCorrelatedRun(t *testing.T) {
	client := &fakePipelineClient{queueBuild: &azdopipeline.Build{ID: 321}}
	service := newTestService(t, client)
	parameters := map[string]string{"runGoImagesBuild": "true"}
	buildID, err := service.TriggerBuildPipeline(context.Background(), 1151, parameters, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "321" {
		t.Fatalf("build ID = %q, want 321", buildID)
	}
	if client.queueRequest == nil {
		t.Fatal("no queue request recorded")
	}
	if !reflect.DeepEqual(client.queueRequest.Parameters, parameters) {
		t.Fatalf("parameters = %#v, want %#v", client.queueRequest.Parameters, parameters)
	}
	if client.queueRequest.Variables[CorrelationVariable] != "session-1" {
		t.Fatalf("variables = %#v", client.queueRequest.Variables)
	}
	if client.queueRequest.Variables[WorkflowVariable] != WorkflowID ||
		client.queueRequest.Variables[VersionsVariable] != `["1.26.1-1"]` ||
		client.queueRequest.Variables[ExecutionDigestVariable] != "digest-1" {

		t.Fatalf("discovery variables = %#v", client.queueRequest.Variables)
	}
}

func TestTriggerRejectsUnexpectedPipeline(t *testing.T) {
	service := newTestService(t, &fakePipelineClient{})
	if _, err := service.TriggerBuildPipeline(context.Background(), 999, nil, nil, nil); err == nil {
		t.Fatal("unexpected pipeline was accepted")
	}
}

func TestPollPipelineComplete(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{
		{ID: 321, Status: "notStarted"},
		{ID: 321, Status: "inProgress"},
		{ID: 321, Status: "completed", Result: "succeeded"},
	}}
	sleeps := 0
	service, err := New(client, Config{
		DefinitionID:    1151,
		SessionID:       "session-1",
		Versions:        []string{"1.26.1-1"},
		ExecutionDigest: "digest-1",
		PollInterval:    time.Millisecond,
	}, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PollPipelineComplete(context.Background(), "321", nil); err != nil {
		t.Fatal(err)
	}
	if client.getCalls != 3 || sleeps != 2 {
		t.Fatalf("get calls = %d, sleeps = %d; want 3 and 2", client.getCalls, sleeps)
	}
}

func TestPollPipelineFailure(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{
		ID:     321,
		Status: "completed",
		Result: "failed",
		WebURL: "https://example/build/321",
	}}}
	service := newTestService(t, client)
	err := service.PollPipelineComplete(context.Background(), "321", nil)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "https://example/build/321") {
		t.Fatalf("error = %v, want failed build and URL", err)
	}
}

func TestPollHonorsCancellation(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{ID: 321, Status: "inProgress"}}}
	service, err := New(client, Config{
		DefinitionID:    1151,
		SessionID:       "session-1",
		Versions:        []string{"1.26.1-1"},
		ExecutionDigest: "digest-1",
		PollInterval:    time.Hour,
	}, func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.PollPipelineComplete(ctx, "321", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestCanonicalVersionSet(t *testing.T) {
	got, err := CanonicalVersionSet([]string{"1.26.1-1", "1.25.6-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != `["1.25.6-1","1.26.1-1"]` {
		t.Fatalf("version set = %q", got)
	}
}

func TestFindCandidatesByVersionSet(t *testing.T) {
	queueTime := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	client := &fakePipelineClient{recentBuilds: []*azdopipeline.Build{
		{
			ID:           10,
			DefinitionID: 1151,
			Status:       "inProgress",
			QueueTime:    queueTime,
			TemplateParameters: map[string]any{
				"releaseVersions":        []any{"1.26.1-1", "1.25.6-1"},
				"runGoImagesBuild":       false,
				"runPublishAnnouncement": false,
			},
			Parameters: map[string]string{
				WorkflowVariable:        WorkflowID,
				VersionsVariable:        `["1.25.6-1","1.26.1-1"]`,
				CorrelationVariable:     "session-old",
				ExecutionDigestVariable: "digest-old",
			},
		},
		{
			ID:                 9,
			DefinitionID:       1151,
			Status:             "completed",
			Result:             "succeeded",
			Parameters:         map[string]string{},
			TemplateParameters: map[string]any{"releaseVersions": []any{"1.26.1-1", "1.25.6-1"}},
		},
		{
			ID:                 8,
			DefinitionID:       1151,
			Status:             "completed",
			Result:             "failed",
			Parameters:         map[string]string{},
			TemplateParameters: map[string]any{"releaseVersions": []any{"1.24.1-1"}},
		},
	}}
	service, err := New(client, Config{
		DefinitionID:    1151,
		SessionID:       "discovery-only",
		Versions:        []string{"1.26.1-1", "1.25.6-1"},
		ExecutionDigest: "discovery-only",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := service.FindCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	if !candidates[0].CreatedByUI || candidates[0].SessionID != "session-old" || candidates[0].QueueTime != queueTime {
		t.Fatalf("UI candidate = %#v", candidates[0])
	}
	if candidates[0].Parameters["runGoImagesBuild"] != "false" || candidates[0].Parameters["runPublishAnnouncement"] != "false" {
		t.Fatalf("candidate parameters = %#v", candidates[0].Parameters)
	}
	if candidates[1].CreatedByUI || candidates[1].BuildID != 9 {
		t.Fatalf("legacy candidate = %#v", candidates[1])
	}
}

func TestValidateCandidate(t *testing.T) {
	client := &fakePipelineClient{getBuilds: []*azdopipeline.Build{{
		ID:                 10,
		DefinitionID:       1151,
		Status:             "inProgress",
		Parameters:         map[string]string{},
		TemplateParameters: map[string]any{"releaseVersions": []any{"1.26.1-1"}},
	}}}
	service := newTestService(t, client)
	candidate, err := service.ValidateCandidate(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BuildID != 10 || candidate.State != azdopipeline.RunStateRunning {
		t.Fatalf("candidate = %#v", candidate)
	}
}

type integrationToken struct{}

func (integrationToken) Token(context.Context) (string, error) { return "test-token", nil }

func TestFocusedGraphWithFakeAzureDevOps(t *testing.T) {
	var queuedParameters map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("definitions") == "1151":
			_, _ = response.Write([]byte(`{"value":[]}`))
		case request.Method == http.MethodPost && request.URL.Query().Get("definitionId") == "1151":
			var body struct {
				TemplateParameters map[string]string `json:"templateParameters"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			queuedParameters = body.TemplateParameters
			_, _ = response.Write([]byte(`{"id":321,"status":"notStarted","parameters":"{\"ReleaseUISessionID\":\"session-1\"}"}`))
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/321"):
			_, _ = response.Write([]byte(`{"id":321,"status":"completed","result":"succeeded","_links":{"web":{"href":"https://example/build/321"}}}`))
		default:
			http.Error(response, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client, err := azdopipeline.NewClient(server.URL, "internal", server.Client(), integrationToken{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(client, Config{
		DefinitionID:    1151,
		SessionID:       "session-1",
		Versions:        []string{"1.26.1-1"},
		ExecutionDigest: "digest-1",
		PollInterval:    time.Millisecond,
	}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	input := &releasesteps.Input{
		Versions:                         []string{"1.26.1-1"},
		RunnerGitHubUser:                 "ghost",
		ReleaseConfigVariableGroup:       "test-release-config",
		MicrosoftGoImagesReleasePipeline: 1151,
	}
	checkpointCount := 0
	steps, state, err := releasesteps.CreateGoImagesReleasePipelineGraphWithCheckpoint(
		input,
		&releasesteps.Secret{},
		nil,
		service,
		func(context.Context, *releasesteps.State) error {
			checkpointCount++
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var runner coordinator.StepRunner
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if queuedParameters["runGoImagesBuild"] != "true" || queuedParameters["runPublishAnnouncement"] != "false" {
		t.Fatalf("queued parameters = %#v", queuedParameters)
	}
	if state.Day.GoImagesReleaseBuildID != "321" || !state.Day.GoImagesReleaseComplete {
		t.Fatalf("final state = %#v", state.Day)
	}
	if checkpointCount != 2 {
		t.Fatalf("checkpoint count = %d, want 2", checkpointCount)
	}
}

func newTestService(t *testing.T, client PipelineClient) *Service {
	t.Helper()
	service, err := New(client, Config{
		DefinitionID:    1151,
		SessionID:       "session-1",
		Versions:        []string{"1.26.1-1"},
		ExecutionDigest: "digest-1",
		PollInterval:    time.Millisecond,
	}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return service
}
