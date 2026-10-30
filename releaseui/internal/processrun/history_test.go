// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type viewingTestGroup struct {
	process contract.Process
}

func (g viewingTestGroup) Identity() *contract.Identity {
	return &contract.Identity{Name: "Viewing tests", Mark: "VT"}
}

func (g viewingTestGroup) Processes() []contract.Process { return []contract.Process{g.process} }

type viewingTestProcess struct {
	run *testRun
}

func (p viewingTestProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{ID: "example", Identity: contract.Identity{Name: "Test run"}}
}

func (p viewingTestProcess) Preflight(context.Context) (error, error) {
	return nil, errors.New("new execution is unavailable in this fixture")
}

func (p viewingTestProcess) InputForm(*releaseinput.Set) any { return nil }

func (p viewingTestProcess) Prepare(context.Context, any) (*contract.StateSnapshot, error) {
	return nil, errors.New("this fixture only loads saved runs")
}

func (p viewingTestProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	run := *p.run
	run.snapshot = runstore.CloneSnapshot(snapshot)
	return &run, nil
}

func TestOpeningSavedGraphPreservesProgressWithoutExecution(t *testing.T) {
	for _, test := range []struct {
		name     string
		closed   bool
		complete bool
	}{
		{name: "open checkpoint"},
		{name: "closed checkpoint", closed: true},
		{name: "completed failure", complete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, executions := viewingTestManager(t, test.closed, test.complete, nil)
			before, err := store.Get(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := manager.History().Select(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
			}
			current, ok := manager.Current("example")
			runner, _, _ := manager.Active("example")
			graph := runner.Snapshot()
			if !ok || graph.Active || !current.Execution.Saved ||
				current.Execution.CanResume != (!test.closed && !test.complete) {

				t.Fatalf("saved view = %#v, found = %v", current, ok)
			}
			if !reflect.DeepEqual(graph, before.Run.Execution) {
				t.Fatalf("graph changed on open: got %#v, want %#v", graph, before.Run.Execution)
			}
			if current.Execution.RunURL != "https://example.invalid/runs/888" {
				t.Fatal("opening a saved graph lost its external link")
			}
			viewer := New(manager.ctx, manager.processes, store)
			if _, err := viewer.History().Select(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			other, ok := viewer.Current("example")
			otherRunner, _, _ := viewer.Active("example")
			if !ok || !reflect.DeepEqual(otherRunner.Snapshot(), graph) ||
				!reflect.DeepEqual(other.Steps, current.Steps) {

				t.Fatal("a separate viewer could not load the saved graph")
			}
			after, err := store.Get(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || store.updates.Load() != 0 || executions.Load() != 0 {
				t.Fatal("opening a saved graph executed work or changed its record")
			}
		})
	}
}

func TestResumeRequiresConfirmationAndExpectedRevision(t *testing.T) {
	for _, test := range []struct {
		name      string
		closed    bool
		complete  bool
		confirmed bool
		revision  int
		remoteRev bool
	}{
		{name: "unconfirmed", revision: 3},
		{name: "stale reviewed revision", confirmed: true, revision: 2},
		{name: "remote revision changed", confirmed: true, revision: 3, remoteRev: true},
		{name: "closed", confirmed: true, revision: 3, closed: true},
		{name: "complete", confirmed: true, revision: 3, complete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, executions := viewingTestManager(t, test.closed, test.complete, nil)
			if _, err := manager.History().Select(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			if test.remoteRev {
				store.mu.Lock()
				store.record.Revision++
				store.mu.Unlock()
			}
			current, _ := manager.Current("example")
			err := manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
				PlanDigest: current.Execution.PlanDigest, Confirmed: test.confirmed, Revision: test.revision,
			})
			if err == nil || executions.Load() != 0 || manager.Running("example") {
				t.Fatalf("resume error = %v, executions = %d", err, executions.Load())
			}
		})
	}
}

