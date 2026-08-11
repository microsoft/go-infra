// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessRunStoreRoundTrip(t *testing.T) {
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := testProcessRun(t)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != run.Digest || loaded.ProcessID != "example" || loaded.Started || loaded.Complete ||
		loaded.UpdatedAt.IsZero() {

		t.Fatalf("loaded run = %#v", loaded)
	}
	loaded.Digest = "tampered"
	if err := store.Save(context.Background(), loaded); err == nil {
		t.Fatal("tampered process run was persisted")
	}
}

func TestProcessRunStoreRequiresPairedCheckpoint(t *testing.T) {
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := testProcessRun(t)
	run.Started = true
	run.Checkpoint = json.RawMessage(`{"run":7}`)
	if err := store.Save(context.Background(), run); err == nil {
		t.Fatal("checkpoint without an external run was persisted")
	}
	run.Checkpoint = nil
	run.External = &ProcessRunReference{
		ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
	}
	if err := store.Save(context.Background(), run); err == nil {
		t.Fatal("external run without a checkpoint was persisted")
	}
}

func testProcessRun(t *testing.T) *ProcessRun {
	t.Helper()
	run, err := newProcessRun("example", ProcessPreparedRun{
		Input: json.RawMessage(`{"mode":"test"}`), Payload: json.RawMessage(`{"value":"fixed"}`),
		Step: ProcessRunStep{Name: "Run example", Timeout: time.Minute},
		View: ProcessPlanView{
			IntentTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
		},
		Target: ProcessRunReference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

var _ ProcessRunStore = (*ProcessRunFileStore)(nil)
