// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func TestRunParallelProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		before := testSnapshot(t, run)
		view, plan := run.TakeView(), run.Plan()
		checkpoints := 0
		start := time.Now()
		runner, done := startTestRun(t, context.Background(), run, func() error {
			checkpoints++
			return nil
		})
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 0, 0)
		time.Sleep(5 * workDelay)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 5, 5)
		runner.ReadState(func() {
			if !reflect.DeepEqual(before, testSnapshot(t, run)) || checkpoints != 0 {
				t.Fatal("transient progress changed durable state")
			}
		})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != workUnits*workDelay {
			t.Fatalf("elapsed = %v, want parallel duration %v", elapsed, workUnits*workDelay)
		}
		if state := testState(t, run); !slices.Equal(state.Determinants, []float64{2, 2.25}) || state.Number != 2.5 || checkpoints != 3 {
			t.Fatalf("completed state = %#v, checkpoints = %d", state, checkpoints)
		}
		for _, step := range runner.Snapshot().Steps {
			if step.Status != coordinator.StepStatusSucceeded || step.Progress != nil && step.Progress.Completed != workUnits {
				t.Fatalf("step = %#v", step)
			}
		}
		if view.Summary != "Ready" || !reflect.DeepEqual(plan, run.Plan()) {
			t.Fatal("execution changed an earlier view or plan")
		}
		if view := run.TakeView(); !view.Test || view.RunURL != "" || view.Summary != "The number is 2.500000, an estimate of e" {
			t.Fatalf("completed view = %#v", view)
		}
	})
}

func TestRunResumesCompletedDimensions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process := NewProcess().Processes()[0]
		run := prepareTestRun(t, process)
		run.state.Determinants[0] = 2
		saved := testSnapshot(t, run)
		if err := process.Validate(saved); err != nil {
			t.Fatal(err)
		}
		restored, err := process.Load(saved)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		checkpoints := 0
		runner, done := startTestRun(t, context.Background(), restored, func() error {
			checkpoints++
			return nil
		})
		synctest.Wait()
		graph := runner.Snapshot()
		if graph.Steps[0].Status != coordinator.StepStatusSucceeded ||
			graph.Steps[0].Progress.Completed != workUnits || graph.Steps[1].Progress.Completed != 0 {
			t.Fatalf("restored progress = %#v", graph)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != workUnits*workDelay || checkpoints != 2 ||
			!slices.Equal(testState(t, restored).Determinants, []float64{2, 2.25}) {
			t.Fatalf("resume duration=%v, checkpoints=%d, state=%#v", time.Since(start), checkpoints, testState(t, restored))
		}
		completed, err := process.Load(testSnapshot(t, restored))
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		_, repeated := startTestRun(t, context.Background(), completed, func() error {
			t.Error("completed work checkpointed again")
			return nil
		})
		if err := <-repeated; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != 0 {
			t.Fatal("completed dimensions repeated their simulated work")
		}
	})
}

func TestCancellationDiscardsTransientProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process := NewProcess().Processes()[0]
		run := prepareTestRun(t, process)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runner, done := startTestRun(t, ctx, run, func() error {
			t.Error("unfinished dimension was checkpointed")
			return nil
		})
		synctest.Wait()
		time.Sleep(5 * workDelay)
		synctest.Wait()
		assertParallelProgress(t, runner.Snapshot(), 5, 5)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v", err)
		}
		if state := testState(t, run); !slices.Equal(state.Determinants, []float64{0, 0}) {
			t.Fatalf("state after cancellation = %#v", state)
		}
		restored, err := process.Load(testSnapshot(t, run))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, resumed := startTestRun(t, context.Background(), restored, func() error { return nil })
		if err := <-resumed; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != workUnits*workDelay {
			t.Fatal("unfinished dimensions did not restart from zero")
		}
	})
}

func TestRunsExecuteIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		processes := NewProcess().Processes()
		first := prepareTestRun(t, processes[1])
		second := prepareTestRun(t, processes[0])
		_, firstDone := startTestRun(t, context.Background(), first, func() error { return nil })
		_, secondDone := startTestRun(t, context.Background(), second, func() error { return nil })
		if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "sabotaged dimension 1") {
			t.Fatalf("first run error = %v", err)
		}
		interrupted := testSnapshot(t, first)
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(interrupted, testSnapshot(t, first)) ||
			!slices.Equal(testState(t, first).Determinants, []float64{0, 0}) ||
			!slices.Equal(testState(t, second).Determinants, []float64{2, 2.25}) {
			t.Fatal("one run affected the other's results")
		}
	})
}

func TestStorageFailureDoesNotStopDemo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := prepareTestRun(t, NewProcess().Processes()[0])
		failure := errors.New("persistence failed")
		_, done := startTestRun(t, context.Background(), run, func() error { return failure })
		if err := <-done; !errors.Is(err, failure) {
			t.Fatalf("execution error = %v, want persistence failure", err)
		}
		if state := testState(t, run); !slices.Equal(state.Determinants, []float64{2, 2.25}) || state.Number != 2.5 {
			t.Fatalf("storage failure stopped the demo: %#v", state)
		}
	})
}

