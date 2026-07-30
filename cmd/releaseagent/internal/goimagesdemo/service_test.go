// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesdemo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const (
	testCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type fakePipelineReader struct {
	recent          []*azdopipeline.Build
	recentResponses [][]*azdopipeline.Build
	builds          []*azdopipeline.Build
	listErr         error
	getErr          error
	listCalls       int
	getCalls        int
}

func (r *fakePipelineReader) ListRecent(context.Context, int) ([]*azdopipeline.Build, error) {
	if len(r.recentResponses) != 0 {
		index := r.listCalls
		r.listCalls++
		if index >= len(r.recentResponses) {
			index = len(r.recentResponses) - 1
		}
		return r.recentResponses[index], r.listErr
	}
	r.listCalls++
	return r.recent, r.listErr
}

func (r *fakePipelineReader) Get(context.Context, int) (*azdopipeline.Build, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	index := r.getCalls
	r.getCalls++
	if index >= len(r.builds) {
		index = len(r.builds) - 1
	}
	return r.builds[index], nil
}

type fakeQueueClient struct {
	request *QueueRequest
	buildID int
	err     error
}

func (q *fakeQueueClient) QueueProductionDemo(_ context.Context, request QueueRequest) (int, error) {
	q.request = &request
	return q.buildID, q.err
}

func TestTriggerQueuesFixedProductionDemo(t *testing.T) {
	reader := &fakePipelineReader{}
	queue := &fakeQueueClient{buildID: 321}
	service := newTestService(t, reader, queue, nil)
	buildID, err := service.TriggerBuildPipeline(
		context.Background(), DefinitionID, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "321" || queue.request == nil {
		t.Fatalf("build ID = %q, request = %#v", buildID, queue.request)
	}
	if queue.request.SourceVersion != testCommit || queue.request.ExecutionDigest != testDigest ||
		queue.request.SourceBuildID != "3019035" || queue.request.VersionSet != `["1.26.5-2"]` {

		t.Fatalf("queue request = %#v", queue.request)
	}
}

func TestTriggerRejectsUnsafeTargetOrParameters(t *testing.T) {
	service := newTestService(t, &fakePipelineReader{}, &fakeQueueClient{}, nil)
	if _, err := service.TriggerBuildPipeline(
		context.Background(), 1492, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	); err == nil {
		t.Fatal("nonproduction pipeline was accepted")
	}
	unsafe := releasesteps.GoImagesProductionDemoPipelineParameters()
	unsafe["publishRepoPrefix"] = "dev/"
	if _, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, unsafe, nil, nil); err == nil {
		t.Fatal("nonproduction publish prefix was accepted")
	}
}

