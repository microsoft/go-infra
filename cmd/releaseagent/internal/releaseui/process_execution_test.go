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
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

func TestDurableProcessUsesSharedLifecycle(t *testing.T) {
	store := newMemoryProcessRunStore()
	var executed bool
	var workItemCreatedBeforeExecution bool
	executor := ProcessExecutor{
		Preflight: func(context.Context) (string, error) { return "verified example", nil },
		Prepare: func(_ context.Context, input json.RawMessage) (ProcessPreparedRun, error) {
			return ProcessPreparedRun{
				Input: input, Payload: json.RawMessage(`{"value":"fixed"}`),
				Step: ProcessRunStep{Name: "Run example", Timeout: time.Minute},
				View: ProcessPlanView{
					IntentTitle: "Run example", ExecutionConfirmation: "Confirm example.",
					ExecutionButtonLabel: "Run example",
				},
				Target: ProcessRunReference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
			}, nil
		},
		Execute: func(ctx context.Context, payload json.RawMessage, checkpoint ProcessCheckpointFunc) error {
			executed = true
			workItemCreatedBeforeExecution = store.count() == 1
			return checkpoint(ctx, ProcessRunCheckpoint{
				State: json.RawMessage(`{"run":7}`),
				External: ProcessRunReference{
					ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
					Status: "completed", Terminal: true, Succeeded: true,
				},
				Progress: ProcessRunProgress{Summary: "Example completed", Completed: 1, Total: 1},
			})
		},
		Resume: func(context.Context, json.RawMessage, json.RawMessage, ProcessCheckpointFunc) error {
			t.Fatal("completed run was resumed")
			return nil
		},
		Validate: func(run *ProcessRun) error { return nil },
	}
	registry, err := newProcessRegistry(ProcessDefinition{
		ID: "example", Name: "Example", Mark: "EX", Description: "Example process",
		Workflow: ProcessWorkflow{
			Heading: "Run example", SubmitLabel: "Review", DurableAction: true,
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode",
				Options: []ProcessInputOption{{Value: "run", Name: "Run", Description: "Run example"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		ctx:              context.Background(),
		processes:        registry,
		processExecutors: map[string]ProcessExecutor{"example": executor},
		processRunStore:  store,
		runner:           &coordinator.StepRunner{},
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		running := server.processRunning
		server.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for example process")
		}
		time.Sleep(time.Millisecond)
	}
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
	executor := ProcessExecutor{
		Preflight: func(context.Context) (string, error) {
			preflightCalled = true
			return "verified", nil
		},
		Execute: func(context.Context, json.RawMessage, ProcessCheckpointFunc) error {
			executed = true
			return nil
		},
		Validate: func(*ProcessRun) error { return nil },
	}
	server := &Server{
		ctx: context.Background(), processExecutors: map[string]ProcessExecutor{"example": executor},
		processRunStore: store, processRun: run, runner: &coordinator.StepRunner{},
	}
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
	if preflightCalled || executed || server.processRun.Started || store.count() != 0 {
		t.Fatalf("preflight = %v, executed = %v, run = %#v, records = %d", preflightCalled, executed, server.processRun, store.count())
	}
}

func TestProcessRunTimeoutAutomaticallyResumesKnownRun(t *testing.T) {
	store := newMemoryProcessRunStore()
	run, err := newProcessRun("example", ProcessPreparedRun{
		Input: json.RawMessage(`{"mode":"test"}`), Payload: json.RawMessage(`{"value":"fixed"}`),
		Step: ProcessRunStep{Name: "Run example", Timeout: 5 * time.Millisecond},
		View: ProcessPlanView{
			IntentTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
		},
		Target: ProcessRunReference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run.Started = true
	workItemID := store.seed(run)
	record, err := store.Get(context.Background(), workItemID)
	if err != nil {
		t.Fatal(err)
	}
	resumeCalls := 0
	executor := ProcessExecutor{
		Resume: func(ctx context.Context, _, _ json.RawMessage, checkpoint ProcessCheckpointFunc) error {
			resumeCalls++
			return checkpoint(ctx, ProcessRunCheckpoint{
				State: json.RawMessage(`{"run":7,"status":"completed"}`),
				External: ProcessRunReference{
					ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7",
					Status: "completed", Terminal: true, Succeeded: true,
				},
			})
		},
		Validate: func(*ProcessRun) error { return nil },
	}
	server := &Server{
		ctx: context.Background(), processRunStore: store, processRun: run, processRunRecord: record,
		runner: &coordinator.StepRunner{}, processRunning: true,
	}
	step := processExecutionStep(run, func(ctx context.Context) error {
		if err := server.processCheckpoint(run.Digest, executor)(ctx, ProcessRunCheckpoint{
			State: json.RawMessage(`{"run":7,"status":"queued"}`),
			External: ProcessRunReference{
				ID: "7", URL: "https://example.com/runs/7", LinkLabel: "Open example run 7", Status: "queued",
			},
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	server.steps = []*coordinator.Step{step}
	server.executeProcessRun(run.Digest, server.runner, step, executor)

	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		running := server.processRunning
		server.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for resumed process run")
		}
		time.Sleep(time.Millisecond)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want 1", resumeCalls)
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != "succeeded" || persisted.External == nil || !persisted.External.Succeeded {
		t.Fatalf("persisted = %#v", persisted)
	}
}