func TestRunDimensionCountControlsGraph(t *testing.T) {
	for _, test := range []struct {
		dimensions int
		result     float64
		estimate   float64
	}{
		{1, 2, 2},
		{3, 64.0 / 27, 2.5555555555555556},
		{5, 2.48832, 2.6148824999999993},
		{50, 2.691588029073608, 2.713023058030004},
	} {
		t.Run(fmt.Sprint(test.dimensions), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				process := NewProcess().Processes()[0]
				saved, err := process.Prepare(context.Background(), &input{Dimensions: test.dimensions})
				if err != nil {
					t.Fatal(err)
				}
				run, err := process.Load(saved)
				if err != nil {
					t.Fatal(err)
				}
				checkpoints := 0
				runner, done := startTestRun(t, context.Background(), run, func() error {
					checkpoints++
					return process.Validate(testSnapshot(t, run))
				})
				synctest.Wait()
				assertParallelProgress(t, runner.Snapshot(), make([]int, test.dimensions)...)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				state := testState(t, run)
				if len(state.Determinants) != test.dimensions || checkpoints != test.dimensions+1 ||
					math.Abs(state.Determinants[test.dimensions-1]-test.result) > 1e-12 {
					t.Fatalf("determinants=%v, checkpoints=%d", state.Determinants, checkpoints)
				}
				for dimension, result := range state.Determinants {
					if result >= math.E || dimension > 0 && result <= state.Determinants[dimension-1] {
						t.Fatalf("determinants do not approach e from below: %v", state.Determinants)
					}
				}
				if math.Abs(state.Number-test.estimate) > 1e-12 ||
					test.dimensions > 1 && math.Abs(state.Number-math.E) >= math.Abs(test.result-math.E) {
					t.Fatalf("estimate = %v, want %v and closer to e than %v", state.Number, test.estimate, test.result)
				}
				if view := run.TakeView(); view.Summary != fmt.Sprintf("The number is %.6f, an estimate of e", test.estimate) {
					t.Fatalf("calculated result = %#v", view)
				}
			})
		})
	}
}

func TestEstimateNumberUsesAllDimensions(t *testing.T) {
	results := []float64{4, 3.5, 10.0 / 3, 3.25}
	if got := estimateNumber(results); math.Abs(got-3) > 1e-12 {
		t.Fatalf("fitted slope = %v, want 3", got)
	}
	for dimension := range results {
		changed := slices.Clone(results)
		changed[dimension]++
		if estimateNumber(changed) == estimateNumber(results) {
			t.Fatalf("dimension %d did not affect the fitted slope", dimension+1)
		}
	}
}

func TestRunCalculatesNumberFromSavedDeterminants(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process := NewProcess().Processes()[0]
		run := prepareTestRun(t, process)
		run.state.Determinants = []float64{2, 2.25}
		restored, err := process.Load(testSnapshot(t, run))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		checkpoints := 0
		_, done := startTestRun(t, context.Background(), restored, func() error {
			checkpoints++
			return process.Validate(testSnapshot(t, restored))
		})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != 0 || checkpoints != 1 || testState(t, restored).Number != 2.5 {
			t.Fatalf("duration=%v, checkpoints=%d, state=%#v", time.Since(start), checkpoints, testState(t, restored))
		}
	})
}

func TestRebelliousRunRepeatsUnfinishedAnalysisWhenResumed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		process := NewProcess().Processes()[1]
		demoRun := prepareTestRun(t, process)
		for attempt := range 2 {
			start := time.Now()
			_, done := startTestRun(t, context.Background(), demoRun, func() error {
				t.Error("sabotaged analysis saved a result")
				return nil
			})
			if err := <-done; err == nil || !strings.Contains(err.Error(), "sabotaged dimension 1") {
				t.Fatalf("attempt %d error = %v", attempt, err)
			}
			if time.Since(start) != workUnits/2*workDelay || !slices.Equal(demoRun.state.Determinants, []float64{0, 0}) {
				t.Fatalf("attempt %d duration=%v, state=%#v", attempt, time.Since(start), demoRun.state)
			}
			restored, err := process.Load(testSnapshot(t, demoRun))
			if err != nil {
				t.Fatal(err)
			}
			demoRun = restored.(*run)
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
			Update: func() (func() error, error) { return checkpoint, nil },
		})
	}()
	return runner, done
}

func assertParallelProgress(t *testing.T, snapshot coordinator.Snapshot, completed ...int) {
	t.Helper()
	if !snapshot.Active || len(snapshot.Steps) != len(completed)+1 {
		t.Fatalf("execution snapshot = %#v", snapshot)
	}
	for dimension, count := range completed {
		step := snapshot.Steps[dimension]
		if step.Name != fmt.Sprintf("Analyze dimension %d", dimension+1) ||
			len(step.DependsOn) != 0 || step.Status != coordinator.StepStatusRunning ||
			step.Progress == nil || step.Progress.Completed != count || step.Progress.Total != workUnits {
			t.Fatalf("parallel analysis = %#v, want %d seconds elapsed", step, count)
		}
	}
	finish := snapshot.Steps[len(completed)]
	if finish.Name != "Calculate the number" || finish.Status != coordinator.StepStatusWaiting ||
		len(finish.DependsOn) != len(completed) {
		t.Fatalf("final calculation = %#v", finish)
	}
}
