// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func TestRunParallelProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		before := run.TakeSnapshot()
		view := run.TakeView()
		plan := run.Plan()
		var checkpoints atomic.Int32
		checkpoint := func() {
			run.TakeSnapshot()
			run.TakeView()
			checkpoints.Add(1)
		}
		start := time.Now()
		runner, done := startTestRun(t, context.Background(), run, checkpoint)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 0, 0)
		time.Sleep(checkpointUnits * workDelay)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), checkpointUnits, checkpointUnits)
		if state := testState(t, run); !state.Initialized || state.Finished ||
			state.WorkCompleted != [2]int{checkpointUnits, checkpointUnits} {

			t.Fatalf("in-progress state = %#v", state)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != workUnits*workDelay {
			t.Fatalf("elapsed = %v, want parallel duration %v", elapsed, workUnits*workDelay)
		}
		if state := testState(t, run); !state.Finished ||
			state.WorkCompleted != [2]int{workUnits, workUnits} {

			t.Fatalf("completed state = %#v", state)
		}
		for _, step := range runner.Snapshot().Steps {
			if step.Status != coordinator.StepStatusSucceeded {
				t.Fatalf("step = %#v", step)
			}
		}
		if got, want := checkpoints.Load(), int32(2+2*workUnits/checkpointUnits); got != want {
			t.Fatalf("checkpoints = %d, want %d", got, want)
		}
		if view.Summary != "Ready" || string(before.State) == string(run.TakeSnapshot().State) ||
			!reflect.DeepEqual(plan, run.Plan()) {

			t.Fatal("execution changed an earlier view or plan, or did not update state")
		}
		if view := run.TakeView(); !view.Test || view.RunURL != "" || view.Summary != "Demo completed" {
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
		var mu sync.Mutex
		var saved *contract.StateSnapshot
		checkpoint := func() {
			mu.Lock()
			defer mu.Unlock()
			snapshot := run.TakeSnapshot()
			state, err := decodeStrictJSON[state](snapshot.State)
			if err != nil {
				t.Error(err)
				cancel()
				return
			}
			if saved == nil && state.WorkCompleted[0] >= checkpointUnits && state.WorkCompleted[1] >= checkpointUnits {
				saved = snapshot
				cancel()
			}
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
		if before.Finished {
			t.Fatal("interrupted run was already finished")
		}
		start := time.Now()
		runner, done := startTestRun(t, context.Background(), restored, func() {
			restored.TakeSnapshot()
			restored.TakeView()
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
		if state := testState(t, restored); !state.Finished {
			t.Fatalf("resumed state = %#v", state)
		}
		completed, err := process.Load(restored.TakeSnapshot())
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		var repeatedCheckpoints atomic.Int32
		_, repeated := startTestRun(t, context.Background(), completed, func() {
			repeatedCheckpoints.Add(1)
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
		firstRunner, firstDone := startTestRun(t, ctx, first, func() { first.TakeSnapshot() })
		secondRunner, secondDone := startTestRun(t, context.Background(), second, func() { second.TakeSnapshot() })
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
		interrupted := first.TakeSnapshot()
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(interrupted, first.TakeSnapshot()) ||
			testState(t, first).Finished || !testState(t, second).Finished {

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
		_, done := startTestRun(t, ctx, run, func() {
			run.TakeSnapshot()
			run.TakeView()
			checkpoints.Add(1)
			cancel()
		})
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v", err)
		}
		if state := testState(t, run); !state.Initialized || state.Finished ||
			state.WorkCompleted != [2]int{} || checkpoints.Load() != 1 {

			t.Fatalf("state after checkpoint cancellation = %#v, checkpoints = %d", state, checkpoints.Load())
		}
	})
}

func TestUpdateReleasesStateLockBeforeCheckpoint(t *testing.T) {
	run := prepareTestRun(t, NewProcess().Processes()[0])
	checkpoints := 0
	err := run.update(context.Background(), func() {
		if !run.mu.TryLock() {
			t.Fatal("checkpoint was called while the state lock was held")
		}
		run.mu.Unlock()
		run.TakeSnapshot()
		run.TakeView()
		checkpoints++
	}, func(state *state) {
		state.Initialized = true
	})
	if err != nil || checkpoints != 1 || !testState(t, run).Initialized {
		t.Fatalf("update error = %v, checkpoints = %d, state = %#v", err, checkpoints, testState(t, run))
	}
}

func TestUpdateHonorsCancellation(t *testing.T) {
	run := prepareTestRun(t, NewProcess().Processes()[0])
	before := run.TakeSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run.update(ctx, func() { t.Fatal("canceled update checkpointed") }, func(*state) {
		t.Fatal("canceled update changed state")
	})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(before, run.TakeSnapshot()) {
		t.Fatalf("canceled update error = %v, state = %#v", err, testState(t, run))
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err = run.update(ctx, func() { cancel() }, func(state *state) { state.Initialized = true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint cancellation error = %v", err)
	}
}

func startTestRun(
	t *testing.T,
	ctx context.Context,
	run contract.Run,
	checkpoint contract.CheckpointFunc,
) (*coordinator.StepRunner, <-chan error) {
	t.Helper()
	steps, err := run.Build(ctx, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	runner := new(coordinator.StepRunner)
	done := make(chan error, 1)
	go func() { done <- runner.Execute(ctx, steps) }()
	return runner, done
}

func assertParallelProgress(t *testing.T, snapshot coordinator.Snapshot, completedA, completedB int) {
	t.Helper()
	if !snapshot.Active || len(snapshot.Steps) != 4 {
		t.Fatalf("execution snapshot = %#v", snapshot)
	}
	for _, step := range snapshot.Steps {
		switch step.Name {
		case "Initialize demo":
			if step.Status != coordinator.StepStatusSucceeded {
				t.Fatalf("initialization = %#v", step)
			}
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
