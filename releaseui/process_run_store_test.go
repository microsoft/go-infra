// Tests for generic process-run persistence.
package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/runstore"
)

func TestProcessRunStateRejectsCheckpointBeforeStart(t *testing.T) {
	run := testProcessRun(t)
	run.Checkpointed = true
	if err := run.Validate(); err == nil {
		t.Fatal("unstarted checkpointed run passed validation")
	}
}

func TestReleaseRunRecordRejectsClosedIncompleteRun(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	record := testReleaseRunRecord(1, run, true, time.Now().UTC())
	if err := validateReleaseRunRecord(record); err == nil {
		t.Fatal("closed incomplete release record passed validation")
	}
}

func testProcessRun(t *testing.T) *releaseRunState {
	t.Helper()
	run, err := runstore.NewState("example", &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return run
}

type memoryProcessRunStore struct {
	mu            sync.Mutex
	nextID        int
	records       map[int]*releaseRunRecord
	createErr     error
	queryErr      error
	updateErr     error
	updateStarted chan struct{}
	updateRelease <-chan struct{}
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*releaseRunRecord)}
}

func (s *memoryProcessRunStore) Create(
	_ context.Context,
	run *releaseRunState,
	_ *contract.RunView,
	_ *contract.Plan,
) (*releaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &releaseRunRecord{
		ID: s.nextID, Revision: 1,
		URL:       fmt.Sprintf("https://example.invalid/releases/%d", s.nextID),
		UpdatedAt: run.UpdatedAt, Run: run.Clone(),
	}
	s.records[record.ID] = record
	s.nextID++
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*releaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("process run record not found")
	}
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) Query(_ context.Context, closed bool, limit int) ([]*releaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	records := make([]*releaseRunRecord, 0, len(s.records))
	for _, record := range s.records {
		if record.Closed == closed {
			records = append(records, cloneReleaseRunRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.After(records[j].UpdatedAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *memoryProcessRunStore) Update(
	_ context.Context,
	current *releaseRunRecord,
	run *releaseRunState,
	_ *contract.RunView,
	_ *contract.Plan,
) (*releaseRunRecord, error) {
	if s.updateStarted != nil {
		close(s.updateStarted)
	}
	if s.updateRelease != nil {
		<-s.updateRelease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	record, ok := s.records[current.ID]
	if !ok || record.Revision != current.Revision {
		return nil, errReleaseRunConflict
	}
	record = &releaseRunRecord{
		ID: record.ID, Revision: record.Revision + 1,
		URL: record.URL, Closed: record.Closed,
		UpdatedAt: time.Now().UTC(), Run: run.Clone(),
	}
	s.records[record.ID] = record
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) latest(t *testing.T) *releaseRunState {
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

func cloneReleaseRunRecord(record *releaseRunRecord) *releaseRunRecord {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Run = record.Run.Clone()
	return &clone
}

var _ runstore.Store = (*memoryProcessRunStore)(nil)
