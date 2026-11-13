// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
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

func TestNewStepRunnerInitializesGraphWithoutExecuting(t *testing.T) {
	executions := 0
	action := func(context.Context, StepControl) error {
		executions++
		return nil
	}
	first := NewRootStep("Initialize", time.Minute, action)
	second := first.Then("Work", time.Minute, action)
	runner, err := NewStepRunner([]*Step{first, second})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runner.Snapshot()
	if executions != 0 || snapshot.Active || snapshot.Error != "" {
		t.Fatalf("view = %#v, executions = %d", snapshot, executions)
	}
	if snapshot.Steps[0].Status != StepStatusWaiting || snapshot.Steps[1].DependsOn[0] != "Initialize" {
		t.Fatalf("initial graph = %#v", snapshot)
	}
	if err := runner.Execute(context.Background(), []*Step{first, second}); err != nil {
		t.Fatal(err)
	}
	if executions != 2 {
		t.Fatalf("explicit execution calls = %d, want 2", executions)
	}
}

func TestSnapshotRejectsInvalidGraph(t *testing.T) {
	for _, test := range []struct {
		name  string
		saved []StepSnapshot
	}{
		{name: "duplicate name", saved: []StepSnapshot{
			{Name: "First", Status: StepStatusSucceeded}, {Name: "First", Status: StepStatusWaiting},
		}},
		{name: "unknown status", saved: []StepSnapshot{
			{Name: "First", Status: "guess"}, {Name: "Second", DependsOn: []string{"First"}, Status: StepStatusWaiting},
		}},
		{name: "unordered dependency", saved: []StepSnapshot{
			{Name: "First", DependsOn: []string{"Second"}, Status: StepStatusSucceeded},
			{Name: "Second", Status: StepStatusWaiting},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := (Snapshot{Steps: test.saved}).Validate(); err == nil {
				t.Fatal("invalid saved graph was accepted")
			}
		})
	}
}

