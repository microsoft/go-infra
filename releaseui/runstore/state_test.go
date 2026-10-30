// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package runstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func TestStateLifecycle(t *testing.T) {
	snapshot := &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
	}
	state, err := NewState("example", snapshot, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	state.Started = true
	if status, err := state.Status(); err != nil || status != StatusRunning {
		t.Fatalf("status = %q, error = %v", status, err)
	}
	state.Complete = true
	state.Result = string(StatusSucceeded)
	if status, err := state.Status(); err != nil || status != StatusSucceeded {
		t.Fatalf("status = %q, error = %v", status, err)
	}
}

func TestStateRejectsCheckpointBeforeStart(t *testing.T) {
	state, err := NewState("example", &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.Checkpointed = true
	if err := state.Validate(); err == nil {
		t.Fatal("unstarted checkpointed run passed validation")
	}
}

func TestRecordReadsClosedIncompleteRun(t *testing.T) {
	state, err := NewState("example", &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	state.Started = true
	record := &Record{ID: 1, Revision: 1, Closed: true, UpdatedAt: time.Now().UTC(), Run: state}
	if err := ValidateRecord(record); err != nil {
		t.Fatalf("read-only closed record validation: %v", err)
	}
}

func TestStateCloneOwnsSavedGraph(t *testing.T) {
	state := &State{Execution: coordinator.Snapshot{Steps: []coordinator.StepSnapshot{
		{
			Name: "Work", Status: coordinator.StepStatusRunning, DependsOn: []string{"Initialize"},
			Progress: &coordinator.StepProgress{Completed: 5, Total: 30},
		},
	}}}
	clone := state.Clone()
	clone.Execution.Steps[0].DependsOn[0] = "changed"
	clone.Execution.Steps[0].Progress.Completed = 30
	if state.Execution.Steps[0].DependsOn[0] != "Initialize" || state.Execution.Steps[0].Progress.Completed != 5 {
		t.Fatal("cloned execution mutated the stored graph")
	}
}

func TestStateRejectsReadOnlyObservationMetadataInDurableExecution(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*coordinator.Snapshot)
	}{
		{name: "step observation error", change: func(snapshot *coordinator.Snapshot) { snapshot.Steps[0].ObservationError = "read failed" }},
		{name: "step observation time", change: func(snapshot *coordinator.Snapshot) { snapshot.Steps[0].ObservedAt = time.Now().UTC() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := NewState("example", &contract.StateSnapshot{
				Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`),
			}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			state.Execution = coordinator.Snapshot{Steps: []coordinator.StepSnapshot{
				{Name: "Monitor", Status: coordinator.StepStatusRunning},
			}}
			test.change(&state.Execution)
			if err := state.Validate(); err == nil {
				t.Fatal("durable state accepted read-only observation metadata")
			}
		})
	}
}
