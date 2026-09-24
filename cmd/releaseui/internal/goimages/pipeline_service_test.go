// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
)

const serviceTestCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

type fakeReader struct {
	builds      []*azdopipeline.Build
	recent      [][]*azdopipeline.Build
	failures    []azdopipeline.BuildFailure
	failureErr  error
	gets        int
	lists       int
	failureGets int
}

func (r *fakeReader) Get(context.Context, int) (*azdopipeline.Build, error) {
	index := r.gets
	r.gets++
	if index >= len(r.builds) {
		index = len(r.builds) - 1
	}
	return r.builds[index], nil
}

func (r *fakeReader) ListRecent(context.Context, int) ([]*azdopipeline.Build, error) {
	index := r.lists
	r.lists++
	if len(r.recent) == 0 {
		return nil, nil
	}
	if index >= len(r.recent) {
		index = len(r.recent) - 1
	}
	return r.recent[index], nil
}

func (r *fakeReader) GetFailures(context.Context, int) ([]azdopipeline.BuildFailure, error) {
	r.failureGets++
	return r.failures, r.failureErr
}

type fakeQueueClient struct {
	request QueueRequest
	calls   int
}

func (q *fakeQueueClient) QueueRelease(_ context.Context, request QueueRequest) (int, error) {
	q.calls++
	q.request = request
	return 888, nil
}

func TestTriggerQueuesEachAllowlistedMode(t *testing.T) {
	for _, test := range []struct {
		mode          Mode
		sourceBuildID string
		wantSource    string
		wantPrefix    string
	}{
		{mode: ModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{mode: ModeRollback, sourceBuildID: "3019035", wantSource: "3019035", wantPrefix: "public/"},
		{mode: ModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			queue := &fakeQueueClient{}
			service := newTestService(t, &fakeReader{}, queue, PipelineRunConfig{
				Mode: test.mode, SourceBuildID: test.sourceBuildID,
			})
			parameters, err := PipelineParameters(test.mode, test.sourceBuildID)
			if err != nil {
				t.Fatal(err)
			}
			buildID, err := service.QueuePipeline(context.Background(), parameters)
			if err != nil {
				t.Fatal(err)
			}
			if buildID != "888" || queue.calls != 1 || queue.request.Mode != test.mode || queue.request.SourceBuildID != test.sourceBuildID {
				t.Fatalf("build = %q, queue = %#v", buildID, queue)
			}
			if parameters["sourceBuildPipelineRunId"] != test.wantSource || parameters["publishRepoPrefix"] != test.wantPrefix {
				t.Fatalf("parameters = %#v", parameters)
			}
		})
	}
}

