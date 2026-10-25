// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfra

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

const testGoInfraHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGoInfraGitHub struct {
	mu sync.Mutex

	preflightCalls             int
	getPRCalls                 int
	labelCalls                 int
	dispatches                 []bool
	runReads                   int
	workflowRun                GoInfraWorkflowRun
	workflowUpdates            []GoInfraWorkflowRun
	pullRequest                GoInfraPullRequest
	preflightErr               error
	releaseOnMergePreflightErr error
	labelErr                   error
	dispatchErr                error
	readErr                    error
}

type fakeGoInfraGitHubStats struct {
	preflightCalls int
	getPRCalls     int
	labelCalls     int
	dispatches     []bool
	runReads       int
}

func (f *fakeGoInfraGitHub) integration() GitHubService {
	return f
}

func (f *fakeGoInfraGitHub) Preflight(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return "verified fake GitHub integration", f.preflightErr
}

func (f *fakeGoInfraGitHub) PreflightReleaseOnMerge(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return "verified fake release-on-merge integration", f.releaseOnMergePreflightErr
}

func (f *fakeGoInfraGitHub) GetPullRequest(_ context.Context, number int) (GoInfraPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getPRCalls++
	if number != f.pullRequest.Number {
		return GoInfraPullRequest{}, errors.New("unexpected pull request")
	}
	return f.pullRequest, nil
}

func (f *fakeGoInfraGitHub) AddReleaseOnMergeLabel(
	_ context.Context,
	number int,
	expectedHeadSHA string,
) (GoInfraPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labelCalls++
	if number != f.pullRequest.Number || expectedHeadSHA != f.pullRequest.HeadSHA {
		return GoInfraPullRequest{}, errors.New("reviewed pull request changed")
	}
	return f.pullRequest, f.labelErr
}

func (f *fakeGoInfraGitHub) DispatchPatchRelease(_ context.Context, dryRun bool) (GoInfraWorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatches = append(f.dispatches, dryRun)
	if f.dispatchErr != nil {
		return GoInfraWorkflowRun{}, f.dispatchErr
	}
	if f.workflowRun.ID == 0 {
		f.workflowRun = testGoInfraWorkflowRun("queued", "")
	}
	return f.workflowRun, nil
}

func (f *fakeGoInfraGitHub) GetWorkflowRun(_ context.Context, id int64) (GoInfraWorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runReads++
	if f.workflowRun.ID != id {
		return GoInfraWorkflowRun{}, errors.New("unexpected workflow run")
	}
	if f.readErr != nil {
		return GoInfraWorkflowRun{}, f.readErr
	}
	if len(f.workflowUpdates) != 0 {
		run := f.workflowUpdates[0]
		f.workflowUpdates = f.workflowUpdates[1:]
		return run, nil
	}
	if f.workflowRun.Status != "completed" {
		return testGoInfraWorkflowRun("completed", "success"), nil
	}
	return f.workflowRun, nil
}

func (f *fakeGoInfraGitHub) stats() fakeGoInfraGitHubStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeGoInfraGitHubStats{
		preflightCalls: f.preflightCalls, getPRCalls: f.getPRCalls,
		labelCalls: f.labelCalls, dispatches: append([]bool(nil), f.dispatches...),
		runReads: f.runReads,
	}
}

func testGoInfraWorkflowRun(status, conclusion string) GoInfraWorkflowRun {
	return GoInfraWorkflowRun{
		ID: 123, URL: "https://github.com/microsoft/go-infra/actions/runs/123",
		Status: status, Conclusion: conclusion, HeadSHA: testGoInfraHeadSHA, CreatedAt: time.Now().UTC(),
	}
}

func testGoInfraPullRequest() GoInfraPullRequest {
	return GoInfraPullRequest{
		Number: 42, Title: "Prepare release", URL: "https://github.com/microsoft/go-infra/pull/42",
		BaseRef: goInfraDefaultRef, HeadRef: "release-change", HeadSHA: testGoInfraHeadSHA,
	}
}

func TestProcessGroupDefinesConcreteReleaseProcesses(t *testing.T) {
	group := NewProcess((&fakeGoInfraGitHub{}).integration())
	processes := group.Processes()
	if len(processes) != 3 {
		t.Fatalf("process count = %d, want 3", len(processes))
	}
	got := []string{
		processes[0].Definition().ID,
		processes[1].Definition().ID,
		processes[2].Definition().ID,
	}
	want := []string{"go-infra-release-on-merge", "go-infra-dry-run", "go-infra-publish"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("process IDs = %v, want %v", got, want)
		}
	}
}

func TestManualWorkflowReadinessDoesNotBlockReleaseOnMerge(t *testing.T) {
	workflowErr := errors.New("manual workflow correlation is unavailable")
	service := &fakeGoInfraGitHub{preflightErr: workflowErr}
	processes := NewProcess(service).Processes()
	for index, process := range processes {
		warning, blocking := process.Preflight(context.Background())
		if warning != nil {
			t.Fatalf("process %s returned warning %v", process.Definition().ID, warning)
		}
		if index == 0 {
			if blocking != nil {
				t.Fatalf("release-on-merge was blocked by manual workflow readiness: %v", blocking)
			}
		} else if !errors.Is(blocking, workflowErr) {
			t.Fatalf("process %s did not enforce manual workflow readiness: %v", process.Definition().ID, blocking)
		}
	}
}

