// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
)

type testUI struct {
	server *Server
	http   *httptest.Server
	client *http.Client
}

func newTestUI(t *testing.T, options ...Option) *testUI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	options = append([]Option{WithDemoDelay(0)}, options...)
	server, err := New(ctx, options...)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	launchURL, err := server.LaunchURL(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("launch status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if csp := response.Header.Get("Content-Security-Policy"); csp == "" {
		t.Fatal("launch response has no Content-Security-Policy")
	}

	t.Cleanup(func() {
		cancel()
		httpServer.Close()
	})
	return &testUI{server: server, http: httpServer, client: client}
}

func TestPersistAndRestorePlan(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := newTestUI(t, WithSessionStore(store))
	created := createTestPlan(t, first)
	if created.SessionID == "" || created.Restored {
		t.Fatalf("unexpected created plan metadata: %#v", created)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := persisted.State
	state.Day.ReleaseIssue = 42
	if err := first.server.checkpointReleaseState(context.Background(), &state); err != nil {
		t.Fatal(err)
	}

	second := newTestUI(t, WithSessionStore(store))
	response, err := second.client.Get(second.http.URL + "/api/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored planResponse
	decodeResponse(t, response, &restored)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restored plan status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !restored.Restored || restored.SessionID != created.SessionID {
		t.Fatalf("restored plan metadata = %#v, created = %#v", restored, created)
	}
	if len(restored.Steps) != len(created.Steps) {
		t.Fatalf("restored step count = %d, want %d", len(restored.Steps), len(created.Steps))
	}
	second.server.mu.Lock()
	restoredReleaseIssue := second.server.releaseState.Day.ReleaseIssue
	second.server.mu.Unlock()
	if restoredReleaseIssue != 42 {
		t.Fatalf("restored release issue = %d, want 42", restoredReleaseIssue)
	}
}

func TestPreflightIsLocalAndExternalExecutionDisabled(t *testing.T) {
	var lookedUp []string
	ui := newTestUI(t, WithExecutableLookup(func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		if name == "gh" {
			return "/test/bin/gh", nil
		}
		return "", errors.New("not found")
	}))
	response, err := ui.client.Get(ui.http.URL + "/api/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var report PreflightReport
	decodeResponse(t, response, &report)
	if report.ExternalExecutionEnabled {
		t.Fatal("external execution unexpectedly enabled")
	}
	if strings.Join(lookedUp, ",") != "gh,az" {
		t.Fatalf("looked up executables %q, want gh,az", lookedUp)
	}
	statusByID := make(map[string]CheckStatus)
	for _, check := range report.Checks {
		statusByID[check.ID] = check.Status
	}
	if statusByID["github-cli"] != CheckStatusPassed || statusByID["azure-cli"] != CheckStatusWarning || statusByID["external-execution"] != CheckStatusUnavailable {
		t.Fatalf("unexpected preflight statuses: %#v", statusByID)
	}
}

type fakeSmokeService struct {
	queued int
	polled int
}

func (s *fakeSmokeService) TriggerBuildPipeline(_ context.Context, pipelineID int, parameters, optionalParameters map[string]string, _ *releasesteps.Secret) (string, error) {
	s.queued++
	if pipelineID != goImagesReleasePipelineID {
		return "", fmt.Errorf("unexpected pipeline %d", pipelineID)
	}
	if parameters["approveAheadOfTime"] != "true" ||
		parameters["runGoImagesBuild"] != "false" ||
		parameters["runPublishAnnouncement"] != "false" ||
		parameters["runUpdateDL"] != "false" ||
		parameters["runGoImageVersionCheck"] != "false" ||
		parameters["releaseIssue"] != "nil" {

		return "", fmt.Errorf("unsafe smoke parameters: %#v", parameters)
	}
	if len(optionalParameters) != 0 {
		return "", fmt.Errorf("unexpected optional parameters: %#v", optionalParameters)
	}
	return "321", nil
}

func (s *fakeSmokeService) PollPipelineComplete(_ context.Context, buildID string, _ *releasesteps.Secret) error {
	s.polled++
	if buildID != "321" {
		return fmt.Errorf("unexpected build ID %q", buildID)
	}
	return nil
}

func TestSmokeExecutionRequiresConfirmedSafePlan(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeSmokeService{}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesSmokeExecution(GoImagesSmokeExecution{
			DefinitionID:  goImagesReleasePipelineID,
			VariableGroup: "test-release-config",
			Preflight: func(context.Context) (string, error) {
				return "fake preflight passed", nil
			},
			NewService: func(string) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	plan := createTestPlan(t, ui)
	if !plan.SmokeTest {
		t.Fatal("plan is not marked as a smoke test")
	}
	for _, name := range []string{
		"runGoImagesBuild",
		"runPublishAnnouncement",
		"runUpdateDL",
		"runGoImageVersionCheck",
	} {
		if plan.Pipeline.Parameters[name] != "false" {
			t.Fatalf("%s = %q, want false", name, plan.Pipeline.Parameters[name])
		}
	}
	if plan.Pipeline.Parameters["approveAheadOfTime"] != "true" || plan.Pipeline.Parameters["releaseIssue"] != "nil" {
		t.Fatalf("unsafe smoke parameters: %#v", plan.Pipeline.Parameters)
	}

	response := postJSON(t, ui, "/api/go-images/smoke/start", `{"planDigest":"wrong","confirmation":"QUEUE PIPELINE 1151 SMOKE TEST"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 0 {
		t.Fatalf("wrong-digest status = %d, queued = %d", response.StatusCode, service.queued)
	}

	body, err := json.Marshal(smokeStartRequest{
		PlanDigest:   plan.PlanDigest,
		Confirmation: smokeConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, "/api/go-images/smoke/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("smoke start status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		complete := ui.server.document.State.Day.GoImagesReleaseComplete
		active := ui.server.externalRunning
		ui.server.mu.Unlock()
		if complete && !active {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for fake smoke execution")
		case <-time.After(time.Millisecond):
		}
	}
	if service.queued != 1 || service.polled != 1 {
		t.Fatalf("queued = %d, polled = %d; want 1 each", service.queued, service.polled)
	}
	response = postJSON(t, ui, "/api/go-images/smoke/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 1 {
		t.Fatalf("second-run status = %d, queued = %d", response.StatusCode, service.queued)
	}
}

func TestRetainedPlanCannotPerformExternalOperations(t *testing.T) {
	ui := newTestUI(t)
	createTestPlan(t, ui)

	ui.server.mu.Lock()
	steps := append([]*coordinator.Step(nil), ui.server.steps...)
	ui.server.mu.Unlock()
	var runner coordinator.StepRunner
	err := runner.Execute(context.Background(), steps)
	if !errors.Is(err, ErrExternalExecutionDisabled) {
		t.Fatalf("original graph execution error = %v, want ErrExternalExecutionDisabled", err)
	}
}

func TestAuthenticationAndHostValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "example.com"
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("non-loopback Host status = %d, want %d", response.StatusCode, http.StatusMisdirectedRequest)
	}
}

func TestCreatePlanAndRunDemo(t *testing.T) {
	ui := newTestUI(t)
	plan := createTestPlan(t, ui)
	if !plan.DemoOnly {
		t.Fatal("plan is not marked as demo-only")
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("plan has %d steps, want focused three-step go-images graph", len(plan.Steps))
	}
	if plan.Pipeline.DefinitionID != goImagesReleasePipelineID {
		t.Fatalf("pipeline ID = %d, want %d", plan.Pipeline.DefinitionID, goImagesReleasePipelineID)
	}
	if plan.Pipeline.Parameters["runGoImagesBuild"] != "true" ||
		plan.Pipeline.Parameters["runPublishAnnouncement"] != "false" ||
		plan.Pipeline.Parameters["runUpdateDL"] != "false" {

		t.Fatalf("unexpected focused pipeline parameters: %#v", plan.Pipeline.Parameters)
	}
	ids := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		if _, ok := ids[step.ID]; ok {
			t.Fatalf("duplicate step ID %q", step.ID)
		}
		ids[step.ID] = struct{}{}
	}

	response := postJSON(t, ui, "/api/demo/start", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start demo status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for demo to finish")
		case <-ticker.C:
			response, err := ui.client.Get(ui.http.URL + "/api/state")
			if err != nil {
				t.Fatal(err)
			}
			var snapshot coordinator.Snapshot
			decodeResponse(t, response, &snapshot)
			if snapshot.Active || len(snapshot.Steps) == 0 {
				continue
			}
			for _, step := range snapshot.Steps {
				if step.Status != coordinator.StepStatusSucceeded {
					t.Fatalf("step %q status = %q, want succeeded", step.ID, step.Status)
				}
			}
			return
		}
	}
}

func TestEventsSendInitialSnapshot(t *testing.T) {
	ui := newTestUI(t)
	createTestPlan(t, ui)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ui.http.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := ui.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}

	reader := bufio.NewReader(response.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		event.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if !strings.Contains(event.String(), "event: state") || !strings.Contains(event.String(), `"sequence":`) {
		t.Fatalf("unexpected initial server event: %q", event.String())
	}
}

func TestPlanRejectsCrossOriginAndInvalidInput(t *testing.T) {
	ui := newTestUI(t)

	request, err := http.NewRequest(http.MethodPost, ui.http.URL+"/api/plan", strings.NewReader(`{"versions":["1.26.1-1"]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.com")
	response, err := ui.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	response = postJSON(t, ui, "/api/plan", `{"versions":["not-a-version"],"runner":"ghost","variableGroup":"test-group"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid input status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func createTestPlan(t *testing.T, ui *testUI) planResponse {
	t.Helper()
	response := postJSON(t, ui, "/api/plan", `{"versions":["1.26.1-1","1.27rc1-1"],"runner":"ghost","security":false,"variableGroup":"test-release-config"}`)
	var plan planResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	return plan
}

func postJSON(t *testing.T, ui *testUI, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ui.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", ui.http.URL)
	response, err := ui.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("decode response: %v; remaining body: %q", err, body)
	}
}
