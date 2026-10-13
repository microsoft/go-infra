// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

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
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type fakeProcess struct {
	definition contract.ProcessDefinition
	preflight  func(context.Context) (error, error)
	prepare    func(context.Context, any) (*contract.StateSnapshot, error)
	build      func(context.Context, *fakeReleaseRun, contract.CheckpointFunc) ([]*coordinator.Step, error)
	plan       *contract.Plan
	planForRun func(*contract.StateSnapshot) *contract.Plan
	view       *contract.RunView
}

type fakeReleaseRun struct {
	mu      sync.RWMutex
	process *fakeProcess
	state   *contract.StateSnapshot
}

func (p *fakeProcess) Definition() contract.ProcessDefinition {
	return p.definition
}

func (p *fakeProcess) Preflight(ctx context.Context) (error, error) {
	if p.preflight == nil {
		return nil, nil
	}
	return p.preflight(ctx)
}

func (p *fakeProcess) InputForm(*releaseinput.Set) any {
	return nil
}

func (p *fakeProcess) Prepare(ctx context.Context, input any) (*contract.StateSnapshot, error) {
	if p.prepare == nil {
		return nil, errors.New("fake process preparation is not configured")
	}
	return p.prepare(ctx, input)
}

func (p *fakeProcess) Load(state *contract.StateSnapshot) (contract.Run, error) {
	if err := runstore.ValidateSnapshot(state); err != nil {
		return nil, err
	}
	return &fakeReleaseRun{process: p, state: runstore.CloneSnapshot(state)}, nil
}

func (r *fakeReleaseRun) TakeSnapshot() *contract.StateSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return runstore.CloneSnapshot(r.state)
}

func (r *fakeReleaseRun) TakeView() *contract.RunView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.process.view == nil {
		return &contract.RunView{Summary: "Ready"}
	}
	view := *r.process.view
	return &view
}

func (r *fakeReleaseRun) Plan() *contract.Plan {
	if r.process.planForRun != nil {
		return r.process.planForRun(r.TakeSnapshot())
	}
	if r.process.plan == nil {
		return nil
	}
	plan := *r.process.plan
	plan.Facts = append([]contract.PlanFact(nil), plan.Facts...)
	return &plan
}

func (r *fakeReleaseRun) Build(
	ctx context.Context,
	checkpoint contract.CheckpointFunc,
) ([]*coordinator.Step, error) {
	if r.process.build == nil {
		return nil, errors.New("fake process graph is not configured")
	}
	return r.process.build(ctx, r, checkpoint)
}

func (r *fakeReleaseRun) setState(state json.RawMessage) {
	r.mu.Lock()
	r.state.State = append(json.RawMessage(nil), state...)
	r.mu.Unlock()
}

func testProcessDefinition(id string) contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{Name: id, Mark: "EX", Description: "Example process"},
		ID:       id, InputPreamble: "Configure", InputSubmitLabel: "Review",
	}
}

type fakeProcessGroup struct {
	identity  *contract.Identity
	processes []contract.Process
}

func newFakeProcessGroup(processes ...contract.Process) *fakeProcessGroup {
	return &fakeProcessGroup{
		identity: &contract.Identity{
			Name: "Examples", Mark: "EG", Description: "Example processes",
		},
		processes: append([]contract.Process(nil), processes...),
	}
}

func (g *fakeProcessGroup) Processes() []contract.Process {
	return append([]contract.Process(nil), g.processes...)
}

func (g *fakeProcessGroup) Identity() *contract.Identity {
	return g.identity
}

func testProcessRun(t *testing.T) *runstore.State {
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
	records       map[int]*runstore.Record
	createErr     error
	queryErr      error
	updateErr     error
	updateStarted chan struct{}
	updateRelease <-chan struct{}
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*runstore.Record)}
}

func (s *memoryProcessRunStore) Create(
	_ context.Context,
	run *runstore.State,
	_ *contract.RunView,
	_ *contract.Plan,
) (*runstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &runstore.Record{
		ID: s.nextID, Revision: 1,
		URL:       fmt.Sprintf("https://example.invalid/releases/%d", s.nextID),
		UpdatedAt: run.UpdatedAt, Run: run.Clone(),
	}
	s.records[record.ID] = record
	s.nextID++
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*runstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("process run record not found")
	}
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) Query(
	_ context.Context,
	closed bool,
	limit int,
) ([]*runstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	records := make([]*runstore.Record, 0, len(s.records))
	for _, record := range s.records {
		if record.Closed == closed {
			records = append(records, cloneReleaseRunRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *memoryProcessRunStore) Update(
	_ context.Context,
	current *runstore.Record,
	run *runstore.State,
	_ *contract.RunView,
	_ *contract.Plan,
) (*runstore.Record, error) {
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
		return nil, runstore.ErrConflict
	}
	record = &runstore.Record{
		ID: record.ID, Revision: record.Revision + 1,
		URL: record.URL, Closed: record.Closed,
		UpdatedAt: time.Now().UTC(), Run: run.Clone(),
	}
	s.records[record.ID] = record
	return cloneReleaseRunRecord(record), nil
}

func (s *memoryProcessRunStore) latest(t *testing.T) *runstore.State {
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

func (s *memoryProcessRunStore) waitForCompleted(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		completed := 0
		for _, record := range s.records {
			if record.Run.Complete {
				completed++
			}
		}
		s.mu.Unlock()
		if completed == count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed records = %d, want %d", completed, count)
		}
		time.Sleep(time.Millisecond)
	}
}

func cloneReleaseRunRecord(record *runstore.Record) *runstore.Record {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Run = record.Run.Clone()
	return &clone
}

var _ runstore.Store = (*memoryProcessRunStore)(nil)
