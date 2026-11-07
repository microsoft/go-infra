// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

type testRun struct {
	snapshot    *contract.StateSnapshot
	snapshotErr error
	plan        *contract.Plan
	view        *contract.RunView
	build       func() ([]*coordinator.Step, error)
}

func (r *testRun) TakeSnapshot() (*contract.StateSnapshot, error) {
	return runstore.CloneSnapshot(r.snapshot), r.snapshotErr
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

func (r *testRun) Build() ([]*coordinator.Step, error) {
	if r.build != nil {
		return r.build()
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
	view          *runstore.View
	updateErr     error
	updateStarted chan struct{}
	updateRelease <-chan struct{}
	startOnce     sync.Once
	updates       atomic.Int32
	savedStates   []string
}

func (s *testStore) Create(
	context.Context,
	string,
	*runstore.State,
	*runstore.View,
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
	view *runstore.View,
) (*runstore.Record, error) {
	s.updates.Add(1)
	s.mu.Lock()
	updateErr := s.updateErr
	s.mu.Unlock()
	if s.updateStarted != nil {
		s.startOnce.Do(func() { close(s.updateStarted) })
	}
	if s.updateRelease != nil {
		<-s.updateRelease
	}
	if updateErr != nil {
		return nil, updateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.ID != current.ID || s.record.Revision != current.Revision {
		return nil, runstore.ErrConflict
	}
	s.savedStates = append(s.savedStates, string(state.Snapshot.State))
	s.record = &runstore.Record{
		ID: current.ID, Revision: current.Revision + 1, URL: current.URL,
		UpdatedAt: time.Now().UTC(), Run: state.Clone(),
	}
	s.view = view.Clone()
	return cloneTestRecord(s.record), nil
}

func (s *testStore) Query(context.Context, bool, int) ([]*runstore.Record, error) {
	return nil, errors.New("test store does not query records")
}

func (s *testStore) GetView(context.Context, int) (*runstore.ViewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &runstore.ViewRecord{
		ID: s.record.ID, Revision: s.record.Revision, URL: s.record.URL, Title: s.record.Title,
		Closed: s.record.Closed, View: s.view.Clone(),
	}, nil
}

func (s *testStore) QueryViews(ctx context.Context, closed bool, _ int) ([]*runstore.ViewRecord, error) {
	if s.record.Closed != closed {
		return nil, nil
	}
	record, err := s.GetView(ctx, s.record.ID)
	return []*runstore.ViewRecord{record}, err
}

func TestCheckpointFailureCancelsExecutionContext(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateErr = errors.New("checkpoint failed")
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	var updateErr error
	acted := false
	steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
		updateErr = ctl.Update(func() {})
		if updateErr != nil {
			return updateErr
		}
		acted = true
		return nil
	})}
	manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
	if !errors.Is(updateErr, store.updateErr) || acted {
		t.Fatalf("update error = %v, external action performed = %v", updateErr, acted)
	}
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
		{name: "open succeeded", complete: true, result: resultSucceeded, wantError: true},
		{name: "closed succeeded", closed: true, complete: true, result: resultSucceeded, wantError: true},
		{name: "closed canceled", closed: true, complete: true, result: resultCanceled, wantError: true},
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
				ID: 42, Closed: test.closed, RecordState: test.recordState, SavedStatus: resultFailed,
				Message: "Unreadable saved release.", Guidance: "Open the tracking item.",
				Err: errors.New("unreadable metadata"),
			}
			addTrackingError(&dashboard, problem)
			addTrackingError(&dashboard, problem)
			var summary webview.ReleaseSummary
			if test.closed {
				if len(dashboard.Recent) != 1 || len(dashboard.Ongoing) != 0 {
					t.Fatalf("closed grouping = %#v", dashboard)
				}
				summary = dashboard.Recent[0]
			} else {
				if len(dashboard.Ongoing) != 1 || len(dashboard.Recent) != 0 {
					t.Fatalf("open grouping = %#v", dashboard)
				}
				summary = dashboard.Ongoing[0]
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
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	checkpointed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); <-done })
	steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(ctx context.Context, ctl coordinator.StepControl) error {
		ctl.ReportProgress(coordinator.StepProgress{Completed: 5, Total: 30})
		if err := ctl.Update(func() {}); err != nil {
			return err
		}
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
	if !record.Run.Checkpointed || record.Run.Complete {
		t.Fatalf("checkpoint was not saved as unfinished: %#v", record.Run)
	}
	graph := store.view.Graph
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
	if !record.Run.Complete || store.view.Graph.Steps[0].Status != coordinator.StepStatusSucceeded {
		t.Fatalf("final saved graph = %#v", record.Run)
	}
}

