// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
)

func TestGoInfraDispatchSuccessWithoutExternalRunFailsPolicyValidation(t *testing.T) {
	run := testStoredGoInfraRun(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun}, nil)
	run.Started = true
	run.Complete = true
	run.Result = "succeeded"
	if err := validateGoInfraProcessRun(run); err == nil {
		t.Fatal("dispatch success without a workflow run was accepted")
	}
}

func TestInterruptedGoInfraRunRestoresUncertain(t *testing.T) {
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := testStoredGoInfraRun(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish}, nil)
	run.Started = true
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newTestUI(t, WithProcessRunStore(store), WithGoInfraGitHubIntegration(github.integration()))
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored processRunResponse
	decodeResponse(t, response, &restored)
	if response.StatusCode != http.StatusOK || !restored.Execution.Run.Complete || restored.Execution.Run.Result != "uncertain" {
		t.Fatalf("restored = %#v", restored)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("replacement plan status = %d", response.StatusCode)
	}
	if len(github.dispatches) != 0 || github.labelCalls != 0 {
		t.Fatalf("dispatches = %v, label calls = %d", github.dispatches, github.labelCalls)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Complete || persisted.Result != "uncertain" {
		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestDiscoveredGoInfraRunResumesMonitoring(t *testing.T) {
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := testStoredGoInfraRun(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun}, nil)
	queued := testGoInfraWorkflowRun("queued", "")
	state, err := json.Marshal(queued)
	if err != nil {
		t.Fatal(err)
	}
	run.Started = true
	run.Checkpoint = state
	external := goInfraExternalRun(queued)
	run.External = &external
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), workflowRun: queued,
		workflowUpdates: []GoInfraWorkflowRun{
			testGoInfraWorkflowRun("in_progress", ""),
			testGoInfraWorkflowRun("completed", "success"),
		},
	}
	ui := newTestUI(t, WithProcessRunStore(store), WithGoInfraGitHubIntegration(github.integration()))
	waitForGoInfraAction(t, ui)
	if github.pollCalls != 1 || len(github.dispatches) != 0 {
		t.Fatalf("poll calls = %d, dispatches = %v", github.pollCalls, github.dispatches)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Complete || persisted.Result != "succeeded" || persisted.External == nil ||
		persisted.External.Status != "completed" || !persisted.External.Succeeded {

		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestGoInfraExecutorRequiresProcessRunStore(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	if _, err := New(context.Background(), WithGoInfraGitHubIntegration(github.integration())); err == nil {
		t.Fatal("go-infra executor was enabled without a process run store")
	}
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), WithProcessRunStore(store)); err == nil {
		t.Fatal("process run store was enabled without an executor")
	}
	if _, err := New(
		context.Background(), WithProcessRunStore(store),
		WithGoInfraGitHubIntegration(GoInfraGitHubIntegration{}),
	); err == nil {
		t.Fatal("incomplete go-infra integration was accepted")
	}
}

func TestUncertainGoInfraRunDoesNotHideBehindGoImagesSession(t *testing.T) {
	runStore, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := testStoredGoInfraRun(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish}, nil)
	run.Started = true
	if err := runStore.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	sessionStore, err := goimagessession.NewFileStore(filepath.Join(t.TempDir(), "go-images-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(sessionStore), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, first, `{"mode":"normal"}`)
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	_, err = New(
		context.Background(), WithSessionStore(sessionStore), WithProcessRunStore(runStore),
		WithGoInfraGitHubIntegration(github.integration()),
	)
	if err == nil {
		t.Fatal("startup accepted simultaneous go-images and uncertain process state")
	}
}

func testStoredGoInfraRun(t *testing.T, input goInfraPlanInput, pullRequest *GoInfraPullRequest) *ProcessRun {
	t.Helper()
	payload := goInfraProcessPayload{Input: input, PullRequest: pullRequest}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	run, err := newProcessRun(goInfraProcessID, ProcessPreparedRun{
		Input: inputJSON, Payload: payloadJSON,
		Step: goInfraProcessStep(payload), View: goInfraProcessView(payload), Target: goInfraProcessTarget(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}
