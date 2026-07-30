// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

func TestUnofficialDemoOptionRequiresSafetyBoundaries(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	demo := GoImagesUnofficialDemoIntegration{
		DefinitionID:   goImagesUnofficialDemoID,
		Preflight:      func(context.Context) (string, error) { return "ok", nil },
		ValidateSource: func(context.Context, string) error { return nil },
		NewService: func(GoImagesUnofficialDemoRequest) (releasesteps.GoImagesReleaseService, error) {
			return &fakeUnofficialDemoService{}, nil
		},
	}
	if _, err := New(context.Background(), WithSessionStore(store), WithGoImagesUnofficialDemoIntegration(demo)); err == nil {
		t.Fatal("unofficial demo was enabled without read-only source selection")
	}
}

func TestReadOnlyIntegrationDoesNotExposeQueueEndpoint(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "verified", nil },
			FindRuns:     func(context.Context, []string) ([]PipelineRunCandidate, error) { return nil, nil },
			ValidateRun: func(context.Context, int, []string) (PipelineRunCandidate, error) {
				return PipelineRunCandidate{}, errors.New("not configured")
			},
			MonitorRun: func(context.Context, int, []string) error { return nil },
		}),
	)
	response, err := ui.client.Get(ui.http.URL + "/api/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var report PreflightReport
	decodeResponse(t, response, &report)
	if !report.AzureReadOnlyEnabled || report.ExternalExecutionEnabled {
		t.Fatalf("preflight report = %#v", report)
	}
	response = postJSON(t, ui, "/api/go-images/smoke/start", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("removed queue endpoint status = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
	}
	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled unofficial demo status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestFindAndImportExistingRun(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := PipelineRunCandidate{
		BuildID:       777,
		DefinitionID:  goImagesPipelineID,
		Status:        "inProgress",
		State:         "running",
		URL:           "https://example/build/777",
		QueueTime:     time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		SourceBranch:  "refs/heads/microsoft/main",
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
		VersionSet:    `["1.25.6-1","1.26.1-1"]`,
		Parameters: map[string]string{
			"sourceBuildPipelineRunId": "$(Build.BuildId)",
			"publishRepoPrefix":        "public/",
		},
	}
	validated := false
	monitored := false
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight: func(context.Context) (string, error) {
				return "fake preflight passed", nil
			},
			FindRuns: func(_ context.Context, versions []string) ([]PipelineRunCandidate, error) {
				if strings.Join(versions, ",") != "1.25.6-1,1.26.1-1" {
					t.Fatalf("versions = %#v", versions)
				}
				return []PipelineRunCandidate{candidate}, nil
			},
			ValidateRun: func(_ context.Context, buildID int, versions []string) (PipelineRunCandidate, error) {
				validated = true
				if buildID != 777 || strings.Join(versions, ",") != "1.25.6-1,1.26.1-1" {
					t.Fatalf("build ID = %d, versions = %#v", buildID, versions)
				}
				return candidate, nil
			},
			MonitorRun: func(_ context.Context, buildID int, versions []string) error {
				monitored = true
				if buildID != 777 || strings.Join(versions, ",") != "1.25.6-1,1.26.1-1" {
					t.Fatalf("monitor build ID = %d, versions = %#v", buildID, versions)
				}
				return nil
			},
		}),
	)

	response := postJSON(t, ui, "/api/go-images/runs/search", `{"versions":["1.26.1-1","1.25.6-1"]}`)
	var search struct {
		Versions   []string               `json:"versions"`
		Candidates []PipelineRunCandidate `json:"candidates"`
	}
	decodeResponse(t, response, &search)
	if response.StatusCode != http.StatusOK || len(search.Candidates) != 1 || search.Candidates[0].BuildID != 777 {
		t.Fatalf("search status = %d, response = %#v", response.StatusCode, search)
	}

	response = postJSON(t, ui, "/api/go-images/runs/import", `{
		"buildId":777,
		"versions":["1.26.1-1","1.25.6-1"]
	}`)
	var imported planResponse
	decodeResponse(t, response, &imported)
	if response.StatusCode != http.StatusOK || !validated {
		t.Fatalf("import status = %d, validated = %v", response.StatusCode, validated)
	}
	if imported.Run.BuildID != "777" || imported.Run.Complete || !imported.Run.Imported {
		t.Fatalf("imported run = %#v", imported.Run)
	}
	if imported.Pipeline.Parameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" ||
		imported.Pipeline.Parameters["publishRepoPrefix"] != "public/" {

		t.Fatalf("imported parameters = %#v", imported.Pipeline.Parameters)
	}
	response = postJSON(t, ui, "/api/go-images/runs/monitor", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("monitor status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		complete := ui.server.document.State.Day.GoImagesReleaseComplete
		active := ui.server.monitorRunning
		ui.server.mu.Unlock()
		if complete && !active {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for imported-run monitor")
		case <-time.After(time.Millisecond):
		}
	}
	if !monitored {
		t.Fatal("imported run was not monitored")
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State.Day.GoImagesReleaseBuildID != "777" ||
		persisted.State.Day.GoImagesCommit != candidate.SourceVersion ||
		persisted.State.Day.GoImagesSourceBranch != candidate.SourceBranch {

		t.Fatalf("persisted go-images state = %#v", persisted.State.Day)
	}
	restoredUI := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			FindRuns:     func(context.Context, []string) ([]PipelineRunCandidate, error) { return nil, nil },
			ValidateRun: func(context.Context, int, []string) (PipelineRunCandidate, error) {
				return candidate, nil
			},
			MonitorRun: func(context.Context, int, []string) error { return nil },
		}),
	)
	response, err = restoredUI.client.Get(restoredUI.http.URL + "/api/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored planResponse
	decodeResponse(t, response, &restored)
	if restored.Run.BuildID != "777" || !restored.Run.Imported || restored.Run.SourceVersion != candidate.SourceVersion {
		t.Fatalf("restored imported run = %#v", restored.Run)
	}
}

