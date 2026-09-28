// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdorunstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/runstore"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeWorkItemClient struct {
	created   *azdoworkitem.Snapshot
	updated   *azdoworkitem.Snapshot
	updateErr error
}

func (f *fakeWorkItemClient) Create(
	_ context.Context,
	_, assignedTo string,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if assignedTo != "Release Operator" {
		return nil, errors.New("unexpected release work item assignee")
	}
	f.created = snapshot
	return testWorkItem(42, 1, "Active", snapshot), nil
}

func (f *fakeWorkItemClient) Get(context.Context, int) (*azdoworkitem.WorkItem, error) {
	return testWorkItem(42, 1, "Active", f.created), nil
}

func (f *fakeWorkItemClient) Update(
	_ context.Context,
	_ *azdoworkitem.WorkItem,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updated = snapshot
	return testWorkItem(42, 2, "Closed", snapshot), nil
}

func (f *fakeWorkItemClient) Query(context.Context, bool, int) ([]*azdoworkitem.WorkItem, error) {
	return []*azdoworkitem.WorkItem{testWorkItem(42, 1, "Active", f.created)}, nil
}

func TestStoreRoundTrip(t *testing.T) {
	client := &fakeWorkItemClient{}
	store, err := newStore(client, "Release Operator")
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
		client.created.Status != azdoworkitem.StatusRunning || !client.created.Test {

		t.Fatalf("record = %#v, snapshot = %#v", record, client.created)
	}
	run.Complete = true
	run.Result = string(runstore.StatusSucceeded)
	updated, err := store.Update(context.Background(), record, run, view, plan)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Run.Result != string(runstore.StatusSucceeded) || !updated.Closed ||
		client.updated.Status != azdoworkitem.StatusSucceeded {

		t.Fatalf("updated record = %#v, snapshot = %#v", updated, client.updated)
	}
}

func TestStoreRejectsUnstartedRun(t *testing.T) {
	store, err := newStore(&fakeWorkItemClient{}, "Release Operator")
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
	client := &fakeWorkItemClient{updateErr: azdoworkitem.ErrRevisionConflict}
	store, err := newStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), &runstore.Record{
		ID: 42, Revision: 7,
	}, testReleaseRun(), nil, nil)
	if !errors.Is(err, runstore.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func testReleaseRun() *runstore.State {
	return &runstore.State{
		ProcessID: "example",
		Snapshot: &contract.StateSnapshot{
			Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
		},
		Digest:  testDigest,
		Started: true,
	}
}

func testWorkItem(id, revision int, state string, snapshot *azdoworkitem.Snapshot) *azdoworkitem.WorkItem {
	return &azdoworkitem.WorkItem{
		ID: id, Revision: revision, URL: "https://example.invalid/workitems/42",
		State: state, ChangedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), Snapshot: snapshot,
	}
}
