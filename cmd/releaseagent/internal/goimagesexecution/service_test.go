// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesexecution

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

type fakeReader struct {
	builds       []*azdopipeline.Build
	recent       [][]*azdopipeline.Build
	timelines    []*azdopipeline.Timeline
	gets         int
	lists        int
	timelineGets int
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

func (r *fakeReader) GetTimeline(context.Context, int) (*azdopipeline.Timeline, error) {
	index := r.timelineGets
	r.timelineGets++
	if len(r.timelines) == 0 {
		return &azdopipeline.Timeline{}, nil
	}
	if index >= len(r.timelines) {
		index = len(r.timelines) - 1
	}
	return r.timelines[index], nil
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
		mode          releasesteps.GoImagesReleaseMode
		sourceBuildID string
		wantSource    string
		wantPrefix    string
	}{
		{mode: releasesteps.GoImagesReleaseModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{mode: releasesteps.GoImagesReleaseModeRollback, sourceBuildID: "3019035", wantSource: "3019035", wantPrefix: "public/"},
		{mode: releasesteps.GoImagesReleaseModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			queue := &fakeQueueClient{}
			service := newTestService(t, &fakeReader{}, queue, Config{
				Mode: test.mode, SourceBuildID: test.sourceBuildID,
			})
			parameters, err := releasesteps.GoImagesPipelineParametersForMode(test.mode, test.sourceBuildID)
			if err != nil {
				t.Fatal(err)
			}
			buildID, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, parameters, nil, nil)
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

func TestPollAzDOMirrorWaitsForPlannedCommit(t *testing.T) {
	checks := 0
	sleeps := 0
	config := completeConfig(Config{
		Mode: releasesteps.GoImagesReleaseModeNormal,
		VerifyMirrorCommit: func(_ context.Context, commit string) error {
			checks++
			if commit != testCommit {
				t.Fatalf("commit = %q, want %q", commit, testCommit)
			}
			if checks < 3 {
				return errors.New("not mirrored yet")
			}
			return nil
		},
	})
	service, err := New(&fakeReader{}, &fakeQueueClient{}, config, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PollAzDOMirror(
		context.Background(), releasesteps.GoImagesInternalMirrorTarget, testCommit, nil,
	); err != nil {
		t.Fatal(err)
	}
	if checks != 3 || sleeps != 2 {
		t.Fatalf("checks = %d, sleeps = %d; want 3 and 2", checks, sleeps)
	}
}

func TestPollAzDOMirrorRejectsUnplannedSource(t *testing.T) {
	service := newTestService(t, &fakeReader{}, &fakeQueueClient{}, Config{Mode: releasesteps.GoImagesReleaseModeNormal})
	if err := service.PollAzDOMirror(context.Background(), "other/repo", testCommit, nil); err == nil {
		t.Fatal("service accepted a different mirror target")
	}
	if err := service.PollAzDOMirror(
		context.Background(),
		releasesteps.GoImagesInternalMirrorTarget,
		"2ef65db89e42942c24e3d8f0b8a8eb52bc86857a",
		nil,
	); err == nil {
		t.Fatal("service accepted a different mirror commit")
	}
}

func TestTriggerRejectsMutatedExecution(t *testing.T) {
	service := newTestService(t, &fakeReader{}, &fakeQueueClient{}, Config{Mode: releasesteps.GoImagesReleaseModeNormal})
	parameters, _ := releasesteps.GoImagesPipelineParametersForMode(releasesteps.GoImagesReleaseModeNormal, "")
	if _, err := service.TriggerBuildPipeline(context.Background(), 1492, parameters, nil, nil); err == nil {
		t.Fatal("service accepted the deprecated definition")
	}
	parameters["publishRepoPrefix"] = "dev/"
	if _, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, parameters, nil, nil); err == nil {
		t.Fatal("normal service accepted test parameters")
	}
}

func TestTriggerReconcilesExistingRelease(t *testing.T) {
	parameters, _ := releasesteps.GoImagesPipelineParametersForMode(releasesteps.GoImagesReleaseModeRollback, "3019035")
	variables := map[string]string{
		correlationVariable: "session", executionDigestVariable: testDigest,
		modeVariable: "rollback", versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "3019035",
	}
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	reader := &fakeReader{recent: [][]*azdopipeline.Build{{{
		ID: 777, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: variables, TemplateParameters: template,
	}}}}
	queue := &fakeQueueClient{}
	service := newTestService(t, reader, queue, Config{
		Mode: releasesteps.GoImagesReleaseModeRollback, SourceBuildID: "3019035", PreviousQueueAttempt: true,
	})
	buildID, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, parameters, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "777" || queue.calls != 0 {
		t.Fatalf("build = %q, queue calls = %d", buildID, queue.calls)
	}
}

func TestTriggerRetriesReconciliationAfterCheckpointedAttempt(t *testing.T) {
	parameters, _ := releasesteps.GoImagesPipelineParametersForMode(releasesteps.GoImagesReleaseModeTest, "")
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
			ID: 778, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
			Parameters: variables, TemplateParameters: template,
		}},
	}}
	queue := &fakeQueueClient{}
	sleeps := 0
	service, err := New(reader, queue, completeConfig(Config{
		Mode: releasesteps.GoImagesReleaseModeTest, PreviousQueueAttempt: true, ReconcileAttempts: 3,
	}), func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, parameters, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if buildID != "778" || queue.calls != 0 || sleeps != 1 {
		t.Fatalf("build = %q, queue calls = %d, sleeps = %d", buildID, queue.calls, sleeps)
	}
}

