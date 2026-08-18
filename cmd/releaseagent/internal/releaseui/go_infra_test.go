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

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
)

const testGoInfraHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGoInfraGitHub struct {
	preflightCalls  int
	getPRCalls      int
	labelCalls      int
	dispatches      []bool
	dispatchStarted chan struct{}
	dispatchRelease <-chan struct{}
	pullRequest     GoInfraPullRequest
	preflightErr    error
	labelErr        error
	dispatchErr     error
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
		DispatchPatchRelease: func(_ context.Context, dryRun bool) error {
			f.dispatches = append(f.dispatches, dryRun)
			if f.dispatchStarted != nil {
				close(f.dispatchStarted)
			}
			if f.dispatchRelease != nil {
				<-f.dispatchRelease
			}
			return f.dispatchErr
		},
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
	store, err := NewGoInfraActionFileStore(filepath.Join(t.TempDir(), "go-infra-action.json"))
	if err != nil {
		t.Fatal(err)
	}
	return []Option{WithGoInfraActionStore(store), WithGoInfraGitHubIntegration(github.integration())}
}

func TestGoInfraPreflightDisabled(t *testing.T) {
	ui := newTestUI(t, WithExecutableLookup(func(name string) (string, error) {
		if name == "gh" {
			return "/test/bin/gh", nil
		}
		return "", errors.New("not found")
	}))
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

func TestGoInfraReleaseOnMergeRequiresExactConfirmation(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	options := append([]Option{WithExecutableLookup(func(string) (string, error) { return "/test/bin/gh", nil })}, testGoInfraOptions(t, github)...)
	ui := newTestUI(t, options...)
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
	var plan goInfraPlanResponse
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
	if !plan.Execution.Run.Complete || plan.Execution.Run.Result != "succeeded" || plan.Execution.Run.URL != github.pullRequest.URL {
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
			var plan goInfraPlanResponse
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
			if len(github.dispatches) != 1 || github.dispatches[0] != test.dryRun || github.labelCalls != 0 {
				t.Fatalf("dispatches = %v, label calls = %d", github.dispatches, github.labelCalls)
			}
		})
	}
}

func TestGoInfraStartRejectsRapidSecondConfirmation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), dispatchStarted: started, dispatchRelease: release,
	}
	ui := newTestUI(t, testGoInfraOptions(t, github)...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraPlanResponse
	decodeResponse(t, response, &plan)
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
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("second start status = %d", response.StatusCode)
	}
	close(release)
	waitForGoInfraAction(t, ui)
	if len(github.dispatches) != 1 {
		t.Fatalf("dispatches = %v", github.dispatches)
	}
}

func TestGoInfraMutationFailureIsTerminal(t *testing.T) {
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), dispatchErr: errors.New("fake dispatch rejected"),
	}
	options := testGoInfraOptions(t, github)
	ui := newTestUI(t, options...)
	response := postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	var plan goInfraPlanResponse
	decodeResponse(t, response, &plan)
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
	if !plan.Execution.Run.Complete || plan.Execution.Run.Result != "failed" {
		t.Fatalf("plan = %#v", plan)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || len(github.dispatches) != 1 {
		t.Fatalf("retry status = %d, dispatches = %v", response.StatusCode, github.dispatches)
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
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "go-images-session.json"))
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
	if _, err := store.Load(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("go-images session store was modified: %v", err)
	}
}

func waitForGoInfraAction(t *testing.T, ui *testUI) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		running := ui.server.goInfraRunning
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