func TestFinalViewValidationFailureIsVisible(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	session.runner = &coordinator.StepRunner{}
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
		ctl.ReportProgress(coordinator.StepProgress{RunURL: "javascript:alert(1)"})
		return nil
	})}
	manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
	current, ok := manager.Current("example")
	if !ok || manager.Running("example") || current.Execution.Run.Result != resultUncertain ||
		!strings.Contains(session.runner.Snapshot().Error, "invalid HTTPS link") || store.updates.Load() != 0 {

		t.Fatalf("invalid display produced a success-shaped result: %#v", current)
	}
}

func TestCheckpointWaitsForPersistence(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	returned := make(chan struct{})
	done := make(chan struct{})
	steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
		err := ctl.Update(func() {})
		close(returned)
		return err
	})}
	go func() {
		defer close(done)
		manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
	}()
	waitForTestSignal(t, store.updateStarted)
	select {
	case <-returned:
		t.Fatal("Update returned before persistence completed")
	default:
	}
	close(release)
	waitForTestSignal(t, returned)
	waitForTestSignal(t, done)
}

func TestPersistenceDoesNotBlockReads(t *testing.T) {
	for _, phase := range []string{"checkpoint", "completion"} {
		t.Run(phase, func(t *testing.T) {
			manager, session, run, store, cancel := checkpointTestRun(t)
			defer cancel()
			if phase == "completion" {
				session.runner = &coordinator.StepRunner{}
			}
			store.updateStarted = make(chan struct{})
			release := make(chan struct{})
			store.updateRelease = release
			checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
			done := make(chan struct{})
			go func() {
				defer close(done)
				steps := []*coordinator.Step{
					coordinator.NewRootStep(
						"Work", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
							if phase == "checkpoint" {
								return ctl.Update(func() {})
							}
							return nil
						},
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
	session.runner = &coordinator.StepRunner{}
	store.updateErr = errors.New("revision changed during final persistence")
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	steps := []*coordinator.Step{
		coordinator.NewRootStep("finish", time.Minute, func(context.Context, coordinator.StepControl) error { return nil }),
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
			checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
			done := make(chan struct{})
			go func() {
				saveTestCheckpoint(checkpointer)
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
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	firstDone := make(chan struct{})
	go func() {
		saveTestCheckpoint(checkpointer)
		close(firstDone)
	}()
	waitForTestSignal(t, store.updateStarted)
	secondDone := make(chan struct{})
	go func() {
		saveTestCheckpoint(checkpointer)
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

func TestUpdatesCaptureAndPersistInMutationOrder(t *testing.T) {
	manager, session, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	session.runner = &coordinator.StepRunner{}
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
	secondAttempted := make(chan struct{})
	first := coordinator.NewRootStep("First", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
		return ctl.Update(func() { run.snapshot.State = json.RawMessage(`{"value":1}`) })
	})
	second := coordinator.NewRootStep("Second", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
		<-store.updateStarted
		close(secondAttempted)
		return ctl.Update(func() { run.snapshot.State = json.RawMessage(`{"value":2}`) })
	})
	done := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); <-done })
	go func() {
		defer close(done)
		manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner,
			[]*coordinator.Step{first, second}, run, checkpointer)
	}()
	waitForTestSignal(t, store.updateStarted)
	waitForTestSignal(t, secondAttempted)
	session.runner.ReadState(func() {
		if got := string(run.snapshot.State); got != `{"value":1}` {
			t.Errorf("second update changed state before the first was saved: %s", got)
		}
	})
	unblock()
	waitForTestSignal(t, done)
	store.mu.Lock()
	defer store.mu.Unlock()
	want := []string{`{"value":1}`, `{"value":2}`, `{"value":2}`}
	if !slices.Equal(store.savedStates, want) {
		t.Fatalf("persisted states = %v, want %v", store.savedStates, want)
	}
}

func TestSnapshotFailureNeverReplacesLastValidCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name    string
		update  bool
		capture bool
	}{
		{name: "invalid update", update: true},
		{name: "invalid final snapshot"},
		{name: "update capture error", update: true, capture: true},
		{name: "final capture error", capture: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, session, run, store, cancel := checkpointTestRun(t)
			defer cancel()
			failure := errors.New("negative work count")
			if test.capture {
				failure = errors.New("snapshot capture failed")
			}
			validate := func(saved *contract.StateSnapshot) error {
				_, state, err := snapshot.Decode[struct{}, struct {
					Value int `json:"value"`
				}](saved)
				if err != nil {
					return err
				}
				if state.Value < 0 {
					return failure
				}
				return nil
			}
			registry, err := processregistry.New(viewingTestGroup{
				process: viewingTestProcess{run: run, validate: validate},
			})
			if err != nil {
				t.Fatal(err)
			}
			manager.processes = registry
			checkpointer := manager.newProcessCheckpointer(session, "example", session.state.Digest, run, cancel)
			acted := false
			var updateErr error
			steps := []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(_ context.Context, ctl coordinator.StepControl) error {
				if err := ctl.Update(func() {
					run.snapshot.State = json.RawMessage(`{"value":1}`)
					run.view.Detail = "Valid state"
				}); err != nil {
					return err
				}
				mutate := func() {
					run.snapshot.State = json.RawMessage(`{"value":-1}`)
					run.view.Detail = "Invalid state"
					if test.capture {
						run.snapshotErr = failure
					}
				}
				if test.update {
					updateErr = ctl.Update(mutate)
					if updateErr != nil {
						return updateErr
					}
					acted = true
				} else {
					mutate()
				}
				return nil
			})}
			manager.executeProcessRun(session, session.state.Digest, manager.ctx, session.runner, steps, run, checkpointer)
			if test.update && (!errors.Is(updateErr, failure) || acted) {
				t.Fatalf("update error = %v, subsequent action performed = %v", updateErr, acted)
			}
			if !strings.Contains(session.runner.Snapshot().Error, failure.Error()) {
				t.Fatalf("snapshot failure is not visible: %#v", session.runner.Snapshot())
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			want := []string{`{"value":1}`, `{"value":1}`}
			if !slices.Equal(store.savedStates, want) || store.record.Run.Result != resultUncertain ||
				store.view.Detail != "Valid state" {
				t.Fatalf("saved states = %v, outcome = %s, detail = %q", store.savedStates, store.record.Run.Result, store.view.Detail)
			}
		})
	}
}

