// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const serviceTestCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"

type fakeReader struct {
	builds       []*azdopipeline.Build
	recent       [][]*azdopipeline.Build
	failures     []azdopipeline.BuildFailure
	failureErr   error
	failureBlock bool
	getDelay     time.Duration
	gets         int
	lists        int
	failureGets  int
}

func (r *fakeReader) Get(ctx context.Context, _ int) (*azdopipeline.Build, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.getDelay > 0 {
		if err := sleepContext(ctx, r.getDelay); err != nil {
			return nil, err
		}
	}
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

func (r *fakeReader) GetFailures(ctx context.Context, _ int) ([]azdopipeline.BuildFailure, error) {
	r.failureGets++
	if r.failureBlock {
		<-ctx.Done()
		return nil, ctx.Err()
	}
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

func TestPipelineStatusValidatesIdentityAndTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*azdopipeline.Build)
	}{
		{name: "canceled test run"},
		{name: "different build", change: func(build *azdopipeline.Build) { build.ID = 889 }},
		{name: "different definition", change: func(build *azdopipeline.Build) { build.DefinitionID = 1 }},
		{name: "different branch", change: func(build *azdopipeline.Build) { build.SourceBranch = "refs/heads/other" }},
		{name: "different source", change: func(build *azdopipeline.Build) { build.SourceVersion = strings.Repeat("a", 40) }},
		{name: "different intent", change: func(build *azdopipeline.Build) { build.Parameters[executionDigestVariable] = "wrong-intent" }},
		{name: "different session", change: func(build *azdopipeline.Build) { build.Parameters[correlationVariable] = "other" }},
		{name: "different versions", change: func(build *azdopipeline.Build) { build.Parameters[versionsVariable] = `["1.25.1-1"]` }},
		{name: "different mode", change: func(build *azdopipeline.Build) { build.Parameters[modeVariable] = "normal" }},
		{name: "publication target", change: func(build *azdopipeline.Build) { build.TemplateParameters["publishRepoPrefix"] = "public/" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			build := testPipelineBuild(t, ModeTest, "completed", "canceled")
			if test.change != nil {
				test.change(build)
			}
			reader, queue := &fakeReader{builds: []*azdopipeline.Build{build}}, &fakeQueueClient{}
			service := newTestService(t, reader, queue, PipelineRunConfig{Mode: ModeTest})
			observation, err := service.GetPipelineStatus(context.Background(), "888")
			if test.change == nil {
				if err != nil || observation.Status != coordinator.StepStatusCanceled || observation.Progress.RunURL != pipelineRunURL("888") {
					t.Fatalf("observation = %#v, error = %v", observation, err)
				}
			} else if err == nil {
				t.Fatalf("unrelated build was accepted: %#v", observation)
			}
			if queue.calls != 0 || reader.gets != 1 || reader.lists != 0 || reader.failureGets != 0 {
				t.Fatalf("observation caused extra actions: queue=%d, get=%d, list=%d, failures=%d",
					queue.calls, reader.gets, reader.lists, reader.failureGets)
			}
		})
	}
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

func TestPipelineStatusReadsOnceWithoutPolling(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{
		testPipelineBuild(t, ModeNormal, "inProgress", ""),
		testPipelineBuild(t, ModeNormal, "completed", "succeeded"),
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
	for index, status := range []coordinator.StepStatus{coordinator.StepStatusRunning, coordinator.StepStatusSucceeded} {
		observation, err := service.GetPipelineStatus(context.Background(), "888")
		if err != nil || observation.Status != status || reader.gets != index+1 || reader.failureGets != 0 || sleeps != 0 {
			t.Fatalf("observation=%#v, err=%v, gets=%d, failure gets=%d, sleeps=%d",
				observation, err, reader.gets, reader.failureGets, sleeps)
		}
	}
}

func TestPipelineStatusReportsFailure(t *testing.T) {
	reader := &fakeReader{
		builds: []*azdopipeline.Build{testPipelineBuild(t, ModeNormal, "completed", "failed")},
		failures: []azdopipeline.BuildFailure{{
			Path: "Build > Linux arm32 > Build Images", Message: "PowerShell exited with code 1",
		}},
	}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	observation, err := service.GetPipelineStatus(context.Background(), "888")
	if err != nil || observation.Status != coordinator.StepStatusFailed ||
		!strings.Contains(observation.Error, "Build > Linux arm32 > Build Images: PowerShell exited with code 1") ||
		observation.Progress.RunURL != pipelineRunURL("888") || reader.failureGets != 1 {

		t.Fatalf("observation=%#v, err=%v", observation, err)
	}
}

func TestPipelineStatusReportsCancellation(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{testPipelineBuild(t, ModeTest, "completed", "canceled")}}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeTest})
	observation, err := service.GetPipelineStatus(context.Background(), "888")
	if err != nil || observation.Status != coordinator.StepStatusCanceled ||
		!strings.Contains(observation.Error, "go-images release build 888 canceled") || reader.failureGets != 0 {

		t.Fatalf("observation=%#v, err=%v", observation, err)
	}
}

