// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package runstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
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

func TestRecordRejectsClosedIncompleteRun(t *testing.T) {
	state, err := NewState("example", &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.Started = true
	record := &Record{ID: 1, Revision: 1, Closed: true, UpdatedAt: time.Now().UTC(), Run: state}
	if err := ValidateRecord(record); err == nil {
		t.Fatal("closed incomplete release record passed validation")
	}
}