func TestInvalidPreparedStateRejectedBeforeLoading(t *testing.T) {
	manager, _, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	failure := errors.New("invalid prepared state")
	var loads atomic.Int32
	process := viewingTestProcess{
		run: run, loads: &loads,
		validate: func(*contract.StateSnapshot) error { return failure },
	}
	registry, err := processregistry.New(viewingTestGroup{process: process})
	if err != nil {
		t.Fatal(err)
	}
	manager.processes = registry
	_, err = manager.Prepare(context.Background(), "example", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), failure.Error()) || loads.Load() != 0 || store.updates.Load() != 0 {
		t.Fatalf("prepare error=%v, loads=%d, writes=%d", err, loads.Load(), store.updates.Load())
	}
	saved, err := run.TakeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadReleaseRun(process, saved); !errors.Is(err, failure) || loads.Load() != 0 {
		t.Fatalf("load error=%v, loads=%d", err, loads.Load())
	}
}

func TestStartReturnsSnapshotCaptureError(t *testing.T) {
	manager, _, run, store, cancel := checkpointTestRun(t)
	defer cancel()
	run.build = func() ([]*coordinator.Step, error) {
		return []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(context.Context, coordinator.StepControl) error {
			return nil
		})}, nil
	}
	prepared, err := manager.Prepare(context.Background(), "example", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("snapshot encoding failed")
	run.snapshotErr = failure
	err = manager.Start(context.Background(), "example", webview.ReleaseStartRequest{
		PlanDigest: prepared.Execution.PlanDigest, Confirmed: true,
	})
	if !errors.Is(err, failure) || manager.Running("example") || store.updates.Load() != 0 {
		t.Fatalf("start error=%v, running=%v, writes=%d", err, manager.Running("example"), store.updates.Load())
	}
}

func TestSnapshotValidationCannotMutatePayload(t *testing.T) {
	original := &contract.StateSnapshot{Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`)}
	process := viewingTestProcess{validate: func(saved *contract.StateSnapshot) error {
		saved.State = json.RawMessage(`{"changed":true}`)
		return nil
	}}
	if err := validateProcessSnapshot(process, original); err == nil {
		t.Fatal("mutating validator was accepted")
	}
	if string(original.State) != `{}` {
		t.Fatal("validator changed the original snapshot")
	}
}

func saveTestCheckpoint(c *processCheckpointer) error {
	persist, err := c.Capture()
	if err != nil {
		return err
	}
	return persist()
}

func checkpointTestRun(
	t *testing.T,
) (*Manager, *processSession, *testRun, *testStore, context.CancelFunc) {
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
		view:     &contract.RunView{Summary: "Running"},
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
	registry, err := processregistry.New(viewingTestGroup{process: viewingTestProcess{run: run}})
	if err != nil {
		t.Fatal(err)
	}
	manager.processes = registry
	runner, err := coordinator.NewStepRunner([]*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(context.Context, coordinator.StepControl) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	session.runner = runner
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