func TestReconciliationRejectsConflictingCorrelation(t *testing.T) {
	parameters, _ := releasesteps.GoImagesPipelineParametersForMode(releasesteps.GoImagesReleaseModeNormal, "")
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	reader := &fakeReader{recent: [][]*azdopipeline.Build{{{
		ID: 777, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: testCommit,
		Parameters: map[string]string{
			correlationVariable: "session", executionDigestVariable: testDigest,
			modeVariable: "test", versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "",
		},
		TemplateParameters: template,
	}}}}
	service := newTestService(t, reader, &fakeQueueClient{}, Config{
		Mode: releasesteps.GoImagesReleaseModeNormal, PreviousQueueAttempt: true,
	})
	_, err := service.TriggerBuildPipeline(context.Background(), DefinitionID, parameters, nil, nil)
	if err == nil || !strings.Contains(err.Error(), modeVariable) {
		t.Fatalf("error = %v", err)
	}
}

func TestPollPipelineComplete(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{
		{ID: 888, DefinitionID: DefinitionID, Status: "inProgress"},
		{ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "succeeded"},
	}, timelines: []*azdopipeline.Timeline{{Records: []azdopipeline.TimelineRecord{
		{ID: "stage", Type: "Stage", Name: "Build and test", State: "inProgress", Order: 1},
		{ID: "job", ParentID: "stage", Type: "Job", Name: "linux-amd64", State: "inProgress", Order: 2},
		{ID: "task", ParentID: "job", Type: "Task", Name: "Build image", State: "inProgress", Order: 3},
	}}}}
	sleeps := 0
	service, err := New(reader, &fakeQueueClient{}, completeConfig(Config{
		Mode: releasesteps.GoImagesReleaseModeNormal,
	}), func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	step := coordinator.NewRootStep("wait", "Wait", coordinator.NoTimeout, func(ctx context.Context) error {
		return service.PollPipelineComplete(ctx, "888", nil)
	})
	var runner coordinator.StepRunner
	_, updates, unsubscribe := runner.Subscribe(32)
	defer unsubscribe()
	if err := runner.Execute(context.Background(), []*coordinator.Step{step}); err != nil {
		t.Fatal(err)
	}
	if reader.gets != 2 || reader.timelineGets != 1 || sleeps != 1 {
		t.Fatalf("gets = %d, timeline gets = %d, sleeps = %d", reader.gets, reader.timelineGets, sleeps)
	}
	foundTimelineProgress := false
drain:
	for {
		select {
		case snapshot := <-updates:
			for _, candidate := range snapshot.Steps {
				if candidate.Progress != nil && len(candidate.Progress.Items) == 1 &&
					candidate.Progress.Items[0] == "Build and test › linux-amd64 › Build image" {

					foundTimelineProgress = true
				}
			}
		default:
			break drain
		}
	}
	if !foundTimelineProgress {
		t.Fatal("runner snapshots did not include the active Azure timeline path")
	}
}