func TestImportRejectsCandidateFromWrongDefinition(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := PipelineRunCandidate{
		BuildID:       777,
		DefinitionID:  goImagesUnofficialDemoID,
		State:         "succeeded",
		SourceBranch:  goImagesDemoSourceBranch,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			FindRuns:     func(context.Context, []string) ([]PipelineRunCandidate, error) { return nil, nil },
			ValidateRun:  func(context.Context, int, []string) (PipelineRunCandidate, error) { return candidate, nil },
			MonitorRun:   func(context.Context, int, []string) error { return nil },
		}),
	)
	response := postJSON(t, ui, "/api/go-images/runs/import", `{
		"buildId":777,
		"versions":["1.26.5-2"]
	}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("wrong-definition import status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("wrong-definition candidate unexpectedly persisted: %v", err)
	}
}

func TestImportWithIncompatibleSourceCannotEnableDemo(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := PipelineRunCandidate{
		BuildID:       777,
		DefinitionID:  goImagesPipelineID,
		Status:        "completed",
		Result:        "succeeded",
		State:         "succeeded",
		SourceBranch:  goImagesDemoSourceBranch,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			FindRuns:     func(context.Context, []string) ([]PipelineRunCandidate, error) { return nil, nil },
			ValidateRun:  func(context.Context, int, []string) (PipelineRunCandidate, error) { return candidate, nil },
			MonitorRun:   func(context.Context, int, []string) error { return nil },
		}),
		WithGoImagesUnofficialDemoIntegration(GoImagesUnofficialDemoIntegration{
			DefinitionID: goImagesUnofficialDemoID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			ValidateSource: func(context.Context, string) error {
				return errors.New("historical parameter contract mismatch")
			},
			NewService: func(GoImagesUnofficialDemoRequest) (releasesteps.GoImagesReleaseService, error) {
				t.Fatal("incompatible source created a demo service")
				return nil, nil
			},
		}),
	)
	response := postJSON(t, ui, "/api/go-images/runs/import", `{
		"buildId":777,
		"versions":["1.26.5-2"]
	}`)
	var imported planResponse
	decodeResponse(t, response, &imported)
	if response.StatusCode != http.StatusOK || imported.UnofficialDemo.Eligible ||
		!strings.Contains(imported.UnofficialDemo.UnavailableReason, "historical parameter contract mismatch") {

		t.Fatalf("incompatible source response = %#v", imported.UnofficialDemo)
	}
}

type fakeUnofficialDemoService struct {
	queued int
	polled int
}

func (s *fakeUnofficialDemoService) TriggerBuildPipeline(
	_ context.Context,
	pipelineID int,
	parameters,
	optionalParameters map[string]string,
	_ *releasesteps.Secret,
) (string, error) {
	s.queued++
	if pipelineID != goImagesUnofficialDemoID || parameters["publishRepoPrefix"] != "dev/" ||
		parameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" || len(optionalParameters) != 0 {

		return "", errors.New("unsafe unofficial demo request")
	}
	return "888", nil
}

func (s *fakeUnofficialDemoService) PollPipelineComplete(
	_ context.Context,
	buildID string,
	_ *releasesteps.Secret,
) error {
	s.polled++
	if buildID != "888" {
		return errors.New("unexpected unofficial demo build ID")
	}
	return nil
}

func TestRealUnofficialDemoRequiresExactImportedIntent(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := PipelineRunCandidate{
		BuildID:       3019035,
		DefinitionID:  goImagesPipelineID,
		Status:        "completed",
		Result:        "succeeded",
		State:         "succeeded",
		SourceBranch:  goImagesDemoSourceBranch,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
		VersionSet:    `["1.26.5-2"]`,
		Parameters: map[string]string{
			"sourceBuildPipelineRunId": "$(Build.BuildId)",
			"publishRepoPrefix":        "public/",
		},
	}
	service := &fakeUnofficialDemoService{}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "official verified", nil },
			FindRuns: func(context.Context, []string) ([]PipelineRunCandidate, error) {
				return []PipelineRunCandidate{candidate}, nil
			},
			ValidateRun: func(context.Context, int, []string) (PipelineRunCandidate, error) { return candidate, nil },
			MonitorRun:  func(context.Context, int, []string) error { return nil },
		}),
		WithGoImagesUnofficialDemoIntegration(GoImagesUnofficialDemoIntegration{
			DefinitionID: goImagesUnofficialDemoID,
			Preflight:    func(context.Context) (string, error) { return "unofficial verified", nil },
			ValidateSource: func(_ context.Context, commit string) error {
				if commit != candidate.SourceVersion {
					t.Fatalf("validated source commit = %q", commit)
				}
				return nil
			},
			NewService: func(request GoImagesUnofficialDemoRequest) (releasesteps.GoImagesReleaseService, error) {
				if request.SourceBuildID != "3019035" || request.SourceVersion != candidate.SourceVersion ||
					strings.Join(request.Versions, ",") != "1.26.5-2" || len(request.ExecutionDigest) != 64 {

					t.Fatalf("unofficial demo request = %#v", request)
				}
				return service, nil
			},
		}),
	)

	response, err := ui.client.Get(ui.http.URL + "/api/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var report PreflightReport
	decodeResponse(t, response, &report)
	if !report.ExternalExecutionEnabled || !report.UnofficialDemoEnabled {
		t.Fatalf("preflight = %#v", report)
	}

	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 0 {
		t.Fatalf("pre-import status = %d, queued = %d", response.StatusCode, service.queued)
	}

	response = postJSON(t, ui, "/api/go-images/runs/import", `{
		"buildId":3019035,
		"versions":["1.26.5-2"]
	}`)
	var imported planResponse
	decodeResponse(t, response, &imported)
	if response.StatusCode != http.StatusOK || !imported.UnofficialDemo.Eligible ||
		len(imported.UnofficialDemo.PlanDigest) != 64 ||
		imported.UnofficialDemo.Parameters["publishRepoPrefix"] != "dev/" ||
		!strings.Contains(imported.UnofficialDemo.Confirmation, "3019035") {

		t.Fatalf("imported demo intent = %#v", imported.UnofficialDemo)
	}

	wrongDigest, err := json.Marshal(unofficialDemoStartRequest{
		PlanDigest:   strings.Repeat("0", 64),
		Confirmation: imported.UnofficialDemo.Confirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", string(wrongDigest))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 0 {
		t.Fatalf("wrong-digest status = %d, queued = %d", response.StatusCode, service.queued)
	}
	wrongPhrase, err := json.Marshal(unofficialDemoStartRequest{
		PlanDigest:   imported.UnofficialDemo.PlanDigest,
		Confirmation: "QUEUE SOMETHING ELSE",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", string(wrongPhrase))
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || service.queued != 0 {
		t.Fatalf("wrong-phrase status = %d, queued = %d", response.StatusCode, service.queued)
	}

	startBody, err := json.Marshal(unofficialDemoStartRequest{
		PlanDigest:   imported.UnofficialDemo.PlanDigest,
		Confirmation: imported.UnofficialDemo.Confirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", string(startBody))
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		complete := ui.server.document.State.Day.GoImagesDemoComplete
		active := ui.server.unofficialDemoRunning
		ui.server.mu.Unlock()
		if complete && !active {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for fake unofficial demo")
		case <-time.After(time.Millisecond):
		}
	}
	if service.queued != 1 || service.polled != 1 {
		t.Fatalf("queued = %d, polled = %d", service.queued, service.polled)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State.Day.GoImagesDemoBuildID != "888" || !persisted.State.Day.GoImagesDemoComplete ||
		persisted.State.Day.GoImagesDemoParameters["publishRepoPrefix"] != "dev/" {

		t.Fatalf("persisted demo state = %#v", persisted.State.Day)
	}
	restoredUI := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(GoImagesReadOnlyIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "official verified", nil },
			FindRuns:     func(context.Context, []string) ([]PipelineRunCandidate, error) { return nil, nil },
			ValidateRun:  func(context.Context, int, []string) (PipelineRunCandidate, error) { return candidate, nil },
			MonitorRun:   func(context.Context, int, []string) error { return nil },
		}),
		WithGoImagesUnofficialDemoIntegration(GoImagesUnofficialDemoIntegration{
			DefinitionID:   goImagesUnofficialDemoID,
			Preflight:      func(context.Context) (string, error) { return "unofficial verified", nil },
			ValidateSource: func(context.Context, string) error { return nil },
			NewService: func(GoImagesUnofficialDemoRequest) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	response, err = restoredUI.client.Get(restoredUI.http.URL + "/api/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored planResponse
	decodeResponse(t, response, &restored)
	if !restored.Restored || restored.UnofficialDemo.Run.BuildID != "888" ||
		!restored.UnofficialDemo.Run.Complete || !restored.UnofficialDemo.Eligible {

		t.Fatalf("restored unofficial demo = %#v", restored.UnofficialDemo)
	}
	response = postJSON(t, ui, "/api/go-images/unofficial-demo/start", string(startBody))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 1 {
		t.Fatalf("repeat status = %d, queued = %d", response.StatusCode, service.queued)
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

func TestImportedRunMonitorCannotQueue(t *testing.T) {
	monitor := importedRunMonitor{
		buildID: 777,
		monitor: func(context.Context, int) error { return nil },
	}
	if _, err := monitor.TriggerBuildPipeline(context.Background(), goImagesPipelineID, nil, nil, nil); err == nil {
		t.Fatal("imported-run monitor accepted a queue request")
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
	if plan.Pipeline.DefinitionID != goImagesPipelineID {
		t.Fatalf("pipeline ID = %d, want %d", plan.Pipeline.DefinitionID, goImagesPipelineID)
	}
	if plan.Pipeline.Parameters["sourceBuildPipelineRunId"] != "$(Build.BuildId)" ||
		plan.Pipeline.Parameters["publishRepoPrefix"] != "public/" {

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

	response = postJSON(t, ui, "/api/plan", `{"versions":["not-a-version"]}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid input status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func createTestPlan(t *testing.T, ui *testUI) planResponse {
	t.Helper()
	response := postJSON(t, ui, "/api/plan", `{"versions":["1.26.1-1","1.27rc1-1"]}`)
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
