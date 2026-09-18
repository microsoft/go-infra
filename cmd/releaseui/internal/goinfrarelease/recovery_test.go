// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfrarelease

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
)

func TestGoInfraDispatchSuccessWithoutExternalRunFailsPolicyValidation(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	process := NewProcess(github.integration())
	run := testStoredGoInfraRun(t, github.integration(), goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	})
	run.Started = true
	run.Complete = true
	run.Result = "succeeded"
	if _, err := process.Restore(run); err == nil {
		t.Fatal("dispatch success without a workflow run was accepted")
	}
}

func TestInterruptedGoInfraRunRestoresUncertain(t *testing.T) {
	store := newMemoryProcessRunStore()
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	run := testStoredGoInfraRun(t, github.integration(), goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish,
	})
	run.Started = true
	workItemID := store.seed(t, run)
	ui := newGoInfraTestUI(t, github.integration(),
		releaseui.WithReleaseRunStore(store),
		releaseui.WithReleaseWorkItem(testProcessWorkItem(t, workItemID, run)),
	)
	response := getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	var restored goInfraTestPlanResponse
	decodeResponse(t, response, &restored)
	if response.StatusCode != http.StatusOK || !restored.Execution.Run.Complete || len(restored.Steps) != 1 ||
		restored.Steps[0].Status != "failed" {

		t.Fatalf("restored = %#v", restored)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusConflict {
		t.Fatalf("replacement plan status = %d", status)
	}
	stats := github.stats()
	if len(stats.dispatches) != 0 || stats.labelCalls != 0 {
		t.Fatalf("dispatches = %v, label calls = %d", stats.dispatches, stats.labelCalls)
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != "uncertain" {
		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestDiscoveredGoInfraRunResumesMonitoring(t *testing.T) {
	store := newMemoryProcessRunStore()
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	run := testStoredGoInfraRun(t, github.integration(), goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	})
	queued := testGoInfraWorkflowRun("queued", "")
	state, err := json.Marshal(queued)
	if err != nil {
		t.Fatal(err)
	}
	run.Started = true
	run.Checkpoint = state
	external := goInfraExternalRun(queued)
	run.External = &external
	workItemID := store.seed(t, run)
	github.mu.Lock()
	github.workflowRun = queued
	github.workflowUpdates = []GoInfraWorkflowRun{
		testGoInfraWorkflowRun("in_progress", ""),
		testGoInfraWorkflowRun("completed", "success"),
	}
	github.mu.Unlock()
	ui := newGoInfraTestUI(t, github.integration(),
		releaseui.WithReleaseRunStore(store),
		releaseui.WithReleaseWorkItem(testProcessWorkItem(t, workItemID, run)),
	)
	persisted := waitForGoInfraAction(t, store)
	stats := github.stats()
	if stats.pollCalls != 1 || len(stats.dispatches) != 0 {
		t.Fatalf("poll calls = %d, dispatches = %v", stats.pollCalls, stats.dispatches)
	}
	if !persisted.Complete || persisted.Result != "succeeded" || persisted.External == nil ||
		persisted.External.Status != "completed" || !persisted.External.Succeeded {

		t.Fatalf("persisted = %#v", persisted)
	}
	response := getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	var restored goInfraTestPlanResponse
	decodeResponse(t, response, &restored)
	if response.StatusCode != http.StatusOK || !restored.Execution.Run.Complete ||
		restored.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {

		t.Fatalf("restored = %#v", restored)
	}
}

func TestGoInfraExecutionRequiresProcessRunStore(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newGoInfraTestUI(t, github.integration())
	response := getResponse(t, ui, "/api/processes/go-infra/preflight")
	defer response.Body.Close()
	var report releaseui.PreflightReport
	decodeResponse(t, response, &report)
	if response.StatusCode != http.StatusOK || !report.PlanningEnabled || report.ExternalExecutionEnabled {
		t.Fatalf("preflight = %#v", report)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d", response.StatusCode)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	stats := github.stats()
	if status != http.StatusForbidden || len(stats.dispatches) != 0 {
		t.Fatalf("start status = %d, dispatches = %v", status, stats.dispatches)
	}
}

func TestGoInfraRestoreSelectionRequiresProcessRunStore(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	run := testStoredGoInfraRun(t, github.integration(), goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	})
	run.Started = true
	_, err := releaseui.New(
		context.Background(),
		releaseui.WithProcesses(NewProcess(github.integration())),
		releaseui.WithReleaseWorkItem(testProcessWorkItem(t, 1, run)),
	)
	if err == nil || !strings.Contains(err.Error(), "requires a durable run store") {
		t.Fatalf("New error = %v", err)
	}
}

func testProcessWorkItem(t *testing.T, id int, run *contract.State) *azdoworkitem.WorkItem {
	t.Helper()
	payload, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	status := azdoworkitem.StatusStarting
	if run.External != nil {
		status = azdoworkitem.StatusRunning
	}
	return &azdoworkitem.WorkItem{
		ID: id, Revision: 1, URL: "https://example.invalid/workitems/1", State: "Active",
		ChangedAt: time.Now().UTC(),
		Snapshot: &azdoworkitem.Snapshot{
			SchemaVersion: azdoworkitem.CurrentSchemaVersion,
			ProcessID:     run.ProcessID,
			Status:        status,
			Test:          run.Test,
			IntentDigest:  run.Digest,
			Payload:       payload,
		},
	}
}
