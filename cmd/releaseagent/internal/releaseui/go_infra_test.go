// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
)

const testGoInfraHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGoInfraGitHub struct {
	preflightCalls  int
	getPRCalls      int
	labelCalls      int
	dispatches      []bool
	dispatchStarted chan struct{}
	dispatchRelease <-chan struct{}
	pollCalls       int
	pollStarted     chan struct{}
	pollRelease     <-chan struct{}
	workflowRun     GoInfraWorkflowRun
	workflowUpdates []GoInfraWorkflowRun
	pullRequest     GoInfraPullRequest
	preflightErr    error
	labelErr        error
	dispatchErr     error
	pollErr         error
}

type goInfraTestPlanResponse struct {
	Input     goInfraPlanInput  `json:"input"`
	Steps     []planStep        `json:"steps"`
	SessionID string            `json:"sessionId"`
	Execution executionResponse `json:"execution"`
	View      ProcessPlanView   `json:"view"`
}

func (f *fakeGoInfraGitHub) integration() GoInfraGitHubIntegration {
	return GoInfraGitHubIntegration{
		Preflight: func(context.Context) (string, error) {
			f.preflightCalls++
			return "verified fake GitHub integration", f.preflightErr
		},
		GetPullRequest: func(_ context.Context, number int) (GoInfraPullRequest, error) {
			f.getPRCalls++
			if number != f.pullRequest.Number {
				return GoInfraPullRequest{}, errors.New("unexpected pull request")
			}
			return f.pullRequest, nil
		},
		AddReleaseOnMergeLabel: func(_ context.Context, number int, expectedHeadSHA string) (GoInfraPullRequest, error) {
			f.labelCalls++
			if number != f.pullRequest.Number || expectedHeadSHA != f.pullRequest.HeadSHA {
				return GoInfraPullRequest{}, errors.New("reviewed pull request changed")
			}
			return f.pullRequest, f.labelErr
		},
		DispatchPatchRelease: func(_ context.Context, dryRun bool) (GoInfraWorkflowRun, error) {
			f.dispatches = append(f.dispatches, dryRun)
			if f.dispatchStarted != nil {
				close(f.dispatchStarted)
			}
			if f.dispatchRelease != nil {
				<-f.dispatchRelease
			}
			if f.dispatchErr != nil {
				return GoInfraWorkflowRun{}, f.dispatchErr
			}
			if f.workflowRun.ID == 0 {
				f.workflowRun = testGoInfraWorkflowRun("queued", "")
			}
			return f.workflowRun, nil
		},
		PollWorkflowRun: func(_ context.Context, id int64, report func(GoInfraWorkflowRun) error) (GoInfraWorkflowRun, error) {
			f.pollCalls++
			if id != f.workflowRun.ID {
				return GoInfraWorkflowRun{}, errors.New("unexpected workflow run")
			}
			if f.pollStarted != nil {
				close(f.pollStarted)
			}
			if f.pollRelease != nil {
				<-f.pollRelease
			}
			updates := f.workflowUpdates
			if len(updates) == 0 {
				updates = []GoInfraWorkflowRun{testGoInfraWorkflowRun("completed", "success")}
			}
			for _, update := range updates {
				if err := report(update); err != nil {
					return GoInfraWorkflowRun{}, err
				}
			}
			return updates[len(updates)-1], f.pollErr
		},
	}
}

func testGoInfraWorkflowRun(status, conclusion string) GoInfraWorkflowRun {
	return GoInfraWorkflowRun{
		ID: 123, URL: "https://github.com/microsoft/go-infra/actions/runs/123",
		Status: status, Conclusion: conclusion, HeadSHA: testGoInfraHeadSHA, CreatedAt: time.Now().UTC(),
	}
}

func testGoInfraPullRequest() GoInfraPullRequest {
	return GoInfraPullRequest{
		Number: 42, Title: "Prepare release", URL: "https://github.com/microsoft/go-infra/pull/42",
		BaseRef: goInfraDefaultRef, HeadRef: "release-change", HeadSHA: testGoInfraHeadSHA,
	}
}