func TestPipelineStatusFailureDetailsAreOptional(t *testing.T) {
	reader := &fakeReader{
		builds:     []*azdopipeline.Build{testPipelineBuild(t, ModeNormal, "completed", "failed")},
		failureErr: errors.New("timeline unavailable"),
	}
	service := newTestService(t, reader, &fakeQueueClient{}, PipelineRunConfig{Mode: ModeNormal})
	observation, err := service.GetPipelineStatus(context.Background(), "888")
	if err != nil || !strings.Contains(observation.Error, `failed with result "failed"`) ||
		observation.Progress.RunURL != pipelineRunURL("888") {

		t.Fatalf("observation=%#v, err=%v", observation, err)
	}
}

func TestPipelineDiagnosticTimeoutKeepsConfirmedFailure(t *testing.T) {
	for _, test := range []struct {
		timeout  time.Duration
		readTime time.Duration
	}{
		{timeout: 5 * time.Second},
		{timeout: 200 * time.Millisecond},
		{timeout: 5 * time.Second, readTime: 4900 * time.Millisecond},
	} {
		t.Run(test.timeout.String()+"/read-"+test.readTime.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reader := &fakeReader{
					builds:       []*azdopipeline.Build{testPipelineBuild(t, ModeNormal, "completed", "failed")},
					failureBlock: true,
					getDelay:     test.readTime,
				}
				queue := new(fakeQueueClient)
				service := newTestService(t, reader, queue, PipelineRunConfig{Mode: ModeNormal})
				input := &Input{Versions: []string{"1.26.5-2"}, Mode: ModeNormal, SourceVersion: serviceTestCommit}
				state, err := NewState(input)
				if err != nil {
					t.Fatal(err)
				}
				state.VerifiedMirroredCommit, state.QueueAttempted, state.BuildID = serviceTestCommit, true, "888"
				checkpoints := 0
				steps, _, err := NewGraphWithCheckpoint(input, state, service, func(*State) { checkpoints++ })
				if err != nil {
					t.Fatal(err)
				}
				runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
				if err != nil {
					t.Fatal(err)
				}
				saved := runner.ExecutionSnapshot()
				last := len(saved.Steps) - 1
				saved.Steps[last].Status = coordinator.StepStatusRunning
				runner, err = coordinator.NewStepRunner(steps, saved)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
				defer cancel()
				started := time.Now()
				if err := runner.Observe(ctx); err != nil {
					t.Fatalf("optional diagnostics invalidated the confirmed failure: %v", err)
				}
				snapshot := runner.Snapshot()
				if snapshot.Steps[last].ObservationError != "" || snapshot.Steps[last].Status != coordinator.StepStatusFailed ||
					snapshot.Steps[last].Progress.RunURL != pipelineRunURL("888") ||
					!strings.Contains(snapshot.Steps[last].Error, `failed with result "failed"`) ||
					ctx.Err() != nil || time.Since(started) != test.readTime+min(time.Second, (test.timeout-test.readTime)/2) {

					t.Fatalf("confirmed failure was lost or diagnostics consumed the refresh budget: %#v", snapshot)
				}
				if state.Complete || checkpoints != 0 || queue.calls != 0 || reader.gets != 1 ||
					reader.failureGets != 1 || runner.ExecutionSnapshot().Steps[last].Status != coordinator.StepStatusRunning {

					t.Fatal("read-only refresh changed execution or made unexpected provider calls")
				}
				cancel()
				err = runner.Execute(context.Background(), steps)
				if err == nil || !strings.Contains(err.Error(), `failed with result "failed"`) ||
					errors.Is(err, context.DeadlineExceeded) || !state.Complete || state.Result != "failed" ||
					checkpoints != 1 || queue.calls != 0 || reader.gets != 2 || reader.failureGets != 2 {

					t.Fatalf("execution did not preserve the same confirmed outcome: error=%v, state=%#v, checkpoints=%d",
						err, state, checkpoints)
				}
			})
		})
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

func TestPipelineReadHonorsCancellation(t *testing.T) {
	reader := &fakeReader{builds: []*azdopipeline.Build{{ID: 888, DefinitionID: DefinitionID, Status: "inProgress"}}}
	service, err := NewPipelineRunService(reader, &fakeQueueClient{}, completeConfig(PipelineRunConfig{
		Mode: ModeNormal,
	}), func(ctx context.Context, _ time.Duration) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.GetPipelineStatus(ctx, "888"); !errors.Is(err, context.Canceled) {
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
	config.ReconcileInterval = time.Millisecond
	return config
}

func testPipelineBuild(t *testing.T, mode Mode, status, result string) *azdopipeline.Build {
	t.Helper()
	parameters, err := PipelineParameters(mode, "")
	if err != nil {
		t.Fatal(err)
	}
	template := make(map[string]any, len(parameters))
	for name, value := range parameters {
		template[name] = value
	}
	return &azdopipeline.Build{
		ID: 888, DefinitionID: DefinitionID, SourceBranch: SourceBranch, SourceVersion: serviceTestCommit,
		Status: status, Result: result,
		Parameters: map[string]string{
			correlationVariable: "session", executionDigestVariable: testDigest,
			modeVariable: string(mode), versionsVariable: `["1.26.5-2"]`, sourceBuildVariable: "",
		},
		TemplateParameters: template,
	}
}
