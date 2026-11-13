// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

type viewingTestGroup struct{ process contract.Process }

func (g viewingTestGroup) Identity() *contract.Identity {
	return &contract.Identity{Name: "Viewing tests", Mark: "VT"}
}
func (g viewingTestGroup) Processes() []contract.Process { return []contract.Process{g.process} }

type viewingTestProcess struct {
	run          *testRun
	loads        *atomic.Int32
	preflightErr error
	validate     func(*contract.StateSnapshot) error
}

func (p viewingTestProcess) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{ID: "example", Identity: contract.Identity{Name: "Test run"}}
}
func (p viewingTestProcess) Preflight(context.Context) (error, error) { return nil, p.preflightErr }
func (p viewingTestProcess) InputForm(*releaseinput.Set) any          { return nil }
func (p viewingTestProcess) Prepare(context.Context, any) (*contract.StateSnapshot, error) {
	return p.run.TakeSnapshot()
}

func (p viewingTestProcess) Validate(snapshot *contract.StateSnapshot) error {
	if p.validate != nil {
		return p.validate(snapshot)
	}
	return runstore.ValidateSnapshot(snapshot)
}

func (p viewingTestProcess) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	if p.loads != nil {
		p.loads.Add(1)
	}
	run := *p.run
	run.snapshot = runstore.CloneSnapshot(snapshot)
	return &run, nil
}

type dashboardTestReader struct {
	active    []*runstore.ViewRecord
	recent    []*runstore.ViewRecord
	activeErr error
	queryErr  error
}

func (*dashboardTestReader) GetView(context.Context, int) (*runstore.ViewRecord, error) {
	return nil, errors.New("unexpected individual view lookup")
}

func (r *dashboardTestReader) QueryViews(_ context.Context, closed bool, _ int) ([]*runstore.ViewRecord, error) {
	if closed {
		return r.recent, r.queryErr
	}
	return r.active, r.activeErr
}

func TestDashboardGroupsByRunAndTrackingState(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    runstore.Status
		executing bool
		finished  bool
	}{
		{name: "running", status: runstore.StatusRunning, executing: true},
		{name: "interrupted", status: runstore.StatusRunning},
		{name: "succeeded", status: runstore.StatusSucceeded, finished: true},
		{name: "failed", status: runstore.StatusFailed, finished: true},
		{name: "canceled", status: runstore.StatusCanceled, finished: true},
		{name: "uncertain", status: runstore.StatusUncertain, finished: true},
		{name: "unknown"},
	} {
		for _, closed := range []bool{false, true} {
			for _, unreadable := range []bool{false, true} {
				if test.status == "" && !unreadable {
					continue
				}
				t.Run(fmt.Sprintf("%s/closed=%t/unreadable=%t", test.name, closed, unreadable), func(t *testing.T) {
					manager, store, _, _, cancel := publishedTestManager(t, nil)
					defer cancel()
					reader := new(dashboardTestReader)
					if unreadable {
						err := &runstore.RecordError{
							ID: 1, Closed: closed, SavedStatus: string(test.status),
							Err: errors.New("unreadable published view"),
						}
						if closed {
							reader.queryErr = err
						} else {
							reader.activeErr = err
						}
					} else {
						view := store.view.Clone()
						view.Status, view.Executing = test.status, test.executing
						record := &runstore.ViewRecord{ID: 1, Revision: 3, Closed: closed, View: view}
						if closed {
							reader.recent = []*runstore.ViewRecord{record}
						} else {
							reader.active = []*runstore.ViewRecord{record}
						}
					}
					history := manager.History()
					history.reader = reader
					dashboard := history.Dashboard(context.Background())
					entries, other := dashboard.Ongoing, dashboard.Recent
					if closed && test.finished {
						entries, other = dashboard.Recent, dashboard.Ongoing
					}
					if len(entries) != 1 || len(other) != 0 || dashboard.TrackingError != "" {
						t.Fatalf("run and tracking state produced the wrong group: %#v", dashboard)
					}
					wantStatus := string(test.status)
					if !unreadable && test.status == runstore.StatusRunning && !test.executing {
						wantStatus = "saved"
					}
					entry := entries[0]
					if entry.Status != wantStatus || entry.Closed != closed || (entry.Issue != nil) != unreadable {
						t.Fatalf("grouping lost the outcome, tracking state or warning: %#v", entry)
					}
				})
			}
		}
	}
}

