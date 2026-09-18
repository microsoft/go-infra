// Tests for generic process-run persistence.
package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/contract"
)

type fakeReleaseWorkItemClient struct {
	item       *azdoworkitem.WorkItem
	title      string
	assignedTo string
}

func (f *fakeReleaseWorkItemClient) Create(
	_ context.Context,
	title, assignedTo string,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	f.title = title
	f.assignedTo = assignedTo
	f.item = &azdoworkitem.WorkItem{
		ID: 42, Revision: 1, URL: "https://example.invalid/workitems/42", Snapshot: snapshot,
	}
	return f.item, nil
}

func (f *fakeReleaseWorkItemClient) Get(context.Context, int) (*azdoworkitem.WorkItem, error) {
	return f.item, nil
}

func (f *fakeReleaseWorkItemClient) Update(
	_ context.Context,
	current *azdoworkitem.WorkItem,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if current != f.item {
		return nil, errors.New("unexpected current work item")
	}
	f.item = &azdoworkitem.WorkItem{
		ID: current.ID, Revision: current.Revision + 1, URL: current.URL, Snapshot: snapshot,
	}
	return f.item, nil
}

func TestProcessRunWorkItemStoreRoundTrip(t *testing.T) {
	client := &fakeReleaseWorkItemClient{}
	store, err := NewReleaseRunWorkItemStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	run := testProcessRun(t)
	run.Started = true
	record, err := store.Create(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkItemID != 42 || record.Revision != 1 || record.Run.Digest != run.Digest ||
		client.item.Snapshot.Status != azdoworkitem.StatusStarting || !client.item.Snapshot.Test ||
		client.title != "[releaseagent] Run example" || client.assignedTo != "Release Operator" {

		t.Fatalf("record = %#v, snapshot = %#v", record, client.item.Snapshot)
	}
	run.Complete = true
	run.Result = "succeeded"
	updated, err := store.Update(context.Background(), record, run)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Run.Result != "succeeded" ||
		client.item.Snapshot.Status != azdoworkitem.StatusSucceeded {

		t.Fatalf("updated = %#v, snapshot = %#v", updated, client.item.Snapshot)
	}
}

func TestProcessRunWorkItemStoreRejectsUnstartedRun(t *testing.T) {
	store, err := NewReleaseRunWorkItemStore(&fakeReleaseWorkItemClient{}, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), testProcessRun(t)); err == nil {
		t.Fatal("unstarted process run was persisted")
	}
}

func testProcessRun(t *testing.T) *contract.State {
	t.Helper()
	run, err := contract.NewState("example", contract.Plan{
		VariantID: "test",
		Test:      true,
		Input:     json.RawMessage(`{"mode":"test"}`), Payload: json.RawMessage(`{"value":"fixed"}`),
		View: contract.PlanView{
			IntentTitle: "Run example", ExecutionTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
		},
		Target: contract.Reference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type memoryProcessRunStore struct {
	mu        sync.Mutex
	nextID    int
	records   map[int]*ReleaseRunRecord
	createErr error
	updateErr error
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*ReleaseRunRecord)}
}

func (s *memoryProcessRunStore) Create(_ context.Context, run *contract.State) (*ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &ReleaseRunRecord{
		WorkItemID: s.nextID, Revision: 1,
		URL: fmt.Sprintf("https://example.invalid/workitems/%d", s.nextID),
		Run: run.Clone(),
	}
	s.records[record.WorkItemID] = record
	s.nextID++
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("process run record not found")
	}
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) Update(
	_ context.Context,
	current *ReleaseRunRecord,
	run *contract.State,
) (*ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	record, ok := s.records[current.WorkItemID]
	if !ok || record.Revision != current.Revision {
		return nil, azdoworkitem.ErrRevisionConflict
	}
	record = &ReleaseRunRecord{
		WorkItemID: record.WorkItemID, Revision: record.Revision + 1,
		URL: record.URL, Run: run.Clone(),
	}
	s.records[record.WorkItemID] = record
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) latest(t *testing.T) *contract.State {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(s.records))
	}
	for _, record := range s.records {
		return record.Run.Clone()
	}
	panic("unreachable")
}

func (s *memoryProcessRunStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func cloneProcessRunRecord(record *ReleaseRunRecord) *ReleaseRunRecord {
	clone := *record
	clone.Run = record.Run.Clone()
	return &clone
}

var _ ReleaseRunStore = (*memoryProcessRunStore)(nil)
