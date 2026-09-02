// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
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