func TestDashboardListsNewestFirst(t *testing.T) {
	manager, store, _, _, cancel := publishedTestManager(t, nil)
	defer cancel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	completed := func(id int, updatedAt time.Time, closed bool) *runstore.ViewRecord {
		view := store.view.Clone()
		view.Status = runstore.StatusSucceeded
		view.UpdatedAt = updatedAt
		for index := range view.Graph.Steps {
			view.Graph.Steps[index].Status = coordinator.StepStatusSucceeded
		}
		return &runstore.ViewRecord{ID: id, Revision: 1, Closed: closed, View: view}
	}
	unreadable := func(id int, updatedAt time.Time, closed bool) error {
		return &runstore.RecordError{
			ID: id, Closed: closed, UpdatedAt: updatedAt, SavedStatus: resultSucceeded,
			Message: "Cannot open in this session.", Err: errors.New("unsupported prototype view"),
		}
	}
	history := manager.History()
	history.reader = &dashboardTestReader{
		active: []*runstore.ViewRecord{
			completed(1, start.Add(3*time.Hour), false),
			completed(2, start.Add(time.Hour), false),
		},
		recent: []*runstore.ViewRecord{completed(3, start.Add(2*time.Hour), true)},
		activeErr: errors.Join(
			unreadable(8, start.Add(5*time.Hour), false),
			unreadable(9, start, false),
		),
		queryErr: errors.Join(
			unreadable(5, start.Add(4*time.Hour), true),
			unreadable(4, start, true),
			unreadable(6, time.Time{}, true),
			unreadable(7, time.Time{}, true),
		),
	}
	dashboard := history.Dashboard(context.Background())
	for _, list := range []struct {
		name           string
		entries        []webview.ReleaseSummary
		wantIDs        []int
		wantUnreadable []int
	}{
		{name: "ongoing", entries: dashboard.Ongoing, wantIDs: []int{8, 1, 2, 9}, wantUnreadable: []int{8, 9}},
		{name: "recent", entries: dashboard.Recent, wantIDs: []int{5, 3, 4, 6, 7}, wantUnreadable: []int{5, 4, 6, 7}},
	} {
		var ids, unreadableIDs []int
		for _, entry := range list.entries {
			ids = append(ids, entry.RecordID)
			if entry.Issue != nil {
				unreadableIDs = append(unreadableIDs, entry.RecordID)
			}
		}
		if !reflect.DeepEqual(ids, list.wantIDs) || !reflect.DeepEqual(unreadableIDs, list.wantUnreadable) {
			t.Fatalf("%s IDs = %v, unreadable IDs = %v; want %v and %v",
				list.name, ids, unreadableIDs, list.wantIDs, list.wantUnreadable)
		}
	}
	if dashboard.TrackingError != "" {
		t.Fatalf("read-only entries became an unclassified error: %s", dashboard.TrackingError)
	}
}

func TestViewingUsesOnlyPublishedData(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured process", true: "unavailable process"}[foreign], func(t *testing.T) {
			manager, store, loads, executions, cancel := publishedTestManager(t, nil)
			defer cancel()
			// A valid display remains useful even when its execution payload cannot be loaded.
			store.record.Run = nil
			if foreign {
				store.view.ProcessID = "another-project"
			}
			before := store.view.Clone()
			for range 2 {
				response, err := manager.History().View(context.Background(), 1)
				if err != nil {
					t.Fatal(err)
				}
				if response.Record.View.Graph.Steps[0].Progress.Completed != 5 ||
					foreign && response.ResumeURL != "" {

					t.Fatalf("published view = %#v", response)
				}
				dashboard := manager.History().Dashboard(context.Background())
				if dashboard.TrackingError != "" || len(dashboard.Ongoing) != 1 ||
					dashboard.Ongoing[0].Href != "/releases/1" {

					t.Fatalf("dashboard = %#v", dashboard)
				}
			}
			if _, selected := manager.Current("example"); selected {
				t.Fatal("viewing selected an executor")
			}
			if loads.Load() != 0 || executions.Load() != 0 || store.updates.Load() != 0 ||
				!reflect.DeepEqual(before, store.view) {

				t.Fatal("viewing loaded process code, executed work or changed published data")
			}
		})
	}
}