func TestStepRunnerRejectsChangedExecutionGraphWithoutChangingSavedView(t *testing.T) {
	called := false
	step := NewRootStep("Reviewed", time.Minute, func(context.Context, StepControl) error {
		called = true
		return nil
	})
	runner, err := NewStepRunner([]*Step{step})
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
		func(ctx context.Context, _ StepControl) error {
			return fmt.Errorf("intentional failure")
		},
	).Then(
		"Dependent", NoTimeout,
		func(ctx context.Context, _ StepControl) error {
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
		func(ctx context.Context, _ StepControl) error {
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
	step := NewRootStep("finish", NoTimeout, func(context.Context, StepControl) error { return nil })
	var runner StepRunner
	done := make(chan error, 1)
	go func() {
		done <- runner.ExecuteWithCallbacks(context.Background(), []*Step{step}, ExecutionCallbacks{
			Finalize: func(error) error {
				close(entered)
				<-release
				return finalErr
			},
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
	step := NewRootStep("Controlled step", NoTimeout, func(ctx context.Context, _ StepControl) error {
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
	step := NewRootStep("Progress step", NoTimeout, func(ctx context.Context, ctl StepControl) error {
		ctl.ReportProgress(StepProgress{
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

func TestStepControlsShareRunLock(t *testing.T) {
	var runner StepRunner
	count := 0
	work := func(_ context.Context, ctl StepControl) error {
		for range 10 {
			ctl.Update(func() {
				current := count
				time.Sleep(time.Millisecond)
				count = current + 1
				ctl.ReportProgress(StepProgress{Completed: count, Total: 20})
				runner.Snapshot()
			})
		}
		return nil
	}
	a := NewRootStep("A", NoTimeout, work)
	b := NewRootStep("B", NoTimeout, work)
	join := All(a, b).Then("Join", NoTimeout, func(_ context.Context, ctl StepControl) error {
		var result int
		ctl.Read(func() { result = count })
		if result != 20 {
			return fmt.Errorf("count = %d, want 20", result)
		}
		return nil
	})
	steps, err := join.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
}

func TestStepControlRunLocksAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locked := make(chan struct{})
		release := make(chan struct{})
		first := NewRootStep("First", NoTimeout, func(_ context.Context, ctl StepControl) error {
			ctl.Update(func() {
				close(locked)
				<-release
			})
			return nil
		})
		var firstRunner, secondRunner StepRunner
		done := make(chan error, 1)
		go func() { done <- firstRunner.Execute(context.Background(), []*Step{first}) }()
		<-locked

		called := false
		second := NewRootStep("Second", NoTimeout, func(_ context.Context, ctl StepControl) error {
			ctl.Update(func() { called = true })
			return nil
		})
		err := secondRunner.Execute(context.Background(), []*Step{second})
		close(release)
		if firstErr := <-done; firstErr != nil {
			t.Fatal(firstErr)
		}
		if err != nil || !called {
			t.Fatalf("second run error = %v, callback called = %v", err, called)
		}
	})
}

func TestCaptureFailureCancelsAndSurvivesNilStepReturn(t *testing.T) {
	failure := errors.New("capture failed")
	mutations, captures := 0, 0
	step := NewRootStep("Work", NoTimeout, func(ctx context.Context, ctl StepControl) error {
		ctl.Update(func() { mutations++ })
		if err := ctl.EnsurePersisted(); !errors.Is(err, failure) {
			t.Errorf("EnsurePersisted error=%v, want %v", err, failure)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Error("Update returned before canceling the step")
		}
		ctl.Update(func() { mutations++ })
		if err := ctl.EnsurePersisted(); err == nil {
			t.Error("durability barrier ignored the capture failure")
		}
		return nil
	})
	dependentRan := false
	dependent := step.Then("Dependent", NoTimeout, func(context.Context, StepControl) error {
		dependentRan = true
		return nil
	})
	steps, err := dependent.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	var runner StepRunner
	finalized := false
	err = runner.ExecuteWithCallbacks(context.Background(), steps, ExecutionCallbacks{
		Update: func() (func() error, error) {
			captures++
			return nil, failure
		},
		Finalize: func(err error) error {
			finalized = true
			if !errors.Is(err, failure) {
				t.Errorf("finalizer lost update failure: %v", err)
			}
			return nil
		},
	})
	if !errors.Is(err, failure) || mutations != 2 || captures != 1 || dependentRan || !finalized {
		t.Fatalf("error=%v, mutations=%d, captures=%d, dependent=%v, finalized=%v",
			err, mutations, captures, dependentRan, finalized)
	}
	if graph := runner.Snapshot(); graph.Error == "" || graph.Steps[0].Status == StepStatusSucceeded {
		t.Fatalf("failed update reported success: %#v", graph)
	}
}

func TestStepReadLocking(t *testing.T) {
	var runner StepRunner
	ctl := stepControl{runner: &runner, ctx: context.Background()}
	ctl.Read(func() {
		locked := runner.process.mu.TryLock()
		if locked {
			runner.process.mu.Unlock()
		}
		if locked != raceEnabled {
			t.Errorf("read permits a writer=%v, race build=%v", locked, raceEnabled)
		}
	})
	runner.ReadState(func() {
		if runner.process.mu.TryLock() {
			runner.process.mu.Unlock()
			t.Error("host read did not exclude writers")
		}
		if !runner.process.mu.TryRLock() {
			t.Error("host read excluded another reader")
		} else {
			runner.process.mu.RUnlock()
		}
	})
}

func TestStorageFailureDoesNotCancelAndEnsurePersistedRetries(t *testing.T) {
	var runner StepRunner
	failure := errors.New("storage unavailable")
	attempts := 0
	storageErr := failure
	value, saved := 0, 0
	step := NewRootStep("Work", NoTimeout, func(ctx context.Context, ctl StepControl) error {
		ctl.Update(func() { value = 1 })
		if ctx.Err() != nil || runner.Snapshot().CheckpointError == "" {
			t.Fatal("storage failure canceled execution or was hidden")
		}
		if err := ctl.EnsurePersisted(); !errors.Is(err, failure) {
			t.Fatalf("durability barrier error=%v", err)
		}
		if ctx.Err() != nil {
			t.Fatal("durability barrier canceled execution without the step returning")
		}
		ctl.Update(func() { value = 2 })
		storageErr = nil
		if err := ctl.EnsurePersisted(); err != nil {
			return err
		}
		if saved != 2 || runner.Snapshot().CheckpointError != "" {
			t.Fatalf("latest state not saved or warning not cleared: saved=%d", saved)
		}
		before := attempts
		if err := ctl.EnsurePersisted(); err != nil || attempts != before {
			t.Fatal("already-durable state was saved again")
		}
		return nil
	})
	err := runner.ExecuteWithCallbacks(context.Background(), []*Step{step}, ExecutionCallbacks{
		Update: func() (func() error, error) {
			captured := value
			return func() error {
				attempts++
				if storageErr != nil {
					return storageErr
				}
				saved = captured
				return nil
			}, nil
		},
	})
	if err != nil || attempts != 4 {
		t.Fatalf("execution error=%v, attempts=%d", err, attempts)
	}
}

func TestLaterUpdateCanRecoverStorageFailure(t *testing.T) {
	var runner StepRunner
	attempts := 0
	continued := false
	step := NewRootStep("Work", NoTimeout, func(ctx context.Context, ctl StepControl) error {
		ctl.Update(func() {})
		if ctx.Err() != nil {
			t.Fatal("first save failure canceled execution")
		}
		ctl.Update(func() { continued = true })
		return nil
	})
	err := runner.ExecuteWithCallbacks(context.Background(), []*Step{step}, ExecutionCallbacks{
		Update: func() (func() error, error) {
			return func() error {
				attempts++
				if attempts == 1 {
					return errors.New("temporary outage")
				}
				return nil
			}, nil
		},
	})
	if err != nil || !continued || runner.Snapshot().CheckpointError != "" {
		t.Fatalf("later update did not recover: error=%v, continued=%v", err, continued)
	}
}

func TestStepRunnerReplacesCheckpointStateBetweenRuns(t *testing.T) {
	var runner StepRunner
	step := NewRootStep("Work", NoTimeout, func(_ context.Context, ctl StepControl) error {
		ctl.Update(func() {})
		return nil
	})
	failure := errors.New("storage unavailable")
	err := runner.ExecuteWithCallbacks(context.Background(), []*Step{step}, ExecutionCallbacks{
		Update: func() (func() error, error) {
			return func() error { return failure }, nil
		},
	})
	if !errors.Is(err, failure) || runner.Snapshot().CheckpointError != failure.Error() {
		t.Fatalf("first run error=%v, snapshot=%#v", err, runner.Snapshot())
	}

	if err := runner.ExecuteWithCallbacks(context.Background(), []*Step{step}, ExecutionCallbacks{
		Update: func() (func() error, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("second run error=%v", err)
	}
	if snapshot := runner.Snapshot(); snapshot.CheckpointError != "" || snapshot.Error != "" {
		t.Fatalf("second run retained checkpoint state: %#v", snapshot)
	}
}

func TestEnsurePersistedWaitsForEarlierUpdate(t *testing.T) {
	var runner StepRunner
	saving := make(chan struct{})
	release := make(chan struct{})
	barrierStarted := make(chan struct{})
	barrierDone := make(chan struct{})
	saved := false
	writer := NewRootStep("Writer", NoTimeout, func(_ context.Context, ctl StepControl) error {
		ctl.Update(func() {})
		return nil
	})
	reader := NewRootStep("Barrier", NoTimeout, func(_ context.Context, ctl StepControl) error {
		<-saving
		close(barrierStarted)
		err := ctl.EnsurePersisted()
		if !saved {
			t.Error("barrier returned before persistence")
		}
		close(barrierDone)
		return err
	})
	done := make(chan error, 1)
	go func() {
		done <- runner.ExecuteWithCallbacks(context.Background(), []*Step{writer, reader}, ExecutionCallbacks{
			Update: func() (func() error, error) {
				return func() error {
					close(saving)
					<-release
					saved = true
					return nil
				}, nil
			},
		})
	}()
	<-barrierStarted
	select {
	case <-barrierDone:
		t.Error("barrier did not wait")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStepReadCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprintf("during callback=%v", during), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !during {
				cancel()
			}
			var runner StepRunner
			ctl := stepControl{runner: &runner, ctx: ctx}
			called := false
			ctl.Read(func() {
				called = true
				cancel()
			})
			if !called {
				t.Fatal("canceled read did not call its callback")
			}
			if !runner.process.mu.TryLock() {
				t.Fatal("read left the lock held")
			}
			runner.process.mu.Unlock()
		})
	}
}

func TestStepReadLockReleasedAfterPanic(t *testing.T) {
	var runner StepRunner
	ctl := stepControl{runner: &runner, ctx: context.Background()}
	sentinel := errors.New("read callback panic")
	func() {
		defer func() {
			if got := recover(); got != sentinel {
				t.Errorf("panic=%v, want %v", got, sentinel)
			}
		}()
		ctl.Read(func() { panic(sentinel) })
	}()
	if !runner.process.mu.TryLock() {
		t.Fatal("read callback panic left the run lock held")
	}
	runner.process.mu.Unlock()
}

func TestStepControlRunLockReleasedAfterPanic(t *testing.T) {
	var runner StepRunner
	runner.process.beginExecution(nil, nil)
	ctl := stepControl{runner: &runner, ctx: context.Background()}
	sentinel := errors.New("callback panic")
	func() {
		defer func() {
			if got := recover(); got != sentinel {
				t.Errorf("panic = %v, want %v", got, sentinel)
			}
		}()
		ctl.Update(func() { panic(sentinel) })
	}()
	if !runner.process.mu.TryLock() {
		t.Fatal("callback panic left the run lock held")
	}
	runner.process.mu.Unlock()
	if !runner.process.updateMu.TryLock() {
		t.Fatal("callback panic left the update lock held")
	}
	runner.process.updateMu.Unlock()
}

func TestStepControlsReportParallelProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		expected := []StepProgress{
			{Summary: "First operation", Completed: 1, Total: 3, RunURL: "https://example.invalid/runs/1"},
			{Summary: "Second operation", Completed: 2, Total: 5, RunURL: "https://example.invalid/runs/2"},
		}
		var firstControl StepControl
		steps := make([]*Step, len(expected))
		for index, progress := range expected {
			steps[index] = NewRootStep(progress.Summary, NoTimeout, func(ctx context.Context, ctl StepControl) error {
				if ctl == nil {
					return errors.New("runner did not supply step control")
				}
				if index == 0 {
					firstControl = ctl
				}
				ctl.ReportProgress(progress)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}
		var runner StepRunner
		done := make(chan error, 1)
		go func() { done <- runner.Execute(ctx, steps) }()
		synctest.Wait()
		snapshot := runner.Snapshot()
		for index, step := range snapshot.Steps {
			if step.Status != StepStatusRunning || step.Progress == nil || *step.Progress != expected[index] {
				t.Fatalf("progress was lost or assigned to the wrong step: %#v", snapshot)
			}
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		final := runner.Snapshot()
		firstControl.ReportProgress(StepProgress{Summary: "Late report"})
		if !reflect.DeepEqual(final, runner.Snapshot()) {
			t.Fatal("late reporting changed a completed graph")
		}
	})
}

func TestStepControlPreservesCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{NoTimeout, time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := time.Now()
				mutated, saved := false, false
				var runner StepRunner
				step := NewRootStep("Controlled wait", timeout, func(ctx context.Context, ctl StepControl) error {
					deadline, hasDeadline := ctx.Deadline()
					if hasDeadline != (timeout != NoTimeout) || hasDeadline && !deadline.Equal(started.Add(timeout)) {
						return fmt.Errorf("unexpected deadline %s, present=%v", deadline, hasDeadline)
					}
					if ctl == nil {
						return errors.New("runner did not supply step control")
					}
					ctl.ReportProgress(StepProgress{Summary: "Waiting for cancellation"})
					<-ctx.Done()
					ctl.Update(func() {
						mutated = true
						if locked := runner.process.mu.TryLock(); locked {
							runner.process.mu.Unlock()
							t.Error("canceled Update did not hold the state lock")
						}
					})
					return ctx.Err()
				})
				done := make(chan error, 1)
				go func() {
					done <- runner.ExecuteWithCallbacks(ctx, []*Step{step}, ExecutionCallbacks{
						Update: func() (func() error, error) {
							if !mutated {
								t.Error("capture ran before the canceled callback")
							}
							return func() error { saved = true; return nil }, nil
						},
					})
				}()
				synctest.Wait()
				progress := runner.Snapshot().Steps[0].Progress
				if progress == nil || progress.Summary != "Waiting for cancellation" {
					t.Fatal("step control did not publish progress")
				}
				want := context.Canceled
				if timeout == NoTimeout {
					cancel()
				} else {
					want = context.DeadlineExceeded
					time.Sleep(timeout)
				}
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("execution error = %v, want %v", err, want)
				}
				if runner.Snapshot().Active {
					t.Fatal("canceled or timed-out step left the runner active")
				}
				if !mutated || !saved {
					t.Fatalf("Update after cancellation: mutated=%v, saved=%v", mutated, saved)
				}
			})
		})
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
