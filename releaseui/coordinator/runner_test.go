// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func execute(t *testing.T, step *Step) error {
	t.Helper()
	steps, err := step.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	var sr StepRunner
	return sr.Execute(context.Background(), steps)
}

func TestNewStepRunnerDisplaysSavedGraphWithoutExecuting(t *testing.T) {
	executions := 0
	action := func(context.Context) error {
		executions++
		return nil
	}
	first := NewRootStep("Initialize", time.Minute, action)
	second := first.Then("Work", time.Minute, action)
	saved := Snapshot{
		Sequence: 7, Active: true, Error: "last recorded error",
		Steps: []StepSnapshot{
			{Name: "Initialize", DependsOn: []string{}, Status: StepStatusSucceeded},
			{
				Name: "Work", DependsOn: []string{"Initialize"}, Status: StepStatusRunning,
				Progress: &StepProgress{Completed: 5, Total: 30, Summary: "Saved work"},
			},
		},
	}
	runner, err := NewStepRunner([]*Step{first, second}, saved)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runner.Snapshot()
	if executions != 0 || snapshot.Active || snapshot.Sequence != saved.Sequence || snapshot.Error != saved.Error {
		t.Fatalf("view = %#v, executions = %d", snapshot, executions)
	}
	if !reflect.DeepEqual(snapshot.Steps, saved.Steps) {
		t.Fatalf("restored steps = %#v, want %#v", snapshot.Steps, saved.Steps)
	}
	saved.Steps[1].Progress.Completed = 30
	saved.Steps[1].DependsOn[0] = "changed"
	if got := runner.Snapshot(); got.Steps[1].Progress.Completed != 5 || got.Steps[1].DependsOn[0] != "Initialize" {
		t.Fatalf("caller mutated the restored graph: %#v", got)
	}
	snapshot.Steps[1].Progress.Completed = 99
	if got := runner.Snapshot(); got.Steps[1].Progress.Completed != 5 {
		t.Fatal("returned snapshot mutated the restored graph")
	}
	if err := runner.Execute(context.Background(), []*Step{first, second}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("explicit execution calls = %d, want 2", executions)
	}
}

func TestNewStepRunnerRejectsIncompatibleSavedGraph(t *testing.T) {
	first := NewRootStep("First", time.Minute, func(context.Context) error { return nil })
	second := first.Then("Second", time.Minute, func(context.Context) error { return nil })
	for _, test := range []struct {
		name  string
		saved []StepSnapshot
	}{
		{name: "missing step", saved: []StepSnapshot{{Name: "First", Status: StepStatusSucceeded}}},
		{name: "duplicate name", saved: []StepSnapshot{
			{Name: "First", Status: StepStatusSucceeded}, {Name: "First", Status: StepStatusWaiting},
		}},
		{name: "unknown status", saved: []StepSnapshot{
			{Name: "First", Status: "guess"}, {Name: "Second", DependsOn: []string{"First"}, Status: StepStatusWaiting},
		}},
		{name: "different dependency", saved: []StepSnapshot{
			{Name: "First", Status: StepStatusSucceeded}, {Name: "Second", Status: StepStatusWaiting},
		}},
		{name: "unordered dependency", saved: []StepSnapshot{
			{Name: "First", DependsOn: []string{"Second"}, Status: StepStatusSucceeded},
			{Name: "Second", Status: StepStatusWaiting},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewStepRunner([]*Step{first, second}, Snapshot{Steps: test.saved}); err == nil {
				t.Fatal("invalid saved graph was accepted")
			}
		})
	}
}