func TestResumeRequiresConfirmationRevisionAndUnownedCheckpoint(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*runstore.Record, *webview.ReleaseStartRequest)
	}{
		{name: "confirmation", change: func(_ *runstore.Record, r *webview.ReleaseStartRequest) { r.Confirmed = false }},
		{name: "revision", change: func(_ *runstore.Record, r *webview.ReleaseStartRequest) { r.Revision-- }},
		{name: "digest", change: func(_ *runstore.Record, r *webview.ReleaseStartRequest) { r.PlanDigest = "different" }},
		{name: "closed", change: func(r *runstore.Record, _ *webview.ReleaseStartRequest) { r.Closed = true }},
		{name: "owned", change: func(r *runstore.Record, _ *webview.ReleaseStartRequest) { r.Run.Executing = true }},
		{name: "complete", change: func(r *runstore.Record, _ *webview.ReleaseStartRequest) {
			r.Run.Complete, r.Run.Result = true, resultSucceeded
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, store, _, executions, cancel := publishedTestManager(t, nil)
			defer cancel()
			request := webview.ReleaseStartRequest{RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true}
			test.change(store.record, &request)
			if err := manager.Resume(context.Background(), "example", request); err == nil {
				t.Fatal("unsafe resume accepted")
			}
			if executions.Load() != 0 || store.updates.Load() != 0 || manager.Running("example") {
				t.Fatal("rejected resume performed work")
			}
		})
	}
}

func TestResumeDoesNotReadPublishedViewAsExecutionState(t *testing.T) {
	manager, store, _, executions, cancel := publishedTestManager(t, nil)
	defer cancel()
	request := webview.ReleaseStartRequest{
		RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true,
	}
	store.view = nil
	if err := manager.Resume(context.Background(), "example", request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Running("example") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executions.Load() != 1 || manager.Running("example") {
		t.Fatal("execution required a stored display snapshot")
	}
	record, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Run.Complete || record.Run.Result != resultSucceeded {
		t.Fatalf("execution outcome = %#v", record.Run)
	}
}

func TestAbandonedReservationCanBeExplicitlyRepaired(t *testing.T) {
	manager, store, _, executions, cancel := publishedTestManager(t, nil)
	defer cancel()
	store.record.Run.Executing = true
	exported, err := manager.History().Export(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	exported.Run.Executing = false
	updated, err := manager.History().Import(context.Background(), 1, exported)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Run.Executing || updated.Revision != 4 || executions.Load() != 0 {
		t.Fatalf("explicit repair executed work or retained ownership: %#v", updated)
	}
}

func TestResumeChecksPreflightBeforeLoadingExecution(t *testing.T) {
	manager, store, loads, executions, cancel := publishedTestManager(t, nil)
	defer cancel()
	p := viewingTestProcess{run: &testRun{snapshot: store.record.Run.Snapshot}, loads: loads, preflightErr: errors.New("target rejected")}
	registry, err := processregistry.New(viewingTestGroup{process: p})
	if err != nil {
		t.Fatal(err)
	}
	manager.processes = registry
	err = manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
		RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "target rejected") || loads.Load() != 0 ||
		executions.Load() != 0 || store.updates.Load() != 0 {

		t.Fatalf("resume preflight error=%v", err)
	}
}

func TestRecordReservationPreventsSecondServerExecution(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	manager, store, _, executions, cancel := publishedTestManager(t, release)
	defer cancel()
	request := webview.ReleaseStartRequest{RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true}
	if err := manager.Resume(context.Background(), "example", request); err != nil {
		t.Fatal(err)
	}
	other := New(manager.ctx, manager.processes, store)
	record, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	request.Revision = record.Revision
	if err := other.Resume(context.Background(), "example", request); err == nil {
		t.Fatal("another server acquired a running record")
	}
	deadline := time.Now().Add(5 * time.Second)
	for executions.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions=%d", executions.Load())
	}
}

