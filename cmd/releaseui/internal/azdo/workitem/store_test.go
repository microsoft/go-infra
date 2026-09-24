// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdoworkitem

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/webapi"
	"github.com/microsoft/azure-devops-go-api/azuredevops/workitemtracking"
	"github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
)

func TestStoreRoundTrip(t *testing.T) {
	var created *Snapshot
	sdk := &fakeClient{
		create: func(_ context.Context, args workitemtracking.CreateWorkItemArgs) (*workitemtracking.WorkItem, error) {
			created = snapshotFromPatch(t, args.Document, 5)
			return sdkWorkItem(t, 42, 1, created), nil
		},
		update: func(_ context.Context, args workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
			if args.Document == nil {
				t.Fatal("update document is nil")
			}
			assertPatch(t, *args.Document, 1, webapi.OperationValues.Add, "/fields/System.State", "Closed")
			updated := snapshotFromPatch(t, args.Document, 3)
			return sdkWorkItem(t, 42, 2, updated), nil
		},
	}
	store, err := NewStore(newTestClient(t, sdk, "test-token"), "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	run := testReleaseRun()
	view := &contract.RunView{Test: true, Summary: "Ready"}
	plan := &contract.Plan{Subtitle: "Run example", Facts: []contract.PlanFact{{Label: "Value", Value: "fixed"}}}
	record, err := store.Create(context.Background(), run, view, plan)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 42 || record.Revision != 1 || record.Run.Digest != run.Digest ||
		created.Status != StatusRunning || !created.Test {

		t.Fatalf("record = %#v, snapshot = %#v", record, created)
	}
	run.Complete = true
	run.Result = string(releaseui.ReleaseRunStatusSucceeded)
	updated, err := store.Update(context.Background(), record, run, view, plan)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Run.Result != string(releaseui.ReleaseRunStatusSucceeded) || !updated.Closed {
		t.Fatalf("updated record = %#v", updated)
	}
}

func TestStoreRejectsUnstartedRun(t *testing.T) {
	store, err := NewStore(newTestClient(t, &fakeClient{}, "test-token"), "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	run := testReleaseRun()
	run.Started = false
	if _, err := store.Create(context.Background(), run, nil, nil); err == nil {
		t.Fatal("unstarted release run was persisted")
	}
}

func TestStoreMapsRevisionConflict(t *testing.T) {
	typeName := "WorkItemRevisionMismatchException"
	message := "stale revision"
	sdk := &fakeClient{update: func(context.Context, workitemtracking.UpdateWorkItemArgs) (*workitemtracking.WorkItem, error) {
		return nil, azuredevops.WrappedError{TypeName: &typeName, Message: &message}
	}}
	store, err := NewStore(newTestClient(t, sdk, "test-token"), "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), &releaseui.ReleaseRunRecord{
		ID: 42, Revision: 7,
	}, testReleaseRun(), nil, nil)
	if !errors.Is(err, releaseui.ErrReleaseRunConflict) {
		t.Fatalf("error = %v, want ErrReleaseRunConflict", err)
	}
}

func testReleaseRun() *releaseui.ReleaseRunState {
	return &releaseui.ReleaseRunState{
		ProcessID: "example",
		Snapshot: &contract.StateSnapshot{
			Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
		},
		Digest:  testDigest,
		Started: true,
	}
}

func snapshotFromPatch(t *testing.T, document *[]webapi.JsonPatchOperation, index int) *Snapshot {
	t.Helper()
	if document == nil || len(*document) <= index {
		t.Fatalf("patch document = %#v", document)
	}
	description, ok := (*document)[index].Value.(string)
	if !ok {
		t.Fatalf("description patch = %#v", (*document)[index])
	}
	snapshot, err := ParseDescription(description)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