func testGoInfraOptions(t *testing.T, github *fakeGoInfraGitHub) []Option {
	t.Helper()
	store, err := NewProcessRunFileStore(filepath.Join(t.TempDir(), "process-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	return []Option{WithProcessRunStore(store), WithGoInfraGitHubIntegration(github.integration())}
}

func TestGoInfraPreflightDisabled(t *testing.T) {
	ui := newTestUI(t)
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var report PreflightReport
	decodeResponse(t, response, &report)
	if response.StatusCode != http.StatusOK || report.PlanningEnabled || report.ExternalExecutionEnabled {
		t.Fatalf("report = %#v", report)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("plan status = %d", response.StatusCode)
	}
}

func TestGoInfraPlanningDoesNotRequireExecutionReadiness(t *testing.T) {
	github := &fakeGoInfraGitHub{preflightErr: errors.New("workflow contract is not ready")}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var preflight PreflightReport
	decodeResponse(t, response, &preflight)
	if response.StatusCode != http.StatusOK || !preflight.PlanningEnabled || preflight.ExternalExecutionEnabled {
		t.Fatalf("preflight = %#v", preflight)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK || len(plan.Execution.PlanDigest) != 64 || github.preflightCalls != 1 {
		t.Fatalf("status = %d, plan = %#v, preflight calls = %d", response.StatusCode, plan, github.preflightCalls)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForGoInfraAction(t, ui)
	if github.preflightCalls != 2 || len(github.dispatches) != 0 {
		t.Fatalf("preflight calls = %d, dispatches = %v", github.preflightCalls, github.dispatches)
	}
}

func TestGoInfraReleaseOnMergeRequiresExactConfirmation(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var preflight PreflightReport
	decodeResponse(t, response, &preflight)
	if !preflight.PlanningEnabled || !preflight.ExternalExecutionEnabled {
		t.Fatalf("preflight = %#v", preflight)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"release-on-merge","pullRequest":"42"}`)
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK || plan.Input.PullRequest != "42" || len(plan.Execution.PlanDigest) != 64 ||
		!plan.Execution.Eligible || len(plan.Steps) != 1 || github.getPRCalls != 1 {

		t.Fatalf("status = %d, plan = %#v, get calls = %d", response.StatusCode, plan, github.getPRCalls)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"wrong","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || github.labelCalls != 0 {
		t.Fatalf("wrong digest status = %d, label calls = %d", response.StatusCode, github.labelCalls)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || github.labelCalls != 0 {
		t.Fatalf("unconfirmed status = %d, label calls = %d", response.StatusCode, github.labelCalls)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForGoInfraAction(t, ui)
	if github.labelCalls != 1 || len(github.dispatches) != 0 {
		t.Fatalf("label calls = %d, dispatches = %v", github.labelCalls, github.dispatches)
	}

	response, err = ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || plan.Execution.Run.URL != github.pullRequest.URL ||
		len(plan.Steps) != 1 || plan.Steps[0].Status != "succeeded" {

		t.Fatalf("completed plan = %#v", plan)
	}
}

func TestGoInfraWorkflowDispatchModes(t *testing.T) {
	for _, test := range []struct {
		mode   string
		dryRun bool
	}{
		{mode: goInfraDispatchModeDryRun, dryRun: true},
		{mode: goInfraDispatchModePublish, dryRun: false},
	} {
		t.Run(test.mode, func(t *testing.T) {
			github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
			ui := newTestUI(t, testGoInfraOptions(t, github)...)
			response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"`+test.mode+`"}`)
			var plan goInfraTestPlanResponse
			decodeResponse(t, response, &plan)
			if response.StatusCode != http.StatusOK || plan.Input.DispatchMode != test.mode || len(plan.Execution.PlanDigest) != 64 {
				t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
			}
			response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("start status = %d", response.StatusCode)
			}
			waitForGoInfraAction(t, ui)
			if len(github.dispatches) != 1 || github.dispatches[0] != test.dryRun || github.pollCalls != 1 || github.labelCalls != 0 {
				t.Fatalf("dispatches = %v, polls = %d, label calls = %d", github.dispatches, github.pollCalls, github.labelCalls)
			}
			response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
			if err != nil {
				t.Fatal(err)
			}
			decodeResponse(t, response, &plan)
			if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "succeeded" ||
				plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {

				t.Fatalf("completed plan = %#v", plan)
			}
		})
	}
}