func TestRunnerPublishesProgressAndHeartbeatThroughCheckpointWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		manager, store, _, _, cancel := publishedTestManager(t, release)
		defer cancel()
		if err := manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
			RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true,
		}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		store.mu.Lock()
		originalCheckpoint := runstore.CloneSnapshot(store.record.Run.Snapshot)
		before := store.view.Clone()
		store.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		store.mu.Lock()
		published := store.view.Clone()
		checkpoint := runstore.CloneSnapshot(store.record.Run.Snapshot)
		store.mu.Unlock()
		if !published.UpdatedAt.After(before.UpdatedAt) || published.Graph.Steps[0].Progress.Completed != 6 ||
			!published.Executing || !reflect.DeepEqual(originalCheckpoint, checkpoint) {

			t.Fatalf("progress/heartbeat altered recovery state or was not published: %#v", published)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		store.mu.Lock()
		heartbeat := store.view.UpdatedAt
		store.mu.Unlock()
		if !heartbeat.After(published.UpdatedAt) {
			t.Fatal("unchanged graph did not publish runner liveness")
		}
		close(release)
		synctest.Wait()
		store.mu.Lock()
		defer store.mu.Unlock()
		if !store.record.Run.Complete || store.record.Run.Executing ||
			store.view.Status != runstore.StatusSucceeded || store.view.Executing ||
			store.view.Graph.Steps[0].Status != coordinator.StepStatusSucceeded {

			t.Fatalf("final published result=%#v, checkpoint=%#v", store.view, store.record.Run)
		}
	})
}

func TestDisplayFailureKeepsExecutionRunningAndRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		manager, store, _, _, cancel := publishedTestManager(t, release)
		defer cancel()
		if err := manager.Resume(context.Background(), "example", webview.ReleaseStartRequest{
			RecordID: 1, Revision: 3, PlanDigest: store.record.Run.Digest, Confirmed: true,
		}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		store.mu.Lock()
		store.updateErr = errors.New("view storage unavailable")
		store.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if !manager.Running("example") {
			t.Fatal("storage failure stopped execution")
		}
		session, _ := manager.processSession("example")
		if session.runner.Snapshot().CheckpointError == "" {
			t.Fatal("storage failure was not visible")
		}
		record, err := store.Get(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if !record.Run.Executing {
			t.Fatal("failed persistence silently released durable ownership")
		}
		store.mu.Lock()
		store.updateErr = nil
		store.mu.Unlock()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if session.runner.Snapshot().CheckpointError != "" {
			t.Fatal("successful retry did not clear the warning")
		}
		close(release)
		synctest.Wait()
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.record.Run.Result != resultSucceeded {
			t.Fatalf("recovered release outcome = %s", store.record.Run.Result)
		}
	})
}

func publishedTestManager(
	t *testing.T, release <-chan struct{},
) (*Manager, *testStore, *atomic.Int32, *atomic.Int32, context.CancelFunc) {
	t.Helper()
	loads, executions := new(atomic.Int32), new(atomic.Int32)
	snapshot := &contract.StateSnapshot{Input: json.RawMessage(`{}`), State: json.RawMessage(`{"saved":true}`)}
	run := &testRun{
		snapshot: snapshot, plan: &contract.Plan{Subtitle: "Published test"},
		view: &contract.RunView{Test: true, RunURL: "https://example.invalid/runs/888"},
	}
	run.build = func() ([]*coordinator.Step, error) {
		return []*coordinator.Step{coordinator.NewRootStep("Work", time.Minute, func(ctx context.Context, ctl coordinator.StepControl) error {
			executions.Add(1)
			ctl.ReportProgress(coordinator.StepProgress{Completed: 6, Total: 30, RunURL: "https://example.invalid/runs/888"})
			if release != nil {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})}, nil
	}
	state, err := runstore.NewState("example", snapshot, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	state.Started, state.Checkpointed = true, true
	store := &testStore{
		record: &runstore.Record{ID: 1, Revision: 3, UpdatedAt: time.Now().UTC(), Run: state},
		view: &runstore.View{
			ProcessID: "example", Name: "Published test", Mark: "PT", Digest: state.Digest,
			Status: runstore.StatusRunning, UpdatedAt: time.Now().UTC(), Graph: coordinator.Snapshot{Steps: []coordinator.StepSnapshot{
				{Name: "Work", Status: coordinator.StepStatusRunning, Progress: &coordinator.StepProgress{Completed: 5, Total: 30}},
			}},
		},
	}
	registry, err := processregistry.New(viewingTestGroup{process: viewingTestProcess{run: run, loads: loads}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return New(ctx, registry, store), store, loads, executions, cancel
}