func TestTimelineStepProgressShowsParallelWork(t *testing.T) {
	progress := timelineStepProgress(888, azdopipeline.RunStateRunning, []azdopipeline.TimelineRecord{
		{ID: "stage", Type: "Stage", Name: "Publish", State: "inProgress"},
		{ID: "job-a", ParentID: "stage", Type: "Job", Name: "linux-amd64", State: "inProgress", Order: 1},
		{ID: "task-a", ParentID: "job-a", Type: "Task", Name: "Push image", State: "inProgress", Order: 2},
		{ID: "job-b", ParentID: "stage", Type: "Job", Name: "windows-amd64", State: "inProgress", Order: 3},
		{ID: "task-b", ParentID: "job-b", Type: "Task", Name: "Push image", State: "inProgress", Order: 4},
		{ID: "done", Type: "Stage", Name: "Build", State: "completed", Result: "succeeded"},
	})
	if progress.Summary != "Running 2 pipeline tasks in parallel" || progress.Completed != 1 || progress.Total != 2 {
		t.Fatalf("progress = %#v", progress)
	}
	if len(progress.Items) != 2 || progress.Items[0] != "Publish › linux-amd64 › Push image" ||
		progress.Items[1] != "Publish › windows-amd64 › Push image" {

		t.Fatalf("items = %#v", progress.Items)
	}
}

func TestTimelinePollingIsThrottled(t *testing.T) {
	reader := &fakeReader{timelines: []*azdopipeline.Timeline{{}}}
	for range 7 {
		reader.builds = append(reader.builds, &azdopipeline.Build{
			ID: 888, DefinitionID: DefinitionID, Status: "inProgress",
		})
	}
	reader.builds = append(reader.builds, &azdopipeline.Build{
		ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "succeeded",
	})
	config := completeConfig(Config{Mode: releasesteps.GoImagesReleaseModeNormal})
	config.PollInterval = time.Millisecond
	config.TimelinePollInterval = 3 * time.Millisecond
	service, err := New(reader, &fakeQueueClient{}, config, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PollPipelineComplete(context.Background(), "888", nil); err != nil {
		t.Fatal(err)
	}
	if reader.gets != 8 || reader.timelineGets != 3 {
		t.Fatalf("build polls = %d, timeline polls = %d; want 8 and 3", reader.gets, reader.timelineGets)
	}
}

func TestPollPipelineCompleteReportsFailure(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{
		ID: 888, DefinitionID: DefinitionID, Status: "completed", Result: "failed", WebURL: "https://example/build/888",
	}}}
	service := newTestService(t, reader, &fakeQueueClient{}, Config{Mode: releasesteps.GoImagesReleaseModeNormal})
	err := service.PollPipelineComplete(context.Background(), strconv.Itoa(888), nil)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "https://example/build/888") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRejectsInvalidRollbackBuild(t *testing.T) {
	_, err := New(&fakeReader{}, &fakeQueueClient{}, completeConfig(Config{
		Mode: releasesteps.GoImagesReleaseModeRollback, SourceBuildID: "bad",
	}), nil)
	if err == nil {
		t.Fatal("invalid rollback build was accepted")
	}
}

func TestPollHonorsCancellation(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{ID: 888, Status: "inProgress"}}}
	service, err := New(reader, &fakeQueueClient{}, completeConfig(Config{
		Mode: releasesteps.GoImagesReleaseModeNormal,
	}), func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.PollPipelineComplete(ctx, "888", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func newTestService(t *testing.T, reader PipelineReader, queue QueueClient, config Config) *Service {
	t.Helper()
	service, err := New(reader, queue, completeConfig(config), func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func completeConfig(config Config) Config {
	config.SessionID = "session"
	config.ExecutionDigest = testDigest
	config.Versions = []string{"1.26.5-2"}
	config.SourceVersion = testCommit
	if config.VerifyMirrorCommit == nil {
		config.VerifyMirrorCommit = func(context.Context, string) error { return nil }
	}
	config.PollInterval = time.Millisecond
	config.ReconcileInterval = time.Millisecond
	return config
}