func TestConcurrentResumeStartsOneExecutorAndKeepsTheRecord(t *testing.T) {
	manager, store, executions := viewingTestManager(t, false, false, nil)
	if _, err := manager.History().Select(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.Current("example")
	start := webview.ReleaseStartRequest{PlanDigest: current.Execution.PlanDigest, Confirmed: true, Revision: 3}
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	result := make(chan error, 1)
	go func() { result <- manager.Resume(context.Background(), "example", start) }()
	waitForTestSignal(t, store.updateStarted)
	if err := manager.Resume(context.Background(), "example", start); err == nil {
		t.Fatal("duplicate resume was accepted")
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, err := store.Get(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if record.Run.Complete {
			if record.ID != 1 || record.Run.Result != resultSucceeded || executions.Load() != 4 {
				t.Fatalf("resumed record = %#v, executions = %d", record, executions.Load())
			}
			for _, step := range record.Run.Execution.Steps {
				if step.Status != coordinator.StepStatusSucceeded {
					t.Fatalf("final graph lost a step outcome: %#v", record.Run.Execution)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resumed fake graph did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOpeningRecordWithoutSavedGraphReportsUnsupportedState(t *testing.T) {
	manager, store, executions := viewingTestManager(t, false, false, nil)
	store.record.Run.Execution = coordinator.Snapshot{}
	if _, err := manager.History().releaseSummary(context.Background(), store.record); err == nil {
		t.Fatal("dashboard offered a record with no saved graph as openable")
	}
	if _, err := manager.History().Select(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "no saved graph") {
		t.Fatalf("open error = %v", err)
	}
	if executions.Load() != 0 || store.updates.Load() != 0 {
		t.Fatal("missing graph caused execution or a migration write")
	}
}

func TestImportRejectsIncompatibleSavedGraphsWithoutWriting(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*coordinator.Snapshot)
		wantErr string
	}{
		{
			name:    "missing graph",
			change:  func(graph *coordinator.Snapshot) { graph.Steps = nil },
			wantErr: "no saved graph",
		},
		{
			name:    "different step",
			change:  func(graph *coordinator.Snapshot) { graph.Steps[len(graph.Steps)-1].Name = "Other" },
			wantErr: "does not match the saved graph",
		},
		{
			name:    "different dependencies",
			change:  func(graph *coordinator.Snapshot) { graph.Steps[len(graph.Steps)-1].DependsOn = nil },
			wantErr: "does not match the saved graph",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, executions := viewingTestManager(t, false, false, nil)
			before, err := store.Get(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			exported, err := manager.History().Export(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&exported.Run.Execution)
			if err := exported.Run.Validate(); err != nil {
				t.Fatalf("fixture should pass structural validation: %v", err)
			}
			_, err = manager.History().Import(context.Background(), 1, exported)
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != ErrorInvalid || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("import error = %v, want invalid graph", err)
			}
			after, err := store.Get(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) || store.updates.Load() != 0 || executions.Load() != 0 {
				t.Fatal("invalid graph import changed the record or executed a step")
			}
		})
	}
}

func TestImportAcceptsRepairedProgressAndReopensRecord(t *testing.T) {
	manager, store, executions := viewingTestManager(t, false, false, nil)
	exported, err := manager.History().Export(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for index := range exported.Run.Execution.Steps {
		step := &exported.Run.Execution.Steps[index]
		if step.Name == "Work A" {
			step.Progress = &coordinator.StepProgress{
				Completed: 6, Total: 30, RunURL: "https://example.invalid/runs/888",
			}
		}
	}
	updated, err := manager.History().Import(context.Background(), 1, exported)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 4 {
		t.Fatalf("import revision = %d, want 4", updated.Revision)
	}
	if _, err := manager.History().Select(context.Background(), 1); err != nil {
		t.Fatalf("accepted repair could not be reopened: %v", err)
	}
	runner, _, _ := manager.Active("example")
	if !reflect.DeepEqual(runner.ExecutionSnapshot(), updated.Run.Execution) ||
		store.updates.Load() != 1 || executions.Load() != 0 {

		t.Fatal("opening the accepted repair lost progress, wrote again, or executed a step")
	}
}

func TestSavedGraphRefreshesWithoutExecutionOrWritesAndStopsOnShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var reads atomic.Int32
		observer := func(ctx context.Context) (*coordinator.Observation, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("observation has no deadline")
			}
			read := reads.Add(1)
			if read >= 3 {
				return nil, errors.New("provider unavailable")
			}
			status := coordinator.StepStatusRunning
			if read == 2 {
				status = coordinator.StepStatusCanceled
			}
			return &coordinator.Observation{
				Status:   status,
				Progress: coordinator.StepProgress{Summary: string(status), RunURL: "https://example.invalid/runs/888"},
			}, nil
		}
		manager, store, executions := viewingTestManager(t, false, false, observer)
		ctx, cancel := context.WithCancel(manager.ctx)
		defer cancel()
		manager.ctx = ctx
		before, err := store.Get(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := manager.History().Select(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		runner, _, _ := manager.Active("example")
		initial, updates, unsubscribe := runner.Subscribe(4)
		defer unsubscribe()
		work := slices.IndexFunc(initial.Steps, func(step coordinator.StepSnapshot) bool { return step.Name == "Work A" })
		if work < 0 {
			t.Fatal("saved graph has no Work A step")
		}
		if reads.Load() != 1 || initial.Active || initial.Steps[work].ObservedAt.IsZero() ||
			initial.Steps[work].Status != coordinator.StepStatusRunning {

			t.Fatalf("initial observation=%#v, reads=%d", initial, reads.Load())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		changed := <-updates
		if changed.Active || changed.Steps[work].Status != coordinator.StepStatusCanceled ||
			!changed.Steps[work].ObservedAt.After(initial.Steps[work].ObservedAt) || reads.Load() != 2 {

			t.Fatalf("refreshed event=%#v, reads=%d", changed, reads.Load())
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		failed := <-updates
		if failed.Active || failed.Steps[work].ObservationError == "" ||
			failed.Steps[work].Status != coordinator.StepStatusCanceled ||
			failed.Steps[work].ObservedAt != changed.Steps[work].ObservedAt {

			t.Fatalf("failed refresh lost last known data: %#v", failed)
		}
		after, err := store.Get(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || store.updates.Load() != 0 || executions.Load() != 0 ||
			manager.Running("example") {

			t.Fatal("read-only refresh changed the release or its execution ownership")
		}
		cancel()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if reads.Load() != 3 {
			t.Fatal("observation continued after the server context stopped")
		}
	})
}

func TestInterruptedExecutionReturnsToReadOnlyObservation(t *testing.T) {
	var reads atomic.Int32
	first := make(chan struct{})
	var once sync.Once
	observer := func(context.Context) (*coordinator.Observation, error) {
		reads.Add(1)
		once.Do(func() { close(first) })
		return &coordinator.Observation{Status: coordinator.StepStatusRunning}, nil
	}
	manager, store, _ := viewingTestManager(t, false, false, observer)
	registered, ok := manager.processes.Lookup("example")
	if !ok {
		t.Fatal("fixture process is not configured")
	}
	process, ok := registered.Implementation.(viewingTestProcess)
	if !ok {
		t.Fatal("unexpected fixture process")
	}
	build := process.run.build
	process.run.build = func(ctx context.Context, checkpoint contract.CheckpointFunc) ([]*coordinator.Step, error) {
		steps, err := build(ctx, checkpoint)
		if err == nil && checkpoint != nil {
			steps[len(steps)-1].Func = func(context.Context) error { return context.DeadlineExceeded }
		}
		return steps, err
	}
	if _, err := manager.History().Select(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("initial observation did not start")
	}
	current, _ := manager.Current("example")
	if err := manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
		PlanDigest: current.Execution.PlanDigest, Confirmed: true, Revision: 3,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for (manager.Running("example") || reads.Load() < 2) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	current, _ = manager.Current("example")
	stored, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if manager.Running("example") || reads.Load() < 2 || !current.Execution.Saved ||
		!current.Execution.CanResume || stored.Run.Complete || stored.Run.Result != "" {

		t.Fatalf("interrupted execution did not return to read-only viewing: %#v, reads=%d", current, reads.Load())
	}
}

func TestResumeCancelsTheSavedGraphWatcher(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan error, 1)
	observer := func(ctx context.Context) (*coordinator.Observation, error) {
		close(entered)
		<-ctx.Done()
		stopped <- ctx.Err()
		return nil, ctx.Err()
	}
	manager, _, executions := viewingTestManager(t, false, false, observer)
	if _, err := manager.History().Select(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read-only watcher did not start")
	}
	runner, _, _ := manager.Active("example")
	current, _ := manager.Current("example")
	if err := manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
		PlanDigest: current.Execution.PlanDigest, Confirmed: true, Revision: 3,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("watcher timed out instead of being canceled by resume: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume left the read-only watcher running")
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Running("example") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	resumed, _, _ := manager.Active("example")
	if resumed != runner || executions.Load() != 4 {
		t.Fatalf("resume replaced the runner or retained observation data: %#v", runner.Snapshot())
	}
	for _, step := range runner.Snapshot().Steps {
		if step.ObservationError != "" {
			t.Fatalf("resume retained a step's observation error: %#v", step)
		}
	}
}

func viewingTestManager(
	t *testing.T,
	closed, complete bool,
	observer func(context.Context) (*coordinator.Observation, error),
) (*Manager, *testStore, *atomic.Int32) {
	t.Helper()
	var executions atomic.Int32
	action := func(context.Context) error {
		executions.Add(1)
		return nil
	}
	build := func(context.Context, contract.CheckpointFunc) ([]*coordinator.Step, error) {
		first := coordinator.NewRootStep("Initialize", time.Minute, action)
		one := first.Then("Work A", time.Minute, action)
		one.Observe = observer
		two := first.Then("Work B", time.Minute, action)
		last := one.Then("Finish", time.Minute, action, two)
		return last.TransitiveDependencies()
	}
	steps, err := build(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.NewStepRunner(steps, coordinator.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	graph := runner.Snapshot()
	for index := range graph.Steps {
		step := &graph.Steps[index]
		switch step.Name {
		case "Initialize":
			step.Status = coordinator.StepStatusSucceeded
		case "Work A":
			step.Status = coordinator.StepStatusRunning
			step.Progress = &coordinator.StepProgress{Completed: 5, Total: 30}
			if complete {
				step.Status, step.Error = coordinator.StepStatusFailed, "fake work failed"
			}
		case "Work B":
			if complete {
				step.Status = coordinator.StepStatusSucceeded
			}
		case "Finish":
			if complete {
				step.Status = coordinator.StepStatusBlocked
			}
		}
	}
	run := &testRun{
		snapshot: &contract.StateSnapshot{Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`)},
		view:     &contract.RunView{Test: true, RunURL: "https://example.invalid/runs/888"},
		build:    build,
	}
	state, err := runstore.NewState("example", run.snapshot, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.Started, state.Checkpointed, state.Complete, state.Execution = true, true, complete, graph
	if complete {
		state.Result = resultFailed
		state.Execution.Error = "fake work failed"
	}
	store := &testStore{record: &runstore.Record{
		ID: 1, Revision: 3, Closed: closed, UpdatedAt: time.Now().UTC(), Run: state,
	}}
	registry, err := processregistry.New(viewingTestGroup{process: viewingTestProcess{run: run}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return New(ctx, registry, store), store, &executions
}
