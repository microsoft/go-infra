// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type testRun struct {
	snapshot *contract.StateSnapshot
	plan     *contract.Plan
	view     *contract.RunView
}

func (r *testRun) TakeSnapshot() *contract.StateSnapshot {
	return runstore.CloneSnapshot(r.snapshot)
}

func (r *testRun) TakeView() *contract.RunView {
	if r.view == nil {
		return nil
	}
	view := *r.view
	return &view
}

func (r *testRun) Plan() *contract.Plan {
	return r.plan
}

func (r *testRun) Build(context.Context, contract.CheckpointFunc) ([]*coordinator.Step, error) {
	return nil, errors.New("test run does not build an execution graph")
}

type testStore struct {
	mu sync.Mutex

	record        *runstore.Record
	updateErr     error
	updateStarted chan struct{}
	updateRelease <-chan struct{}
	startOnce     sync.Once
	updates       atomic.Int32
}

func (s *testStore) Create(
	context.Context,
	*runstore.State,
	*contract.RunView,
	*contract.Plan,
) (*runstore.Record, error) {
	return nil, errors.New("test store does not create records")
}

func (s *testStore) Get(context.Context, int) (*runstore.Record, error) {
	return nil, errors.New("test store does not load records")
}

func (s *testStore) Update(
	_ context.Context,
	current *runstore.Record,
	state *runstore.State,
	_ *contract.RunView,
	_ *contract.Plan,
) (*runstore.Record, error) {
	s.updates.Add(1)
	if s.updateStarted != nil {
		s.startOnce.Do(func() { close(s.updateStarted) })
	}
	if s.updateRelease != nil {
		<-s.updateRelease
	}
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != current.ID || s.record.Revision != current.Revision {
		return nil, runstore.ErrConflict
	}
	s.record = &runstore.Record{
		ID: current.ID, Revision: current.Revision + 1, URL: current.URL,
		UpdatedAt: time.Now().UTC(), Run: state.Clone(),
	}
	return cloneTestRecord(s.record), nil
}

func (s *testStore) Query(context.Context, bool, int) ([]*runstore.Record, error) {
	return nil, errors.New("test store does not query records")
}

func TestCheckpointFailureCancelsExecutionContext(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateErr = errors.New("checkpoint failed")
	checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
	checkpointer.Checkpoint()
	checkpointer.Close()
	if !errors.Is(manager.ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want cancellation", manager.ctx.Err())
	}
	if !checkpointer.failed.Load() {
		t.Fatal("checkpoint failure was not recorded")
	}
}

func TestProcessRunOutcome(t *testing.T) {
	checkpointErr := errors.New("checkpoint failed")
	for _, test := range []struct {
		name                string
		executionErr        error
		snapshotErr         error
		checkpointFailed    bool
		checkpointPersisted bool
		complete            bool
		result              string
	}{
		{name: "success", complete: true, result: resultSucceeded},
		{name: "failure", executionErr: errors.New("step failed"), complete: true, result: resultFailed},
		{name: "canceled before checkpoint", executionErr: context.Canceled, complete: true, result: resultUncertain},
		{name: "deadline before checkpoint", executionErr: context.DeadlineExceeded, complete: true, result: resultUncertain},
		{name: "canceled after checkpoint", executionErr: context.Canceled, checkpointPersisted: true},
		{name: "deadline after checkpoint", executionErr: context.DeadlineExceeded, checkpointPersisted: true},
		{name: "snapshot", snapshotErr: errors.New("snapshot failed"), complete: true, result: resultUncertain},
		{name: "checkpoint", executionErr: checkpointErr, checkpointFailed: true, complete: true, result: resultUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			complete, result := processRunOutcome(
				test.executionErr, test.snapshotErr, test.checkpointFailed, test.checkpointPersisted,
			)
			if complete != test.complete || result != test.result {
				t.Fatalf("outcome = (%v, %q), want (%v, %q)", complete, result, test.complete, test.result)
			}
		})
	}
}

func TestCheckpointWaitsForPersistence(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
	returned := make(chan struct{})
	go func() {
		checkpointer.Checkpoint()
		close(returned)
	}()
	waitForTestSignal(t, store.updateStarted)
	select {
	case <-returned:
		t.Fatal("Checkpoint returned before persistence completed")
	default:
	}
	close(release)
	waitForTestSignal(t, returned)
	checkpointer.Close()
}

