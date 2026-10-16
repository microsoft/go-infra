// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdorunstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeWorkItemClient struct {
	created   *azdoworkitem.Snapshot
	updated   *azdoworkitem.Snapshot
	updateErr error
	queried   []*azdoworkitem.WorkItem
	queryErr  error
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
	if f.queried != nil || f.queryErr != nil {
		return f.queried, f.queryErr
	}
	return []*azdoworkitem.WorkItem{testWorkItem(42, 1, "Active", f.created)}, nil
}

func TestStoreRoundTrip(t *testing.T) {
	client := &fakeWorkItemClient{}
	store, err := newStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	run := testReleaseRun()
	view := &contract.RunView{Test: true, Summary: "Ready", RunURL: "https://example.invalid/runs/888"}
	plan := &contract.Plan{Subtitle: "Run example", Facts: []contract.PlanFact{{Label: "Value", Value: "fixed"}}}
	record, err := store.Create(context.Background(), run, view, plan)
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 42 || record.Revision != 1 || record.Run.Digest != run.Digest ||
		client.created.Status != azdoworkitem.StatusRunning || !client.created.Test || record.Title != "Example test release" {

		t.Fatalf("record = %#v, snapshot = %#v", record, client.created)
	}
	hasRunLink := false
	for _, field := range client.created.Description.Fields {
		if field.Label == "External run" && field.URL == view.RunURL {
			hasRunLink = true
		}
	}
	if !hasRunLink {
		t.Fatal("work-item description omitted the external-run link")
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

func TestStoreQueryRetainsValidRecordsAndReportsInvalidRecords(t *testing.T) {
	snapshot, err := releaseRunSnapshot(testReleaseRun(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := *snapshot
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["schemaVersion"] = json.RawMessage(`1`)
	unsupported.Payload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeWorkItemClient{
		created: &unsupported,
		queried: []*azdoworkitem.WorkItem{
			testWorkItem(41, 1, "Active", &unsupported),
			testWorkItem(42, 1, "Active", snapshot),
			testWorkItem(43, 1, "Active", nil),
		},
	}
	store, err := newStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Query(context.Background(), false, 50)
	if err == nil {
		t.Fatal("query did not report invalid release records")
	}
	for _, want := range []string{"release record 41", "schemaVersion", "release record 43"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("query error %q does not contain %q", err, want)
		}
	}
	if len(records) != 1 || records[0].ID != 42 {
		t.Fatalf("valid records = %#v", records)
	}
	var problem *runstore.RecordError
	if !errors.As(err, &problem) || problem.ID != 41 || problem.URL != "https://example.invalid/workitems/41" ||
		problem.RecordState != "Active" || problem.SavedStatus != "running" || problem.Guidance == "" ||
		problem.Title != "Example test release" || problem.UpdatedAt.IsZero() || problem.Closed {

		t.Fatalf("read-only record metadata = %#v", problem)
	}
	if _, err := store.Get(context.Background(), 41); err == nil {
		t.Fatal("get accepted the unsupported payload")
	}
}

func TestStoreQueryExplainsUnsupportedFormatWithoutRestoringIt(t *testing.T) {
	snapshot := &azdoworkitem.Snapshot{
		SchemaVersion: azdoworkitem.CurrentSchemaVersion,
		ProcessID:     "go-images",
		Status:        azdoworkitem.StatusCanceled,
		IntentDigest:  testDigest,
		Payload:       json.RawMessage(`{"schemaVersion":8,"input":{},"state":{}}`),
	}
	client := &fakeWorkItemClient{
		created: snapshot,
		queried: []*azdoworkitem.WorkItem{testWorkItem(42, 1, "Closed", snapshot)},
	}
	store, err := newStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Query(context.Background(), false, 50)
	var problem *runstore.RecordError
	if len(records) != 0 || !errors.As(err, &problem) {
		t.Fatalf("records = %#v, error = %v, want an unreadable record", records, err)
	}
	if problem.ID != 42 || problem.SavedStatus != "canceled" || problem.RecordState != "Closed" || !problem.Closed ||
		problem.Message != "Cannot open in this session." || !strings.Contains(problem.Guidance, "saved summary") ||
		!strings.Contains(problem.Err.Error(), "format 8") {

		t.Fatalf("unsupported-format problem = %#v", problem)
	}
	if _, err := store.Get(context.Background(), 42); err == nil {
		t.Fatal("get accepted an unsupported-format record")
	}
}

func TestStoreQueryPreservesStructuredAndUnstructuredClientErrors(t *testing.T) {
	snapshot, err := releaseRunSnapshot(testReleaseRun(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("managed release data is unreadable")
	conflict := &azdoworkitem.ReadError{
		ID: 44, URL: "https://example.invalid/workitems/44",
		State: "Closed", Status: azdoworkitem.StatusRunning, Err: cause,
	}
	transportErr := errors.New("tracking service unavailable")
	client := &fakeWorkItemClient{
		queried:  []*azdoworkitem.WorkItem{testWorkItem(42, 1, "Active", snapshot)},
		queryErr: fmt.Errorf("query results: %w", errors.Join(conflict, transportErr)),
	}
	store, err := newStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Query(context.Background(), false, 50)
	if len(records) != 1 || records[0].ID != 42 || !errors.Is(err, cause) || !errors.Is(err, transportErr) {
		t.Fatalf("records = %#v, error = %v, want valid records and both causes", records, err)
	}
	var problem *runstore.RecordError
	if !errors.As(err, &problem) || problem.ID != 44 || problem.URL != conflict.URL ||
		problem.RecordState != "Closed" || !problem.Closed || problem.SavedStatus != "running" ||
		!strings.Contains(problem.Guidance, "managed release data") {

		t.Fatalf("state-conflict problem = %#v", problem)
	}
}

func TestStoreQueryKeepsValidRecordsWhenWorkItemQueryFails(t *testing.T) {
	snapshot, err := releaseRunSnapshot(testReleaseRun(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	queryErr := errors.New(`work item 2 state "Closed" is incompatible with release status "running"`)
	for _, test := range []struct {
		name    string
		items   []*azdoworkitem.WorkItem
		want    int
		wantErr string
	}{
		{name: "valid record", items: []*azdoworkitem.WorkItem{testWorkItem(42, 1, "Active", snapshot)}, want: 1},
		{
			name: "valid and invalid records",
			items: []*azdoworkitem.WorkItem{
				testWorkItem(42, 1, "Active", snapshot),
				testWorkItem(43, 1, "Active", nil),
			},
			want:    1,
			wantErr: "release record 43",
		},
		{name: "no records"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeWorkItemClient{queried: test.items, queryErr: queryErr}
			store, err := newStore(client, "Release Operator")
			if err != nil {
				t.Fatal(err)
			}
			records, err := store.Query(context.Background(), false, 50)
			if !errors.Is(err, queryErr) || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want original query error and %q", err, test.wantErr)
			}
			if len(records) != test.want || len(records) > 0 && records[0].ID != 42 {
				t.Fatalf("records = %#v, want %d valid records", records, test.want)
			}
		})
	}
}

func TestReleaseRecordTitleRemovesOnlyTheManagedPrefix(t *testing.T) {
	for _, test := range []struct {
		title string
		want  string
	}{
		{title: "[releaseagent] Go images test release: 1.26.8-1, 1.27.1-2", want: "Go images test release: 1.26.8-1, 1.27.1-2"},
		{title: "  [releaseagent]  Example test release  ", want: "Example test release"},
		{title: "A release with [releaseagent] in its title", want: "A release with [releaseagent] in its title"},
		{title: "[releaseagent]", want: ""},
	} {
		if got := releaseRecordTitle(test.title); got != test.want {
			t.Errorf("title %q became %q, want %q", test.title, got, test.want)
		}
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
		ID: id, Revision: revision, URL: fmt.Sprintf("https://example.invalid/workitems/%d", id),
		Title: "[releaseagent] Example test release",
		State: state, ChangedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), Snapshot: snapshot,
	}
}
