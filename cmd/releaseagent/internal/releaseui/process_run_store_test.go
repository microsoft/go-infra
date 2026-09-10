// Tests for generic process-run persistence.
package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
)

type fakeReleaseWorkItemClient struct {
	item *azdoworkitem.WorkItem
}

func (f *fakeReleaseWorkItemClient) Create(
	_ context.Context,
	title, assignedTo string,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if title != "[releaseagent] Run example" || assignedTo != "Release Operator" {
		return nil, fmt.Errorf("unexpected title %q or assignee %q", title, assignedTo)
	}
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
	store, err := NewProcessRunWorkItemStore(client, "Release Operator")
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
		client.item.Snapshot.Status != azdoworkitem.StatusStarting || !client.item.Snapshot.Test {

		t.Fatalf("record = %#v, snapshot = %#v", record, client.item.Snapshot)
	}
	loaded, err := store.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Run.Digest != run.Digest {
		t.Fatalf("loaded run = %#v", loaded.Run)
	}

	run.Complete = true
	run.Result = "succeeded"
	run.Checkpoint = json.RawMessage(`{"status":"completed"}`)
	run.External = &ProcessRunReference{
		ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
		Status: "completed", Terminal: true, Succeeded: true,
	}
	updated, err := store.Update(context.Background(), record, run)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Run.Result != "succeeded" ||
		client.item.Snapshot.Status != azdoworkitem.StatusSucceeded {

		t.Fatalf("updated = %#v, snapshot = %#v", updated, client.item.Snapshot)
	}
	description, err := azdoworkitem.RenderDescription(client.item.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"<strong>Status</strong></td><td>Succeeded",
		"<strong>Intent</strong></td><td>Run example",
		"<strong>Action</strong></td><td>Run example",
		`href="https://example.com/docs">Fixed input</a>`,
		`href="https://example.com/runs">example runs</a>`,
		`href="https://example.com/runs/7">example run 7 · completed</a>`,
	} {
		if !strings.Contains(description, text) {
			t.Fatalf("description does not contain %q: %s", text, description)
		}
	}
}

func TestProcessRunWorkItemStoreRejectsUnstartedRun(t *testing.T) {
	store, err := NewProcessRunWorkItemStore(&fakeReleaseWorkItemClient{}, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), testProcessRun(t)); err == nil {
		t.Fatal("unstarted process run was persisted")
	}
}

func testProcessRun(t *testing.T) *ProcessRun {
	t.Helper()
	run, err := newProcessRun("example", ProcessPreparedRun{
		Test:  true,
		Input: json.RawMessage(`{"mode":"test"}`), Payload: json.RawMessage(`{"value":"fixed"}`),
		Step: ProcessRunStep{Name: "Run example", Timeout: time.Minute},
		View: ProcessPlanView{
			IntentTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
			Facts:                []ProcessPlanFact{{Label: "Input", Value: "Fixed input", Href: "https://example.com/docs"}},
		},
		Target: ProcessRunReference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type memoryProcessRunStore struct {
	mu        sync.Mutex
	nextID    int
	records   map[int]*ProcessRunRecord
	createErr error
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*ProcessRunRecord)}
}

func (s *memoryProcessRunStore) Create(_ context.Context, run *ProcessRun) (*ProcessRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &ProcessRunRecord{
		WorkItemID: s.nextID, Revision: 1,
		URL: fmt.Sprintf("https://example.invalid/workitems/%d", s.nextID),
		Run: cloneProcessRun(run),
	}
	s.records[record.WorkItemID] = record
	s.nextID++
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*ProcessRunRecord, error) {
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
	current *ProcessRunRecord,
	run *ProcessRun,
) (*ProcessRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[current.WorkItemID]
	if !ok || record.Revision != current.Revision {
		return nil, azdoworkitem.ErrRevisionConflict
	}
	record = &ProcessRunRecord{
		WorkItemID: record.WorkItemID, Revision: record.Revision + 1,
		URL: record.URL, Run: cloneProcessRun(run),
	}
	s.records[record.WorkItemID] = record
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) seed(run *ProcessRun) int {
	record, err := s.Create(context.Background(), run)
	if err != nil {
		panic(err)
	}
	return record.WorkItemID
}

func (s *memoryProcessRunStore) latest(t *testing.T) *ProcessRun {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(s.records))
	}
	for _, record := range s.records {
		return cloneProcessRun(record.Run)
	}
	panic("unreachable")
}

func (s *memoryProcessRunStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func cloneProcessRunRecord(record *ProcessRunRecord) *ProcessRunRecord {
	clone := *record
	clone.Run = cloneProcessRun(record.Run)
	return &clone
}

var _ ProcessRunStore = (*memoryProcessRunStore)(nil)