func TestPollMirrorWaitsForPlannedCommit(t *testing.T) {
	checks := 0
	sleeps := 0
	config := completeConfig(PipelineRunConfig{
		Mode: ModeNormal,
		VerifyMirrorCommit: func(_ context.Context, commit string) error {
			checks++
			if commit != serviceTestCommit {
				t.Fatalf("commit = %q, want %q", commit, serviceTestCommit)
			}
			if checks < 3 {
				return errors.New("not mirrored yet")
			}
			return nil
		},
	})
	service, err := NewPipelineRunService(&fakeReader{}, &fakeQueueClient{}, config, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PollMirror(context.Background(), serviceTestCommit); err != nil {
		t.Fatal(err)
	}
	if checks != 3 || sleeps != 2 {
		t.Fatalf("checks = %d, sleeps = %d; want 3 and 2", checks, sleeps)
	}
}

func TestPollMirrorRejectsUnplannedCommit(t *testing.T) {
	service := newTestService(t, &fakeReader{}, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	if err := service.PollMirror(context.Background(), "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"); err == nil {
		t.Fatal("service accepted a different mirror commit")
	}
}

func TestTriggerRejectsMutatedExecution(t *testing.T) {
	service := newTestService(t, &fakeReader{}, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	parameters, _ := PipelineParameters(ModeNormal, "")
	parameters["publishRepoPrefix"] = "dev/"
	if _, err := service.QueuePipeline(context.Background(), parameters); err == nil {
		t.Fatal("normal service accepted test parameters")
	}
}

func TestTriggerReconcilesExistingRelease(t *testing.T) {
	parameters, _ := PipelineParameters(ModeRollback, "3019035")
	variables := map[string]string{
		correlationVariable: "session", executionDigestVariable: testDigest,
		modeVariable: "rollback", versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "3019035",
	}
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	reader := &fakeReader{recent: [][]*azdopipeline.Build{{{
		ID: 777, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: serviceTestCommit,
		Parameters: variables, TemplateParameters: template,
	}}}}
	queue := &fakeQueueClient{}
	service := newTestService(t, reader, queue, PipelineRunConfig{
		Mode: ModeRollback, SourceBuildID: "3019035", PreviousQueueAttempt: true,
	})
	buildID, err := service.QueuePipeline(context.Background(), parameters)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "777" || queue.calls != 0 {
		t.Fatalf("build = %q, queue calls = %d", buildID, queue.calls)
	}
}

func TestTriggerRetriesReconciliationAfterCheckpointedAttempt(t *testing.T) {
	parameters, _ := PipelineParameters(ModeTest, "")
	variables := map[string]string{
		correlationVariable: "session", executionDigestVariable: testDigest,
		modeVariable: "test", versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "",
	}
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	reader := &fakeReader{recent: [][]*azdopipeline.Build{
		nil,
		{{
			ID: 778, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: serviceTestCommit,
			Parameters: variables, TemplateParameters: template,
		}},
	}}
	queue := &fakeQueueClient{}
	sleeps := 0
	service, err := NewPipelineRunService(reader, queue, completeConfig(PipelineRunConfig{
		Mode: ModeTest, PreviousQueueAttempt: true, ReconcileAttempts: 3,
	}), func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := service.QueuePipeline(context.Background(), parameters)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "778" || queue.calls != 0 || sleeps != 1 {
		t.Fatalf("build = %q, queue calls = %d, sleeps = %d", buildID, queue.calls, sleeps)
	}
}

func TestReconciliationRejectsConflictingCorrelation(t *testing.T) {
	parameters, _ := PipelineParameters(ModeNormal, "")
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	reader := &fakeReader{recent: [][]*azdopipeline.Build{{{
		ID: 777, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: serviceTestCommit,
		Parameters: map[string]string{
			correlationVariable: "session", executionDigestVariable: testDigest,
			modeVariable: "test", versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "",
		},
		TemplateParameters: template,
	}}}}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{
		Mode: ModeNormal, PreviousQueueAttempt: true,
	})
	_, err := service.QueuePipeline(context.Background(), parameters)
	if err == nil || !strings.Contains(err.Error(), modeVariable) {
		t.Fatalf("error = %v", err)
	}
}

func TestPollPipeline(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{
		{ID: 888, DefinitionID: DefinitionID, Status: "inProgress"},
		{ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "succeeded"},
	}}
	sleeps := 0
	service, err := NewPipelineRunService(reader, &fakeQueueClient{}, completeConfig(PipelineRunConfig{
		Mode: ModeNormal,
	}), func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PollPipeline(context.Background(), "888"); err != nil {
		t.Fatal(err)
	}
	if reader.gets != 2 || reader.failureGets != 0 || sleeps != 1 {
		t.Fatalf("gets = %d, failure gets = %d, sleeps = %d", reader.gets, reader.failureGets, sleeps)
	}
}

func TestPollPipelineReportsFailure(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{
		ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "failed", WebURL: "https://example/build/888",
	}}, failures: []azdopipeline.BuildFailure{{
		Path: "Build > Linux arm32 > Build Images", Message: "PowerShell exited with code 1",
	}}}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	err := service.PollPipeline(context.Background(), strconv.Itoa(888))
	var resultError *PipelineResultError
	if err == nil || !strings.Contains(err.Error(), "Build > Linux arm32 > Build Images: PowerShell exited with code 1") ||
		!strings.Contains(err.Error(), "https://example/build/888") || reader.failureGets != 1 ||
		!errors.As(err, &resultError) || resultError.Result != "failed" {

		t.Fatalf("error = %v", err)
	}
}

func TestPollPipelineReportsCancellation(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{
		ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "canceled", WebURL: "https://example/build/888",
	}}}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeTest})
	err := service.PollPipeline(context.Background(), "888")
	var resultError *PipelineResultError
	if err == nil || !errors.As(err, &resultError) || resultError.Result != "canceled" ||
		!strings.Contains(err.Error(), "go-images release build 888 canceled") || reader.failureGets != 0 {

		t.Fatalf("error = %v, result error = %#v", err, resultError)
	}
}

func TestPollPipelineFailureDetailsAreOptional(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{
		ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "failed", WebURL: "https://example/build/888",
	}}, failureErr: errors.New("timeline unavailable")}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	err := service.PollPipeline(context.Background(), "888")
	if err == nil || !strings.Contains(err.Error(), `failed with result "failed"`) ||
		!strings.Contains(err.Error(), "https://example/build/888") {

		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsInvalidRollbackBuild(t *testing.T) {
	_, err := NewPipelineRunService(&fakeReader{}, &fakeQueueClient{}, completeConfig(PipelineRunConfig{
		Mode: ModeRollback, SourceBuildID: "bad",
	}), nil)
	if err == nil {
		t.Fatal("invalid rollback build was accepted")
	}
}

func TestPollHonorsCancellation(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{ID: 888, DefinitionID: DefinitionID, Status: "inProgress"}}}
	service, err := NewPipelineRunService(reader, &fakeQueueClient{}, completeConfig(PipelineRunConfig{
		Mode: ModeNormal,
	}), func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.PollPipeline(ctx, "888"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func newTestService(t *testing.T, reader PipelineReader, queue QueueClient, config PipelineRunConfig) *PipelineRunService {
	t.Helper()
	service, err := NewPipelineRunService(reader, queue, completeConfig(config), func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func completeConfig(config PipelineRunConfig) PipelineRunConfig {
	config.SessionID = "session"
	config.ExecutionDigest = testDigest
	config.Versions = []string{"1.26.5-2"}
	config.SourceVersion = serviceTestCommit
	if config.VerifyMirrorCommit == nil {
		config.VerifyMirrorCommit = func(context.Context, string) error { return nil }
	}
	config.PollInterval = time.Millisecond
	config.ReconcileInterval = time.Millisecond
	return config
}