func TestGoInfraStartRejectsRapidSecondConfirmation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), pollStarted: started, pollRelease: release,
	}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response.Body.Close()
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("first start status = %d", response.StatusCode)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake dispatch to start")
	}
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, response, &plan)
	if plan.Execution.Run.Complete || plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {
		t.Fatalf("queued plan = %#v", plan)
	}
	response, err = ui.client.Get(ui.http.URL + "/api/processes/go-infra/state")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot coordinator.Snapshot
	decodeResponse(t, response, &snapshot)
	if !snapshot.Active || len(snapshot.Steps) != 1 || snapshot.Steps[0].Status != coordinator.StepStatusRunning {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("second start status = %d", response.StatusCode)
	}
	close(release)
	waitForGoInfraAction(t, ui)
	if len(github.dispatches) != 1 || github.pollCalls != 1 {
		t.Fatalf("dispatches = %v, polls = %d", github.dispatches, github.pollCalls)
	}
}

func TestGoInfraMutationFailureIsTerminal(t *testing.T) {
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), dispatchErr: errors.New("fake dispatch rejected"),
	}
	options := testGoInfraOptions(t, github)
	ui := newTestUI(t, options...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response.Body.Close()
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForGoInfraAction(t, ui)
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "failed" {
		t.Fatalf("plan = %#v", plan)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || len(github.dispatches) != 1 {
		t.Fatalf("retry status = %d, dispatches = %v", response.StatusCode, github.dispatches)
	}
}

func TestGoInfraWorkflowFailureIsTerminal(t *testing.T) {
	github := &fakeGoInfraGitHub{
		pullRequest:     testGoInfraPullRequest(),
		workflowUpdates: []GoInfraWorkflowRun{testGoInfraWorkflowRun("completed", "failure")},
		pollErr:         errors.New("workflow failed"),
	}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response.Body.Close()
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForGoInfraAction(t, ui)
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "failed" ||
		plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {

		t.Fatalf("plan = %#v", plan)
	}
}

func TestGoInfraPlanRejectsUnsafeInputs(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	for _, body := range []string{
		`{"action":"release-on-merge","pullRequest":"0"}`,
		`{"action":"release-on-merge","pullRequest":"42","dispatchMode":"publish"}`,
		`{"action":"manual-dispatch","pullRequest":"42","dispatchMode":"publish"}`,
		`{"action":"manual-dispatch","dispatchMode":"other"}`,
		`{"action":"other"}`,
	} {
		response := postJSON(t, ui, "/api/processes/go-infra/plan", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, response.StatusCode)
		}
	}
	if github.labelCalls != 0 || len(github.dispatches) != 0 {
		t.Fatalf("label calls = %d, dispatches = %v", github.labelCalls, github.dispatches)
	}
}

func TestGoInfraPlanDoesNotUseGoImagesSessionStore(t *testing.T) {
	store, err := goimagessession.NewFileStore(filepath.Join(t.TempDir(), "go-images-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	options := append([]Option{WithSessionStore(store)}, testGoInfraOptions(t, github)...)
	ui := newTestUI(t, options...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d", response.StatusCode)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, goimagessession.ErrNotFound) {
		t.Fatalf("go-images session store was modified: %v", err)
	}
}

func waitForGoInfraAction(t *testing.T, ui *testUI) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		running := ui.server.processRunning
		ui.server.mu.Unlock()
		if !running {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for go-infra action")
		case <-time.After(time.Millisecond):
		}
	}
}