func TestReleaseOnMergeProcess(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	process := NewProcess(github.integration()).Processes()[0]
	inputs := &releaseinput.Set{}
	form := process.InputForm(inputs).(*releaseOnMergeInput)
	if err := inputs.Parse(json.RawMessage(`{"pullRequest":"42"}`)); err != nil {
		t.Fatal(err)
	}
	if form.PullRequest != 42 {
		t.Fatalf("pull request = %d, want 42", form.PullRequest)
	}
	snapshot, err := process.Prepare(context.Background(), form)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan := run.Plan(); plan == nil || plan.ExecutionButtonLabel != "Apply release-on-merge" {
		t.Fatalf("plan = %#v", plan)
	}
	steps, err := run.Build(context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&coordinator.StepRunner{}).Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if stats := github.stats(); stats.getPRCalls != 1 || stats.labelCalls != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestDryRunGraphObservationDoesNotDispatchOrMutateSavedState(t *testing.T) {
	github := &fakeGoInfraGitHub{workflowRun: testGoInfraWorkflowRun("completed", "cancelled")}
	process := NewProcess(github.integration()).Processes()[1]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input, state, err := decodeGoInfraSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	saved := testGoInfraWorkflowRun("queued", "")
	state.WorkflowRun = &saved
	snapshot, err = encodeGoInfraSnapshot(input, state)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	before := run.TakeSnapshot()
	steps, err := run.Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	observation := runner.Snapshot().Steps[1]
	if observation.Status != coordinator.StepStatusCanceled || observation.Progress.RunURL != saved.URL {
		t.Fatalf("observation = %#v", observation)
	}
	after := run.TakeSnapshot()
	if string(before.Input) != string(after.Input) || string(before.State) != string(after.State) {
		t.Fatal("workflow observation changed the saved snapshot")
	}
	if stats := github.stats(); stats.labelCalls != 0 || len(stats.dispatches) != 0 || stats.runReads != 1 {
		t.Fatalf("workflow observation executed actions: %#v", stats)
	}
}

func TestKnownWorkflowMonitorPreservesTerminalProviderConclusions(t *testing.T) {
	for _, conclusion := range []string{"success", "failure", "cancelled", "timed_out", "skipped"} {
		t.Run(conclusion, func(t *testing.T) {
			saved := testGoInfraWorkflowRun("queued", "")
			github := &fakeGoInfraGitHub{
				workflowRun:     saved,
				workflowUpdates: []GoInfraWorkflowRun{testGoInfraWorkflowRun("completed", conclusion)},
			}
			process := NewProcess(github.integration()).Processes()[1]
			snapshot, err := process.Prepare(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			input, state, err := decodeGoInfraSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			state.WorkflowRun = &saved
			snapshot, err = encodeGoInfraSnapshot(input, state)
			if err != nil {
				t.Fatal(err)
			}
			run, err := process.Load(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			checkpoints := 0
			steps, err := run.Build(context.Background(), func() { checkpoints++ })
			if err != nil {
				t.Fatal(err)
			}
			err = (&coordinator.StepRunner{}).Execute(context.Background(), steps)
			if (err == nil) != (conclusion == "success") ||
				errors.Is(err, coordinator.ErrCanceled) != (conclusion == "cancelled") {

				t.Fatalf("conclusion %q was misclassified: %v", conclusion, err)
			}
			_, stored, err := decodeGoInfraSnapshot(run.TakeSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			if stored.WorkflowRun.Conclusion != conclusion || checkpoints != 1 {
				t.Fatalf("raw conclusion was lost: %#v, checkpoints=%d", stored.WorkflowRun, checkpoints)
			}
			if stats := github.stats(); len(stats.dispatches) != 0 || stats.labelCalls != 0 || stats.runReads != 1 {
				t.Fatalf("known-run monitoring performed other actions: %#v", stats)
			}
		})
	}
}

func TestPreparedGraphObservationDoesNotDiscoverOrStartAWorkflow(t *testing.T) {
	github := new(fakeGoInfraGitHub)
	process := NewProcess(github.integration()).Processes()[1]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := run.Build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := github.stats(); stats.runReads != 0 || len(stats.dispatches) != 0 || stats.labelCalls != 0 {
		t.Fatalf("observation without a saved ID contacted the provider: %#v", stats)
	}
}

func TestDryRunCheckpointsWorkflowState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		github := &fakeGoInfraGitHub{
			workflowUpdates: []GoInfraWorkflowRun{
				testGoInfraWorkflowRun("in_progress", ""),
				testGoInfraWorkflowRun("completed", "success"),
			},
		}
		process := NewProcess(github.integration()).Processes()[1]
		snapshot, err := process.Prepare(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		run, err := process.Load(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		checkpoints := 0
		steps, err := run.Build(context.Background(), func() {
			checkpoints++
			if _, err := process.Load(run.TakeSnapshot()); err != nil {
				t.Errorf("load checkpoint: %v", err)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := (&coordinator.StepRunner{}).Execute(context.Background(), steps); err != nil {
			t.Fatal(err)
		}
		if checkpoints != 3 {
			t.Fatalf("checkpoint count = %d, want 3", checkpoints)
		}
		view := run.TakeView()
		if !view.Test || view.Summary != "GitHub workflow completed" ||
			view.RunURL != "https://github.com/microsoft/go-infra/actions/runs/123" {

			t.Fatalf("view = %#v", view)
		}
		if stats := github.stats(); len(stats.dispatches) != 1 || stats.runReads != 2 {
			t.Fatalf("shared monitor calls = %#v", stats)
		}
	})
}
