// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"context"
	"encoding/json"
	"errors"
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
				if dashboard.TrackingError != "" || len(dashboard.NeedsAttention) != 1 ||
					dashboard.NeedsAttention[0].Href != "/releases/1" {

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
