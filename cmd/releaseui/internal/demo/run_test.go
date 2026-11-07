// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

func TestRunParallelProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		before := testSnapshot(t, run)
		view := run.TakeView()
		plan := run.Plan()
		var checkpoints atomic.Int32
		checkpoint := func() error {
			if _, err := run.TakeSnapshot(); err != nil {
				return err
			}
			run.TakeView()
			checkpoints.Add(1)
			return nil
		}
		start := time.Now()
		runner, done := startTestRun(t, context.Background(), run, checkpoint)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 0, 0)
		time.Sleep(checkpointUnits * workDelay)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), checkpointUnits, checkpointUnits)
		runner.ReadState(func() {
			if state := testState(t, run); state.WorkCompleted != [2]int{checkpointUnits, checkpointUnits} {

				t.Fatalf("in-progress state = %#v", state)
			}
		})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != workUnits*workDelay {
			t.Fatalf("elapsed = %v, want parallel duration %v", elapsed, workUnits*workDelay)
		}
		if state := testState(t, run); state.WorkCompleted != [2]int{workUnits, workUnits} {

			t.Fatalf("completed state = %#v", state)
		}
		for _, step := range runner.Snapshot().Steps {
			if step.Status != coordinator.StepStatusSucceeded {
				t.Fatalf("step = %#v", step)
			}
		}
		if got, want := checkpoints.Load(), int32(2*workUnits/checkpointUnits); got != want {
			t.Fatalf("checkpoints = %d, want %d", got, want)
		}
		if view.Summary != "Ready" || string(before.State) == string(testSnapshot(t, run).State) ||
			!reflect.DeepEqual(plan, run.Plan()) {

			t.Fatal("execution changed an earlier view or plan, or did not update state")
		}
		if view := run.TakeView(); !view.Test || view.RunURL != "" || view.Summary != "Simulated work completed" {
			t.Fatalf("completed view = %#v", view)
		}
	})
}

func TestRunResumesCheckpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process := NewProcess().Processes()[0]
		run := prepareTestRun(t, process)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var saved *contract.StateSnapshot
		checkpoint := func() error {
			savedSnapshot, err := run.TakeSnapshot()
			if err != nil {
				return err
			}
			_, state, err := snapshot.Decode[struct{}, state](savedSnapshot)
			if err != nil {
				t.Error(err)
				cancel()
				return err
			}
			if saved == nil && state.WorkCompleted[0] >= checkpointUnits && state.WorkCompleted[1] >= checkpointUnits {
				saved = savedSnapshot
				cancel()
			}
			return nil
		}
		_, interrupted := startTestRun(t, ctx, run, checkpoint)
		if err := <-interrupted; !errors.Is(err, context.Canceled) {
			t.Fatalf("interruption error = %v", err)
		}
		if saved == nil {
			t.Fatal("no checkpoint was saved")
		}
		restored, err := process.Load(saved)
		if err != nil {
			t.Fatal(err)
		}
		before := testState(t, restored)
		if before.WorkCompleted == [2]int{workUnits, workUnits} {
			t.Fatal("interrupted run was already finished")
		}
		start := time.Now()
		runner, done := startTestRun(t, context.Background(), restored, func() error {
			if _, err := restored.TakeSnapshot(); err != nil {
				return err
			}
			restored.TakeView()
			return nil
		})
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), before.WorkCompleted[0], before.WorkCompleted[1])
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		remaining := max(workUnits-before.WorkCompleted[0], workUnits-before.WorkCompleted[1])
		if elapsed := time.Since(start); elapsed != time.Duration(remaining)*workDelay {
			t.Fatalf("resume duration = %v, want %v", elapsed, time.Duration(remaining)*workDelay)
		}
		if state := testState(t, restored); state.WorkCompleted != [2]int{workUnits, workUnits} {
			t.Fatalf("resumed state = %#v", state)
		}
		completed, err := process.Load(testSnapshot(t, restored))
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		var repeatedCheckpoints atomic.Int32
		_, repeated := startTestRun(t, context.Background(), completed, func() error {
			repeatedCheckpoints.Add(1)
			return nil
		})
		if err := <-repeated; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 || repeatedCheckpoints.Load() != 0 {
			t.Fatalf("completed work repeated: elapsed = %v, checkpoints = %d", elapsed, repeatedCheckpoints.Load())
		}
	})
}

func TestRunsExecuteIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		processes := NewProcess().Processes()
		first := prepareTestRun(t, processes[0])
		second := prepareTestRun(t, processes[1])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstRunner, firstDone := startTestRun(t, ctx, first, func() error { _, err := first.TakeSnapshot(); return err })
		secondRunner, secondDone := startTestRun(t, context.Background(), second, func() error { _, err := second.TakeSnapshot(); return err })
		synctest.Wait()
		assertParallelProgress(t, firstRunner.Snapshot(), 0, 0)
		assertParallelProgress(t, secondRunner.Snapshot(), 0, 0)
		time.Sleep(checkpointUnits * workDelay)
		synctest.Wait()
		assertParallelProgress(t, firstRunner.Snapshot(), checkpointUnits, checkpointUnits)
		assertParallelProgress(t, secondRunner.Snapshot(), checkpointUnits, checkpointUnits)
		cancel()
		if err := <-firstDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("first run error = %v", err)
		}
		interrupted := testSnapshot(t, first)
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(interrupted, testSnapshot(t, first)) ||
			testState(t, first).WorkCompleted == [2]int{workUnits, workUnits} ||
			testState(t, second).WorkCompleted != [2]int{workUnits, workUnits} {

			t.Fatal("finishing the second run changed the interrupted first run")
		}
		if reflect.DeepEqual(first.Plan(), second.Plan()) {
			t.Fatal("demo identities were mixed")
		}
	})
}

func TestCheckpointCancellationStopsExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var checkpoints atomic.Int32
		_, done := startTestRun(t, ctx, run, func() error {
			if _, err := run.TakeSnapshot(); err != nil {
				return err
			}
			run.TakeView()
			checkpoints.Add(1)
			cancel()
			return nil
		})
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v", err)
		}
		state := testState(t, run)
		completed := state.WorkCompleted[0] + state.WorkCompleted[1]
		if completed != checkpointUnits || checkpoints.Load() != 1 {

			t.Fatalf("state after checkpoint cancellation = %#v, checkpoints = %d", state, checkpoints.Load())
		}
	})
}

func TestCancellationDuringWorkWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var checkpoints atomic.Int32
		runner, done := startTestRun(t, ctx, run, func() error { checkpoints.Add(1); return nil })
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 0, 0)
		time.Sleep(workDelay / 2)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v", err)
		}
		if state := testState(t, run); state.WorkCompleted != [2]int{} ||
			checkpoints.Load() != 0 {

			t.Fatalf("state after cancellation = %#v, checkpoints = %d", state, checkpoints.Load())
		}
	})
}

func TestUpdateFailureStopsDemo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		failure := errors.New("persistence failed")
		_, done := startTestRun(t, context.Background(), run, func() error { return failure })
		if err := <-done; !errors.Is(err, failure) {
			t.Fatalf("execution error = %v, want persistence failure", err)
		}
		state := testState(t, run)
		if completed := state.WorkCompleted[0] + state.WorkCompleted[1]; completed != checkpointUnits {
			t.Fatalf("demo continued after persistence failed: %#v", state)
		}
	})
}

func startTestRun(
	t *testing.T,
	ctx context.Context,
	run contract.Run,
	checkpoint func() error,
) (*coordinator.StepRunner, <-chan error) {
	t.Helper()
	steps, err := run.Build()
	if err != nil {
		t.Fatal(err)
	}
	runner := new(coordinator.StepRunner)
	done := make(chan error, 1)
	go func() {
		done <- runner.ExecuteWithCallbacks(ctx, steps, coordinator.ExecutionCallbacks{
			Update: func() (func() error, error) { return nil, checkpoint() },
		})
	}()
	return runner, done
}

func assertParallelProgress(t *testing.T, snapshot coordinator.Snapshot, completedA, completedB int) {
	t.Helper()
	if !snapshot.Active || len(snapshot.Steps) != 3 {
		t.Fatalf("execution snapshot = %#v", snapshot)
	}
	for _, step := range snapshot.Steps {
		switch step.Name {
		case "Fake work A", "Fake work B":
			completed := completedA
			if step.Name == "Fake work B" {
				completed = completedB
			}
			if step.Status != coordinator.StepStatusRunning || step.Progress == nil ||
				step.Progress.Completed != completed || step.Progress.Total != workUnits {

				t.Fatalf("parallel work = %#v, want %d completed units", step, completed)
			}
		case "Finish demo":
			if step.Status != coordinator.StepStatusWaiting {
				t.Fatalf("finish ran before its dependencies = %#v", step)
			}
		default:
			t.Fatalf("unexpected step = %#v", step)
		}
	}
}
