// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func exampleProcessDefinition() ProcessDefinition {
	return ProcessDefinition{
		ID: "example", Name: "Example", Mark: "EX", Description: "Example process",
		Workflow: ProcessWorkflow{
			Heading: "Run example", SubmitLabel: "Review",
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode",
				Options: []ProcessInputOption{{Value: "run", Name: "Run", Description: "Run example"}},
			}},
		},
	}
}

func examplePreparedRun(input json.RawMessage, timeout time.Duration) ReleasePlan {
	return ReleasePlan{
		Input: input, Payload: json.RawMessage(`{"value":"fixed"}`),
		Steps: []ReleaseStep{{Name: "Run example", Timeout: timeout}},
		View: ProcessPlanView{
			IntentTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
		},
		Target: ReleaseReference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	}
}

func exampleProcessSteps(run *ReleaseRunState, action func(context.Context) error) []*coordinator.Step {
	return []*coordinator.Step{coordinator.NewRootStep(run.Steps[0].Name, run.Steps[0].Timeout, action)}
}

func waitForProcessRun(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		running := server.processRunning
		server.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process run")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDurableProcessUsesSharedLifecycle(t *testing.T) {
	store := newMemoryProcessRunStore()
	var executed bool
	var workItemCreatedBeforeExecution bool
	process := &fakeProcess{
		definition: exampleProcessDefinition(),
		preflight: func(context.Context) (ProcessReadiness, error) {
			return ProcessReadiness{PlanningEnabled: true, ExecutionEnabled: true, Details: "verified example"}, nil
		},
		prepare: func(_ context.Context, input json.RawMessage) (ReleasePlan, error) {
			return examplePreparedRun(input, time.Minute), nil
		},
		build: func(_ context.Context, run *ReleaseRunState, checkpoint CheckpointFunc) ([]*coordinator.Step, error) {
			action := func(context.Context) error { return nil }
			if checkpoint != nil {
				action = func(ctx context.Context) error {
					executed = true
					workItemCreatedBeforeExecution = store.count() == 1
					return checkpoint(ctx, ReleaseCheckpoint{
						State: json.RawMessage(`{"run":7}`),
						External: &ReleaseReference{
							ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
							Status: "completed", Terminal: true, Succeeded: true,
						},
						Progress: ReleaseProgress{Summary: "Example completed", Completed: 1, Total: 1},
					})
				}
			}
			return exampleProcessSteps(run, action), nil
		},
	}
	server, err := New(
		context.Background(),
		WithProcesses(process),
		WithReleaseRunStore(store),
		WithDemoDelay(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{"mode":"run"}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", prepared, request)
	if prepared.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", prepared.Code, prepared.Body.String())
	}
	if count := store.count(); count != 0 {
		t.Fatalf("work item count after preparation = %d, want 0", count)
	}
	var plan processRunResponse
	if err := json.Unmarshal(prepared.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	started := httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost, "http://localhost/api/processes/example/start",
		strings.NewReader(`{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`),
	)
	request.Header.Set("Origin", "http://localhost")
	server.handleStartProcessRun("example", started, request)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", started.Code, started.Body.String())
	}
	waitForProcessRun(t, server)
	if !executed {
		t.Fatal("example process was not executed")
	}
	if !workItemCreatedBeforeExecution {
		t.Fatal("process executed before its release work item was created")
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != "succeeded" || persisted.External == nil || persisted.External.ID != "7" {
		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestProcessRunCreationFailurePreventsExecution(t *testing.T) {
	store := newMemoryProcessRunStore()
	store.createErr = errors.New("tracking unavailable")
	run := testProcessRun(t)
	preflightCalled := false
	executed := false
	process := &fakeProcess{
		definition: exampleProcessDefinition(),
		preflight: func(context.Context) (ProcessReadiness, error) {
			preflightCalled = true
			return ProcessReadiness{PlanningEnabled: true, ExecutionEnabled: true, Details: "verified"}, nil
		},
		build: func(_ context.Context, run *ReleaseRunState, _ CheckpointFunc) ([]*coordinator.Step, error) {
			return exampleProcessSteps(run, func(context.Context) error {
				executed = true
				return nil
			}), nil
		},
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	releaseRun, err := process.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	server.processRun = releaseRun
	server.steps = exampleProcessSteps(run, func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "http://localhost/api/processes/example/start",
		strings.NewReader(`{"planDigest":"`+run.Digest+`","confirmed":true}`),
	)
	request.Header.Set("Origin", "http://localhost")
	server.handleStartProcessRun("example", response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("start status = %d, body = %s", response.Code, response.Body.String())
	}
	if !preflightCalled || executed || server.processRun.Snapshot().Started || store.count() != 0 {
		t.Fatalf("preflight = %v, executed = %v, run = %#v, records = %d", preflightCalled, executed, server.processRun, store.count())
	}
}

func TestConcurrentProcessStartsCreateOneWorkItem(t *testing.T) {
	store := newMemoryProcessRunStore()
	preflightEntered := make(chan struct{})
	releasePreflight := make(chan struct{})
	var preflightCalls atomic.Int32
	process := &fakeProcess{
		definition: exampleProcessDefinition(),
		preflight: func(context.Context) (ProcessReadiness, error) {
			if preflightCalls.Add(1) == 1 {
				close(preflightEntered)
				<-releasePreflight
			}
			return ProcessReadiness{PlanningEnabled: true, ExecutionEnabled: true, Details: "verified"}, nil
		},
		prepare: func(_ context.Context, input json.RawMessage) (ReleasePlan, error) {
			return examplePreparedRun(input, time.Minute), nil
		},
		build: func(_ context.Context, run *ReleaseRunState, _ CheckpointFunc) ([]*coordinator.Step, error) {
			return exampleProcessSteps(run, func(context.Context) error { return nil }), nil
		},
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	prepared := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{"mode":"run"}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", prepared, request)
	var plan processRunResponse
	if err := json.Unmarshal(prepared.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}

	start := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost, "http://localhost/api/processes/example/start",
			strings.NewReader(`{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`),
		)
		request.Header.Set("Origin", "http://localhost")
		server.handleStartProcessRun("example", response, request)
		return response
	}
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstResult <- start() }()
	<-preflightEntered
	second := start()
	if second.Code != http.StatusConflict {
		t.Fatalf("second start status = %d, body = %s", second.Code, second.Body.String())
	}
	close(releasePreflight)
	first := <-firstResult
	if first.Code != http.StatusAccepted {
		t.Fatalf("first start status = %d, body = %s", first.Code, first.Body.String())
	}
	waitForProcessRun(t, server)
	if calls := preflightCalls.Load(); calls != 1 {
		t.Fatalf("preflight calls = %d, want 1", calls)
	}
	if count := store.count(); count != 1 {
		t.Fatalf("work item count = %d, want 1", count)
	}
}

func TestProcessRunTimeoutRestoresAndResumesKnownRun(t *testing.T) {
	store := newMemoryProcessRunStore()
	resumeCalls := 0
	process := &fakeProcess{
		definition: exampleProcessDefinition(),
		preflight: func(context.Context) (ProcessReadiness, error) {
			return ProcessReadiness{PlanningEnabled: true, ExecutionEnabled: true, Details: "verified"}, nil
		},
		prepare: func(_ context.Context, input json.RawMessage) (ReleasePlan, error) {
			return examplePreparedRun(input, 5*time.Millisecond), nil
		},
		build: func(_ context.Context, run *ReleaseRunState, checkpoint CheckpointFunc) ([]*coordinator.Step, error) {
			action := func(context.Context) error { return nil }
			switch {
			case checkpoint == nil:
			case len(run.Checkpoint) == 0:
				action = func(ctx context.Context) error {
					if err := checkpoint(ctx, ReleaseCheckpoint{
						State: json.RawMessage(`{"run":7,"status":"queued"}`),
						External: &ReleaseReference{
							ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7", Status: "queued",
						},
					}); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				}
			default:
				action = func(ctx context.Context) error {
					resumeCalls++
					return checkpoint(ctx, ReleaseCheckpoint{
						State: json.RawMessage(`{"run":7,"status":"completed"}`),
						External: &ReleaseReference{
							ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
							Status: "completed", Terminal: true, Succeeded: true,
						},
					})
				}
			}
			return exampleProcessSteps(run, action), nil
		},
	}
	first, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	prepared := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{"mode":"run"}`))
	request.Header.Set("Origin", "http://localhost")
	first.handlePrepareProcessRun("example", prepared, request)
	var plan processRunResponse
	if err := json.Unmarshal(prepared.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	started := httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPost, "http://localhost/api/processes/example/start",
		strings.NewReader(`{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`),
	)
	request.Header.Set("Origin", "http://localhost")
	first.handleStartProcessRun("example", started, request)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", started.Code, started.Body.String())
	}
	waitForProcessRun(t, first)
	interrupted := store.latest(t)
	if interrupted.Complete || len(interrupted.Checkpoint) == 0 || interrupted.External == nil {
		t.Fatalf("interrupted run = %#v", interrupted)
	}

	selected := testReleaseWorkItem(t, 1, interrupted, time.Now().UTC())
	store.mu.Lock()
	selected.Revision = store.records[1].Revision
	store.mu.Unlock()
	second, err := New(
		context.Background(), WithProcesses(process), WithReleaseRunStore(store), WithReleaseWorkItem(selected),
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessRun(t, second)
	if resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want 1", resumeCalls)
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != "succeeded" || persisted.External == nil || !persisted.External.Succeeded {
		t.Fatalf("persisted = %#v", persisted)
	}
}
