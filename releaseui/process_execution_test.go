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

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func exampleProcess() *fakeProcess {
	process := &fakeProcess{
		definition: testProcessDefinition("example"),
		plan: &contract.Plan{
			Subtitle: "Run example", ExecutionButtonLabel: "Run example",
			Facts: []contract.PlanFact{{Label: "Value", Value: "fixed"}},
		},
		view: &contract.RunView{Summary: "Ready"},
	}
	process.prepare = func(context.Context, any) (*contract.StateSnapshot, error) {
		return &contract.StateSnapshot{
			Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
		}, nil
	}
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error { return nil }), nil
	}
	return process
}

func exampleProcessSteps(action func(context.Context) error) []*coordinator.Step {
	return []*coordinator.Step{coordinator.NewRootStep("Run example", time.Minute, action)}
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

func prepareExample(t *testing.T, server *Server) processRunResponse {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", response.Code, response.Body.String())
	}
	var plan processRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func startExample(t *testing.T, server *Server, digest string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "http://localhost/api/processes/example/start",
		strings.NewReader(`{"planDigest":"`+digest+`","confirmed":true}`),
	)
	request.Header.Set("Origin", "http://localhost")
	server.handleStartProcessRun("example", response, request)
	return response
}

func TestProcessPlanIsReadBeforeBuildAndExecutionUsesFreshRun(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	var builds atomic.Int32
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		run *fakeReleaseRun,
		checkpoint contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		builds.Add(1)
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			run.setState(json.RawMessage(`{"value":"executed"}`))
			checkpoint()
			return nil
		}), nil
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	plan := prepareExample(t, server)
	if plan.Plan == nil || plan.Plan.Subtitle != "Run example" || builds.Load() != 1 {
		t.Fatalf("plan = %#v, builds = %d", plan.Plan, builds.Load())
	}
	response := startExample(t, server, plan.Execution.PlanDigest)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", response.Code, response.Body.String())
	}
	waitForProcessRun(t, server)
	if !executed.Load() || builds.Load() != 2 {
		t.Fatalf("executed = %v, builds = %d", executed.Load(), builds.Load())
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != resultSucceeded ||
		string(persisted.Snapshot.State) != `{"value":"executed"}` {

		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestBlockingPreflightPreventsPreparation(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	process.preflight = func(context.Context) (error, error) {
		return nil, errors.New("not configured")
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusPreconditionFailed || store.count() != 0 {
		t.Fatalf("prepare status = %d, records = %d", response.Code, store.count())
	}
}

func TestProcessRunCreationFailurePreventsExecution(t *testing.T) {
	store := newMemoryProcessRunStore()
	store.createErr = errors.New("tracking unavailable")
	process := exampleProcess()
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			return nil
		}), nil
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	plan := prepareExample(t, server)
	response := startExample(t, server, plan.Execution.PlanDigest)
	if response.Code != http.StatusInternalServerError || executed.Load() || store.count() != 0 {
		t.Fatalf("status = %d, executed = %v, records = %d", response.Code, executed.Load(), store.count())
	}
}

func TestRestoreIncompleteProcessRunResumesExecution(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			return nil
		}), nil
	}

	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(context.Background(), WithProcesses(process), WithReleaseRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.restoreProcessRunRecord(record); err != nil {
		t.Fatal(err)
	}
	waitForProcessRun(t, server)
	if !executed.Load() || store.latest(t).Result != resultSucceeded {
		t.Fatalf("executed = %v, run = %#v", executed.Load(), store.latest(t))
	}
}

func TestCheckpointFailureCancelsExecutionContext(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}

	run, err := process.Load(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{
		ctx: ctx, processRun: run, processRunState: state,
		processRunRecord: record, processRunStore: store, processPlan: process.plan,
	}
	store.updateErr = errors.New("checkpoint failed")
	checkpointer := server.newProcessCheckpointer(state.Digest, run, cancel)
	checkpointer.Checkpoint()
	checkpointer.Close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want cancellation", ctx.Err())
	}
}

func TestCheckpointDoesNotWaitForPersistence(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	updateStarted := make(chan struct{})
	updateRelease := make(chan struct{})
	store.updateStarted = updateStarted
	store.updateRelease = updateRelease
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{
		ctx: ctx, processRun: run, processRunState: state,
		processRunRecord: record, processRunStore: store, processPlan: process.plan,
	}
	checkpointer := server.newProcessCheckpointer(state.Digest, run, cancel)
	returned := make(chan struct{})
	go func() {
		checkpointer.Checkpoint()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Checkpoint blocked")
	}
	select {
	case <-updateStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence did not start")
	}
	close(updateRelease)
	checkpointer.Close()
}