func TestTriggerReconcilesExistingRun(t *testing.T) {
	parameters := make(map[string]any)
	for name, value := range releasesteps.GoImagesProductionDemoPipelineParameters() {
		parameters[name] = value
	}
	reader := &fakePipelineReader{recent: []*azdopipeline.Build{{
		ID: 321, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: map[string]string{
			correlationVariable:     "session-1",
			executionDigestVariable: testDigest,
			versionsVariable:        `["1.26.5-2"]`,
			sourceBuildVariable:     "3019035",
		},
		TemplateParameters: parameters,
	}}}
	queue := &fakeQueueClient{buildID: 999}
	service := newTestService(t, reader, queue, nil)
	buildID, err := service.TriggerBuildPipeline(
		context.Background(), DefinitionID, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "321" || queue.request != nil {
		t.Fatalf("build ID = %q, queue request = %#v", buildID, queue.request)
	}
}

func TestTriggerWaitsForEventuallyVisiblePriorRun(t *testing.T) {
	parameters := make(map[string]any)
	for name, value := range releasesteps.GoImagesProductionDemoPipelineParameters() {
		parameters[name] = value
	}
	correlated := &azdopipeline.Build{
		ID: 321, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: map[string]string{
			correlationVariable:     "session-1",
			executionDigestVariable: testDigest,
			versionsVariable:        `["1.26.5-2"]`,
			sourceBuildVariable:     "3019035",
		},
		TemplateParameters: parameters,
	}
	reader := &fakePipelineReader{recentResponses: [][]*azdopipeline.Build{nil, {correlated}}}
	queue := &fakeQueueClient{buildID: 999}
	sleeps := 0
	service, err := New(reader, queue, Config{
		SessionID:            "session-1",
		ExecutionDigest:      testDigest,
		Versions:             []string{"1.26.5-2"},
		SourceBuildID:        "3019035",
		SourceVersion:        testCommit,
		PreviousQueueAttempt: true,
		ReconcileAttempts:    3,
		ReconcileInterval:    time.Millisecond,
	}, func(context.Context, time.Duration) error { sleeps++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := service.TriggerBuildPipeline(
		context.Background(), DefinitionID, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "321" || reader.listCalls != 2 || sleeps != 1 || queue.request != nil {
		t.Fatalf("build ID = %q, list calls = %d, sleeps = %d, queue = %#v", buildID, reader.listCalls, sleeps, queue.request)
	}
}

func TestTriggerRejectsConflictingCorrelatedRun(t *testing.T) {
	parameters := make(map[string]any)
	for name, value := range releasesteps.GoImagesProductionDemoPipelineParameters() {
		parameters[name] = value
	}
	reader := &fakePipelineReader{recent: []*azdopipeline.Build{{
		ID: 321, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: map[string]string{
			correlationVariable:     "session-1",
			executionDigestVariable: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			versionsVariable:        `["1.26.5-2"]`,
			sourceBuildVariable:     "3019035",
		},
		TemplateParameters: parameters,
	}}}
	service := newTestService(t, reader, &fakeQueueClient{}, nil)
	if _, err := service.TriggerBuildPipeline(
		context.Background(), DefinitionID, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	); err == nil {
		t.Fatal("conflicting correlated run was accepted")
	}
}

func TestTriggerRejectsEventuallyVisibleConflictingRun(t *testing.T) {
	parameters := make(map[string]any)
	for name, value := range releasesteps.GoImagesProductionDemoPipelineParameters() {
		parameters[name] = value
	}
	conflicting := &azdopipeline.Build{
		ID: 321, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: map[string]string{
			correlationVariable:     "session-1",
			executionDigestVariable: strings.Repeat("b", 64),
			versionsVariable:        `["1.26.5-2"]`,
			sourceBuildVariable:     "3019035",
		},
		TemplateParameters: parameters,
	}
	reader := &fakePipelineReader{recentResponses: [][]*azdopipeline.Build{nil, {conflicting}}}
	queue := &fakeQueueClient{buildID: 999}
	service, err := New(reader, queue, Config{
		SessionID:            "session-1",
		ExecutionDigest:      testDigest,
		Versions:             []string{"1.26.5-2"},
		SourceBuildID:        "3019035",
		SourceVersion:        testCommit,
		PreviousQueueAttempt: true,
		ReconcileAttempts:    3,
		ReconcileInterval:    time.Millisecond,
	}, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TriggerBuildPipeline(
		context.Background(), DefinitionID, releasesteps.GoImagesProductionDemoPipelineParameters(), nil, nil,
	); err == nil || !strings.Contains(err.Error(), executionDigestVariable) {
		t.Fatalf("conflicting delayed run error = %v", err)
	}
	if queue.request != nil {
		t.Fatalf("queued after delayed conflict: %#v", queue.request)
	}
}

func TestPollPipelineComplete(t *testing.T) {
	reader := &fakePipelineReader{builds: []*azdopipeline.Build{
		{ID: 321, DefinitionID: DefinitionID, Status: "notStarted"},
		{ID: 321, DefinitionID: DefinitionID, Status: "inProgress"},
		{ID: 321, DefinitionID: DefinitionID, Status: "completed", Result: "succeeded"},
	}}
	sleeps := 0
	service := newTestService(t, reader, &fakeQueueClient{}, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err := service.PollPipelineComplete(context.Background(), "321", nil); err != nil {
		t.Fatal(err)
	}
	if reader.getCalls != 3 || sleeps != 2 {
		t.Fatalf("get calls = %d, sleeps = %d", reader.getCalls, sleeps)
	}
}

func TestPollPipelineCompleteFailureAndCancellation(t *testing.T) {
	reader := &fakePipelineReader{builds: []*azdopipeline.Build{{
		ID: 321, DefinitionID: DefinitionID, Status: "completed", Result: "failed", WebURL: "https://example/build/321",
	}}}
	err := newTestService(t, reader, &fakeQueueClient{}, nil).PollPipelineComplete(context.Background(), "321", nil)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("error = %v", err)
	}

	reader = &fakePipelineReader{builds: []*azdopipeline.Build{{ID: 321, DefinitionID: DefinitionID, Status: "inProgress"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := newTestService(t, reader, &fakeQueueClient{}, func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	})
	if err := service.PollPipelineComplete(ctx, "321", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestParametersAreExactlyDev(t *testing.T) {
	want := map[string]string{
		"_info":                    "🔵  go-docker-rolling-internal-pipeline.yml  🔵 🔵",
		"sourceBuildPipelineRunId": "$(Build.BuildId)",
		"publishRepoPrefix":        "public/",
	}
	if got := releasesteps.GoImagesProductionDemoPipelineParameters(); !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters = %#v, want %#v", got, want)
	}
}

func newTestService(t *testing.T, reader PipelineReader, queue QueueClient, sleeper Sleeper) *Service {
	t.Helper()
	service, err := New(reader, queue, Config{
		SessionID:       "session-1",
		ExecutionDigest: testDigest,
		Versions:        []string{"1.26.5-2"},
		SourceBuildID:   "3019035",
		SourceVersion:   testCommit,
		PollInterval:    time.Millisecond,
	}, sleeper)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