func TestPersistenceDoesNotBlockReads(t *testing.T) {
	for _, phase := range []string{"checkpoint", "completion"} {
		t.Run(phase, func(t *testing.T) {
			manager, session, run, store, cancel := checkpointTestRun(t)
			defer cancel()
			store.updateStarted = make(chan struct{})
			release := make(chan struct{})
			store.updateRelease = release
			checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if phase == "checkpoint" {
					checkpointer.Checkpoint()
					return
				}
				steps := []*coordinator.Step{
					coordinator.NewRootStep(
						"complete", time.Minute, func(context.Context) error { return nil },
					),
				}
				manager.executeProcessRun(
					session, session.state.Digest, manager.ctx,
					session.runner, steps, run, checkpointer,
				)
			}()
			waitForTestSignal(t, store.updateStarted)
			readDone := make(chan struct{})
			go func() {
				manager.Current("example")
				close(readDone)
			}()
			waitForTestSignal(t, readDone)
			close(release)
			waitForTestSignal(t, done)
			checkpointer.Close()
		})
	}
}

func TestCheckpointDoesNotOverwriteChangedRun(t *testing.T) {
	for _, change := range []string{"digest", "record", "revision"} {
		t.Run(change, func(t *testing.T) {
			manager, session, run, store, cancel := checkpointTestRun(t)
			defer cancel()
			store.updateStarted = make(chan struct{})
			release := make(chan struct{})
			store.updateRelease = release
			checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
			done := make(chan struct{})
			go func() {
				checkpointer.Checkpoint()
				close(done)
			}()
			waitForTestSignal(t, store.updateStarted)
			session.mu.Lock()
			newState := session.state.Clone()
			newRecord := cloneTestRecord(session.record)
			switch change {
			case "digest":
				newState.Digest = "changed"
			case "record":
				newRecord.ID++
			case "revision":
				newRecord.Revision++
			}
			session.state = newState
			session.record = newRecord
			session.mu.Unlock()
			close(release)
			waitForTestSignal(t, done)
			checkpointer.Close()
			if !checkpointer.failed.Load() || checkpointer.persisted.Load() ||
				!errors.Is(manager.ctx.Err(), context.Canceled) {

				t.Fatalf(
					"stale checkpoint: failed = %v, persisted = %v, context = %v",
					checkpointer.failed.Load(), checkpointer.persisted.Load(), manager.ctx.Err(),
				)
			}
			session.mu.Lock()
			unchanged := session.state == newState && session.record == newRecord
			session.mu.Unlock()
			if !unchanged {
				t.Fatal("the stale storage response replaced the changed run")
			}
		})
	}
}

func TestConcurrentCheckpointsStaySerialized(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
	firstDone := make(chan struct{})
	go func() {
		checkpointer.Checkpoint()
		close(firstDone)
	}()
	waitForTestSignal(t, store.updateStarted)
	secondDone := make(chan struct{})
	go func() {
		checkpointer.Checkpoint()
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("the second checkpoint returned while the first write was blocked")
	default:
	}
	close(release)
	waitForTestSignal(t, firstDone)
	waitForTestSignal(t, secondDone)
	checkpointer.Close()
	if updates := store.updates.Load(); updates != 2 {
		t.Fatalf("updates = %d, want 2", updates)
	}
}

func checkpointTestRun(
	t *testing.T,
) (*Manager, *processSession, contract.Run, *testStore, context.CancelFunc) {
	t.Helper()
	state, err := runstore.NewState(
		"example",
		&contract.StateSnapshot{Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`)},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	state.Started = true
	record := &runstore.Record{
		ID: 1, Revision: 1, URL: "https://example.test/1",
		UpdatedAt: time.Now().UTC(), Run: state.Clone(),
	}
	run := &testRun{
		snapshot: runstore.CloneSnapshot(state.Snapshot),
		plan:     &contract.Plan{Subtitle: "Example"},
		view:     &contract.RunView{Summary: "Running", UpdatedAt: state.UpdatedAt},
	}
	session := &processSession{
		run: run, state: state.Clone(), record: cloneTestRecord(record), plan: run.plan,
		runner: &coordinator.StepRunner{},
	}
	store := &testStore{record: cloneTestRecord(record)}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		ctx: ctx, store: store, sessions: map[string]*processSession{"example": session},
	}
	return manager, session, run, store, cancel
}

func cloneTestRecord(record *runstore.Record) *runstore.Record {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Run = record.Run.Clone()
	return &clone
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not reach its synchronization point")
	}
}