func TestStepRunnerRejectsChangedExecutionGraphWithoutChangingSavedView(t *testing.T) {
	called := false
	step := NewRootStep("Reviewed", time.Minute, func(context.Context) error {
		called = true
		return nil
	})
	runner, err := NewStepRunner([]*Step{step}, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	before := runner.Snapshot()
	changed := NewRootStep("Changed", time.Minute, step.Func)
	if err := runner.ValidateSteps([]*Step{changed}); err == nil {
		t.Fatal("changed graph passed validation")
	}
	if err := runner.Execute(context.Background(), []*Step{changed}); err == nil {
		t.Fatal("changed graph executed")
	}
	if called || !reflect.DeepEqual(before, runner.Snapshot()) {
		t.Fatal("rejected execution changed the saved view or ran a callback")
	}
	if err := runner.ValidateSteps([]*Step{step}); err != nil {
		t.Fatal(err)
	}
}

func TestStepRunner_Execute_Cancel(t *testing.T) {
	// Test that when a step fails, steps that depend on it don't enter their impls.
	a := NewRootStep(
		"Failure", NoTimeout,
		func(ctx context.Context) error {
			return fmt.Errorf("intentional failure")
		},
	).Then(
		"Dependent", NoTimeout,
		func(ctx context.Context) error {
			t.Fatal("dependent step ran")
			return nil
		})
	if err := execute(t, a); err == nil {
		t.Fatal("expected error")
	}
}

func TestStepRunner_Execute_PanicToError(t *testing.T) {
	// Test that when a step panics, it is treated as an error.
	a := NewRootStep(
		"Panic", NoTimeout,
		func(ctx context.Context) error {
			panic("intentional panic")
		},
	)
	if err := execute(t, a); err != nil {
		if !errors.Is(err, errStepPanic) {
			t.Fatalf("expected errStepPanic err, got: %v", err)
		}
	} else {
		t.Fatal("expected error")
	}
}

func TestStepRunnerFinalizesBeforeReportingCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	finalErr := errors.New("outcome could not be saved")
	step := NewRootStep("finish", NoTimeout, func(context.Context) error { return nil })
	var runner StepRunner
	done := make(chan error, 1)
	go func() {
		done <- runner.ExecuteWithFinalizer(context.Background(), []*Step{step}, func(error) error {
			close(entered)
			<-release
			return finalErr
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("finalizer did not run")
	}
	if !runner.Snapshot().Active {
		close(release)
		t.Fatal("runner reported completion before finalization")
	}
	close(release)
	if err := <-done; !errors.Is(err, finalErr) {
		t.Fatalf("execution error = %v", err)
	}
	if snapshot := runner.Snapshot(); snapshot.Active || snapshot.Error != finalErr.Error() {
		t.Fatalf("final snapshot = %#v", snapshot)
	}
}

func TestStepRunnerSnapshots(t *testing.T) {
	releaseStep := make(chan struct{})
	step := NewRootStep("Controlled step", NoTimeout, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseStep:
			return nil
		}
	})

	var runner StepRunner
	initial, updates, unsubscribe := runner.Subscribe(16)
	defer unsubscribe()
	if initial.Active || len(initial.Steps) != 0 {
		t.Fatalf("unexpected initial snapshot: %#v", initial)
	}

	done := make(chan error, 1)
	go func() {
		done <- runner.Execute(context.Background(), []*Step{step})
	}()

	waitForSnapshotStatus(t, updates, StepStatusRunning)
	snapshot := runner.Snapshot()
	if !snapshot.Active || len(snapshot.Steps) != 1 {
		t.Fatalf("unexpected running snapshot: %#v", snapshot)
	}

	close(releaseStep)
	waitForSnapshotStatus(t, updates, StepStatusSucceeded)
	if err := <-done; err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	snapshot = runner.Snapshot()
	if snapshot.Active {
		t.Fatalf("unexpected final snapshot: %#v", snapshot)
	}
}

func TestStepRunnerProgressSnapshots(t *testing.T) {
	reported := make(chan struct{})
	releaseStep := make(chan struct{})
	step := NewRootStep("Progress step", NoTimeout, func(ctx context.Context) error {
		ReportProgress(ctx, StepProgress{
			Summary:   "Running one pipeline task",
			Detail:    "1/3 stages complete",
			Completed: 1,
			Total:     3,
		})
		close(reported)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseStep:
			return nil
		}
	})

	var runner StepRunner
	_, updates, unsubscribe := runner.Subscribe(16)
	defer unsubscribe()
	done := make(chan error, 1)
	go func() {
		done <- runner.Execute(context.Background(), []*Step{step})
	}()

	select {
	case <-reported:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for progress report")
	}
	snapshot := runner.Snapshot()
	if len(snapshot.Steps) != 1 || snapshot.Steps[0].Progress == nil {
		t.Fatalf("snapshot has no progress: %#v", snapshot)
	}
	progress := snapshot.Steps[0].Progress
	if progress.Summary != "Running one pipeline task" || progress.Detail != "1/3 stages complete" ||
		progress.Completed != 1 || progress.Total != 3 {

		t.Fatalf("progress = %#v", progress)
	}

	sawProgressUpdate := false
	for !sawProgressUpdate {
		select {
		case update := <-updates:
			for _, candidate := range update.Steps {
				sawProgressUpdate = candidate.Progress != nil
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for progress snapshot")
		}
	}
	close(releaseStep)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForSnapshotStatus(t *testing.T, updates <-chan Snapshot, want StepStatus) Snapshot {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case snapshot, ok := <-updates:
			if !ok {
				t.Fatal("snapshot subscription closed unexpectedly")
			}
			for _, step := range snapshot.Steps {
				if step.Status == want {
					return snapshot
				}
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for step status %q", want)
		}
	}
}
