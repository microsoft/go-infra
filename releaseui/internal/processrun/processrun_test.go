// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type testRun struct {
	snapshot *contract.StateSnapshot
	plan     *contract.Plan
	view     *contract.RunView
	build    func(context.Context, contract.CheckpointFunc) ([]*coordinator.Step, error)
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

func (r *testRun) Build(ctx context.Context, checkpoint contract.CheckpointFunc) ([]*coordinator.Step, error) {
	if r.build != nil {
		return r.build(ctx, checkpoint)
	}
	return nil, errors.New("test run does not build an execution graph")
}

type blockingViewRun struct {
	contract.Run
	entered chan struct{}
	release <-chan struct{}
}

func (r *blockingViewRun) TakeView() *contract.RunView {
	close(r.entered)
	<-r.release
	return r.Run.TakeView()
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
	string,
	*runstore.State,
	*contract.RunView,
	*contract.Plan,
) (*runstore.Record, error) {
	return nil, errors.New("test store does not create records")
}

func (s *testStore) Get(_ context.Context, id int) (*runstore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.ID != id {
		return nil, errors.New("test store record not found")
	}
	return cloneTestRecord(s.record), nil
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

func TestCurrentIncludesExternalRunURL(t *testing.T) {
	manager, session, _, _, cancel := checkpointTestRun(t)
	defer cancel()
	session.run = &testRun{
		snapshot: session.state.Snapshot,
		plan:     session.plan,
		view:     &contract.RunView{RunURL: "https://example.test/runs/888"},
	}
	current, found := manager.Current("example")
	if !found || current.Execution.RunURL != "https://example.test/runs/888" ||
		current.Execution.Record == nil || current.Execution.Record.ID != 1 {

		t.Fatalf("current release = %#v, found = %v", current, found)
	}
}

func TestCurrentDoesNotHoldSessionLockDuringTakeView(t *testing.T) {
	manager, session, run, _, cancel := checkpointTestRun(t)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	session.run = &blockingViewRun{Run: run, entered: entered, release: release}
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Current("example")
	}()
	readDone := make(chan struct{})
	readStarted := false
	t.Cleanup(func() {
		close(release)
		<-done
		if readStarted {
			<-readDone
		}
	})
	waitForTestSignal(t, entered)
	readStarted = true
	go func() {
		defer close(readDone)
		manager.Active("example")
	}()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Error("runner access was blocked by TakeView")
	}
}

func TestValidateRecordForExecution(t *testing.T) {
	state, err := runstore.NewState("example", &contract.StateSnapshot{
		Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.Started = true
	for _, test := range []struct {
		name      string
		closed    bool
		complete  bool
		result    string
		malformed string
		wantError bool
	}{
		{name: "open unfinished"},
		{name: "closed unfinished", closed: true, wantError: true},
		{name: "open succeeded", complete: true, result: resultSucceeded},
		{name: "closed succeeded", closed: true, complete: true, result: resultSucceeded},
		{name: "closed canceled", closed: true, complete: true, result: resultCanceled},
		{name: "missing identity", malformed: "id", wantError: true},
		{name: "missing state", malformed: "state", wantError: true},
		{name: "missing timestamp", malformed: "timestamp", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := &runstore.Record{
				ID: 1, Revision: 1, Closed: test.closed, UpdatedAt: time.Now().UTC(), Run: state.Clone(),
			}
			record.Run.Complete, record.Run.Result = test.complete, test.result
			switch test.malformed {
			case "id":
				record.ID = 0
			case "state":
				record.Run = nil
			case "timestamp":
				record.UpdatedAt = time.Time{}
			}
			if err := validateRecordForExecution(record); (err != nil) != test.wantError {
				t.Fatalf("execution validation = %v, want error = %v", err, test.wantError)
			}
		})
	}
}

func TestTrackingErrorGroupingUsesNeutralClosedFlag(t *testing.T) {
	for _, test := range []struct {
		name        string
		closed      bool
		recordState string
	}{
		{name: "archived provider state", closed: true, recordState: "Archived"},
		{name: "resolved provider state", closed: true, recordState: "Resolved"},
		{name: "no provider state", closed: true},
		{name: "open provider state", recordState: "Investigating"},
		{name: "display text is not authoritative", recordState: "Closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dashboard webview.DashboardResponse
			problem := &runstore.RecordError{
				ID: 42, Closed: test.closed, RecordState: test.recordState,
				Message: "Unreadable saved release.", Guidance: "Open the tracking item.",
				Err: errors.New("unreadable metadata"),
			}
			addTrackingError(&dashboard, problem)
			addTrackingError(&dashboard, problem)
			var summary webview.ReleaseSummary
			if test.closed {
				if len(dashboard.Recent) != 1 || len(dashboard.NeedsAttention) != 0 {
					t.Fatalf("closed grouping = %#v", dashboard)
				}
				summary = dashboard.Recent[0]
			} else {
				if len(dashboard.NeedsAttention) != 1 || len(dashboard.Recent) != 0 {
					t.Fatalf("open grouping = %#v", dashboard)
				}
				summary = dashboard.NeedsAttention[0]
			}
			if summary.Closed != test.closed || summary.Issue.RecordState != test.recordState {
				t.Fatalf("provider-neutral metadata = %#v", summary)
			}
		})
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
		{name: "external canceled", executionErr: errors.Join(errors.New("pipeline canceled"), coordinator.ErrCanceled), complete: true, result: resultCanceled},
		{name: "external canceled but checkpoint failed", executionErr: coordinator.ErrCanceled, checkpointFailed: true, complete: true, result: resultUncertain},
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

func TestCheckpointSavesGraphProgressWithoutClaimingLiveExecution(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
	checkpointed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); <-done })
	steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(ctx context.Context) error {
		coordinator.ReportProgress(ctx, coordinator.StepProgress{Completed: 5, Total: 30})
		checkpointer.Checkpoint()
		close(checkpointed)
		<-release
		return nil
	})}
	go func() {
		defer close(done)
		manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
	}()
	waitForTestSignal(t, checkpointed)
	record, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	graph := record.Run.Execution
	if graph.Active || len(graph.Steps) != 1 || graph.Steps[0].Status != coordinator.StepStatusRunning ||
		graph.Steps[0].Progress == nil || graph.Steps[0].Progress.Completed != 5 {

		t.Fatalf("saved graph = %#v", graph)
	}
	unblock()
	waitForTestSignal(t, done)
	record, err = store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Run.Complete || record.Run.Execution.Steps[0].Status != coordinator.StepStatusSucceeded {
		t.Fatalf("final saved graph = %#v", record.Run)
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
			if phase == "completion" && !session.runner.Snapshot().Active {
				t.Fatal("runner reported completion before its outcome was saved")
			}
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

func TestFinalPersistenceFailureIsVisible(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateErr = errors.New("revision changed during final persistence")
	checkpointer := manager.newProcessCheckpointer(session, session.state.Digest, run, cancel)
	steps := []*coordinator.Step{
		coordinator.NewRootStep("finish", time.Minute, func(context.Context) error { return nil }),
	}
	manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
	snapshot := session.runner.Snapshot()
	if snapshot.Active || !strings.Contains(snapshot.Error, store.updateErr.Error()) {
		t.Fatalf("completion snapshot = %#v", snapshot)
	}
	current, found := manager.Current("example")
	if !found || current.Execution.Run.Result != resultUncertain {
		t.Fatalf("browser outcome = %#v, found = %v", current.Execution.Run, found)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state.Result != resultUncertain || session.running {
		t.Fatalf("session result = %q, running = %v", session.state.Result, session.running)
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
