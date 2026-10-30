// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestMonitorSharesReadsWithoutRecordingOrExecutingActionsDuringObservation(t *testing.T) {
	type providerRun struct {
		status StepStatus
		raw    string
	}
	reads, records, actions := 0, 0, 0
	monitor, err := NewMonitor("Monitor", time.Minute,
		func(context.Context) (providerRun, error) {
			reads++
			if reads == 1 {
				return providerRun{StepStatusSucceeded, "success"}, nil
			}
			return providerRun{StepStatusFailed, "timed_out"}, nil
		},
		func(run providerRun) (*Observation, error) {
			observation := &Observation{
				Status: run.status, Progress: StepProgress{RunURL: "https://example.invalid/runs/1"},
			}
			if run.status == StepStatusFailed {
				observation.Error = run.raw
			}
			return observation, nil
		},
		func(_ context.Context, run providerRun) error {
			records++
			if run.raw != "timed_out" {
				t.Errorf("checkpoint did not receive the exact provider value: %#v", run)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	action := monitor.Then("Publish", time.Minute, func(context.Context) error {
		actions++
		return nil
	})
	steps, err := action.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewStepRunner(steps, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	_, updates, unsubscribe := runner.Subscribe(4)
	defer unsubscribe()
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	frame := <-updates
	if reads != 1 || records != 0 || actions != 0 || frame.Active ||
		frame.Steps[0].Status != StepStatusSucceeded || frame.Steps[0].ObservedAt.IsZero() ||
		frame.Steps[1].Status != StepStatusWaiting {

		t.Fatalf("read-only frame=%#v, reads=%d, records=%d, actions=%d", frame, reads, records, actions)
	}
	durable := runner.ExecutionSnapshot()
	if durable.Steps[0].Status != StepStatusWaiting || !durable.Steps[0].ObservedAt.IsZero() {
		t.Fatalf("observation changed durable execution progress: %#v", durable)
	}
	err = runner.Execute(context.Background(), steps)
	if err == nil || !strings.Contains(err.Error(), "timed_out") || reads != 2 || records != 1 || actions != 0 {
		t.Fatalf("execute error=%v, reads=%d, records=%d, actions=%d", err, reads, records, actions)
	}
	if graph := runner.Snapshot(); graph.Steps[0].Status != StepStatusFailed ||
		graph.Steps[1].Status != StepStatusBlocked || !graph.Steps[0].ObservedAt.IsZero() {

		t.Fatalf("observation overrode actual execution: %#v", graph)
	}
}

func TestMonitorPollsAndRecordsExactProviderValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		values := []StepStatus{StepStatusWaiting, StepStatusRunning, StepStatusSucceeded}
		var recorded []StepStatus
		reads := 0
		started := time.Now()
		step, err := NewMonitor("Monitor", time.Minute,
			func(context.Context) (StepStatus, error) {
				value := values[reads]
				if elapsed := time.Since(started); elapsed != time.Duration(reads)*monitorInterval {
					t.Errorf("read %d happened after %s", reads, elapsed)
				}
				reads++
				return value, nil
			},
			func(status StepStatus) (*Observation, error) {
				return &Observation{
					Status: status,
					Progress: StepProgress{
						Summary: string(status), Completed: 1, Total: 1, RunURL: "https://example.invalid/runs/1",
					},
				}, nil
			},
			func(_ context.Context, value StepStatus) error {
				recorded = append(recorded, value)
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		runner := new(StepRunner)
		if err := runner.Execute(context.Background(), []*Step{step}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(recorded, values) || reads != 3 {
			t.Fatalf("recorded=%v, reads=%d", recorded, reads)
		}
		snapshot := runner.Snapshot()
		if snapshot.Active || snapshot.Steps[0].Status != StepStatusSucceeded ||
			snapshot.Steps[0].Progress.RunURL != "https://example.invalid/runs/1" ||
			snapshot.Steps[0].Progress.Summary != "succeeded" {

			t.Fatalf("execution snapshot = %#v", snapshot)
		}
	})
}

func TestMonitorDistinguishesTerminalOutcomesFromReadAndCheckpointErrors(t *testing.T) {
	readErr, recordErr := errors.New("read unavailable"), errors.New("checkpoint unavailable")
	for _, test := range []struct {
		name      string
		status    StepStatus
		readErr   error
		recordErr error
		wantErr   error
		recorded  int
	}{
		{name: "success", status: StepStatusSucceeded, recorded: 1},
		{name: "failed", status: StepStatusFailed, recorded: 1},
		{name: "remote cancellation", status: StepStatusCanceled, wantErr: ErrCanceled, recorded: 1},
		{name: "local cancellation", readErr: context.Canceled, wantErr: context.Canceled},
		{name: "unavailable", readErr: readErr, wantErr: readErr},
		{name: "checkpoint", status: StepStatusCanceled, recordErr: recordErr, wantErr: recordErr, recorded: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := 0
			step, err := NewMonitor("Monitor", time.Minute,
				func(context.Context) (StepStatus, error) { return test.status, test.readErr },
				func(status StepStatus) (*Observation, error) {
					return &Observation{Status: status, Error: "provider result"}, nil
				},
				func(context.Context, StepStatus) error { records++; return test.recordErr },
			)
			if err != nil {
				t.Fatal(err)
			}
			err = step.Func(context.Background())
			if records != test.recorded || test.wantErr != nil && !errors.Is(err, test.wantErr) ||
				test.status == StepStatusSucceeded && err != nil || test.status == StepStatusFailed && err == nil {

				t.Fatalf("error=%v, records=%d", err, records)
			}
			if errors.Is(err, ErrCanceled) != (test.name == "remote cancellation") {
				t.Fatalf("incorrect external cancellation classification: %v", err)
			}
		})
	}
}

func TestRunnerObservationKeepsLastKnownDataAndReportsErrors(t *testing.T) {
	readErr := errors.New("provider unavailable")
	var failure error
	step := NewRootStep("Monitor", time.Minute, func(context.Context) error {
		t.Fatal("observation executed a step")
		return nil
	})
	step.Observe = func(context.Context) (*Observation, error) {
		return &Observation{
			Status:   StepStatusRunning,
			Progress: StepProgress{Summary: "Building", RunURL: "https://example.invalid/runs/1"},
		}, failure
	}
	runner, err := NewStepRunner([]*Step{step}, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := runner.Snapshot().Steps[0]
	failure = readErr
	if err := runner.Observe(context.Background()); !errors.Is(err, readErr) {
		t.Fatalf("refresh error = %v", err)
	}
	after := runner.Snapshot()
	if after.Active || after.Error != "" || after.Steps[0].ObservationError != readErr.Error() ||
		after.Steps[0].Status != before.Status ||
		after.Steps[0].ObservedAt != before.ObservedAt || !reflect.DeepEqual(after.Steps[0].Progress, before.Progress) {

		t.Fatalf("lost last-known data or error: %#v", after)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, duplicate := wire["observationError"]; duplicate || strings.Count(string(encoded), `"observationError"`) != 1 {
		t.Fatalf("refresh error was serialized outside its step or more than once: %s", encoded)
	}
	if saved := runner.ExecutionSnapshot(); saved.Steps[0].Status != StepStatusWaiting ||
		saved.Steps[0].ObservationError != "" {

		t.Fatalf("observations leaked into a checkpoint: %#v", saved)
	}
	failure = nil
	if err := runner.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fresh := runner.Snapshot(); fresh.Steps[0].ObservationError != "" {
		t.Fatalf("successful retry retained the refresh error: %#v", fresh)
	}
}

func TestRunnerObservationCannotOverwriteAResumedExecution(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	step := NewRootStep("Monitor", time.Minute, func(context.Context) error { return nil })
	step.Observe = func(context.Context) (*Observation, error) {
		close(entered)
		<-release
		return &Observation{Status: StepStatusFailed, Error: "stale observation"}, nil
	}
	runner, err := NewStepRunner([]*Step{step}, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- runner.Observe(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observation did not start")
	}
	if err := runner.Execute(context.Background(), []*Step{step}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("stale observation was accepted: %v", err)
	}
	if snapshot := runner.Snapshot(); snapshot.Steps[0].Status != StepStatusSucceeded ||
		!snapshot.Steps[0].ObservedAt.IsZero() || snapshot.Steps[0].ObservationError != "" {

		t.Fatalf("stale observation overwrote execution: %#v", snapshot)
	}
}

func TestMonitorDoesNotRecordAfterLocalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	records := 0
	step, err := NewMonitor("Monitor", time.Minute,
		func(context.Context) (StepStatus, error) { cancel(); return StepStatusCanceled, nil },
		func(status StepStatus) (*Observation, error) { return &Observation{Status: status}, nil },
		func(context.Context, StepStatus) error { records++; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := step.Func(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, ErrCanceled) || records != 0 {
		t.Fatalf("local cancellation result=%v, records=%d", err, records)
	}
}

func TestRunnerReportsAnObservationDeadlineWithoutChangingSavedProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		step := NewRootStep("Monitor", time.Minute, func(context.Context) error { return nil })
		step.Observe = func(ctx context.Context) (*Observation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		runner, err := NewStepRunner([]*Step{step}, Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runner.Observe(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("observation deadline error = %v", err)
		}
		snapshot := runner.Snapshot()
		if snapshot.Error != "" || snapshot.Steps[0].ObservationError == "" ||
			snapshot.Steps[0].Status != StepStatusWaiting || !snapshot.Steps[0].ObservedAt.IsZero() {

			t.Fatalf("deadline hid the error or invented an outcome: %#v", snapshot)
		}
	})
}

func TestMonitorRejectsUnknownOutcomesAndInvalidLinks(t *testing.T) {
	for _, observation := range []Observation{
		{Status: "unsupported"},
		{Status: StepStatusSucceeded, Progress: StepProgress{RunURL: "javascript:alert(1)"}},
		{Status: StepStatusRunning, Progress: StepProgress{RunURL: "http://example.invalid/run"}},
		{Status: StepStatusRunning, Progress: StepProgress{RunURL: "https://person@example.invalid/run"}},
	} {
		t.Run(string(observation.Status)+observation.Progress.RunURL, func(t *testing.T) {
			step, err := NewMonitor("Monitor", time.Minute,
				func(context.Context) (Observation, error) { return observation, nil },
				func(value Observation) (*Observation, error) { return &value, nil },
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := step.Observe(context.Background()); err == nil {
				t.Fatal("invalid observation was accepted for viewing")
			}
			if err := step.Func(context.Background()); err == nil {
				t.Fatal("invalid observation was accepted during execution")
			}
		})
	}
}

func TestNewStepRunnerRestoresTheCanonicalStepRunLink(t *testing.T) {
	step := NewRootStep("Monitor", time.Minute, func(context.Context) error { return nil })
	runner, err := NewStepRunner([]*Step{step}, Snapshot{Steps: []StepSnapshot{
		{
			Name: step.Name, Status: StepStatusRunning,
			Progress: &StepProgress{RunURL: "https://example.invalid/runs/1"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runner.ExecutionSnapshot()
	if snapshot.Steps[0].Progress.RunURL != "https://example.invalid/runs/1" {
		t.Fatalf("saved step lost its external link: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, duplicate := wire.Steps[0]["runUrl"]; duplicate || strings.Count(string(encoded), `"runUrl"`) != 1 {
		t.Fatalf("step URL was serialized outside its progress or more than once: %s", encoded)
	}
}
