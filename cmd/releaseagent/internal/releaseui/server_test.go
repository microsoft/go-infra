// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
)

const (
	testSourceCommit = "81ce9afc2b75ec4e153dd15fc3c7539b12024945"
	testGoImagesAPI  = "/api/processes/go-images"
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
	httpServer := httptest.NewTLSServer(server.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := httpServer.Client()
	client.Jar = jar
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
		t.Fatalf("launch status = %d", response.StatusCode)
	}
	if response.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("launch response has no Content-Security-Policy")
	}
	t.Cleanup(func() {
		cancel()
		httpServer.Close()
	})
	return &testUI{server: server, http: httpServer, client: client}
}

func testReadOnly(source *GoImagesSource, rollbackCalls *int) GoImagesReadOnlyIntegration {
	return GoImagesReadOnlyIntegration{
		DefinitionID: goImagesPipelineID,
		Preflight:    func(context.Context) (string, error) { return "verified", nil },
		ResolveCurrentSource: func(context.Context) (GoImagesSource, error) {
			return *source, nil
		},
		ValidateRollback: func(_ context.Context, buildID int) (GoImagesRollbackSource, error) {
			if rollbackCalls != nil {
				*rollbackCalls++
			}
			if buildID != 3019035 {
				return GoImagesRollbackSource{}, errors.New("unexpected build")
			}
			return GoImagesRollbackSource{
				BuildID: buildID, URL: "https://example/build/3019035", SourceBranch: goImagesSourceBranch,
				SourceVersion: testSourceCommit, Versions: []string{"1.25.12-1", "1.26.5-2"},
			}, nil
		},
		ListOngoing: func(context.Context) ([]GoImagesOngoingRun, error) { return nil, nil },
	}
}

func TestDashboardShowsProcessCatalog(t *testing.T) {
	ui := newTestUI(t)
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || len(dashboard.Processes) != 3 {
		t.Fatalf("dashboard = %#v", dashboard)
	}
	if !dashboard.Processes[0].Available || dashboard.Processes[0].ID != goImagesProcessID ||
		!dashboard.Processes[1].Available || dashboard.Processes[1].ID != "go-infra" {
		t.Fatalf("processes = %#v", dashboard.Processes)
	}
	if len(dashboard.Ongoing) != 0 {
		t.Fatalf("ongoing = %#v", dashboard.Ongoing)
	}
	response, err = ui.client.Get(ui.http.URL + "/go-images")
	if err != nil {
		t.Fatal(err)
	}
	goImagesPage, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("go-images page status = %d", response.StatusCode)
	}
	response, err = ui.client.Get(ui.http.URL + "/go-infra")
	if err != nil {
		t.Fatal(err)
	}
	goInfraPage, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("go-infra page status = %d", response.StatusCode)
	}
	if string(goImagesPage) != string(goInfraPage) || !strings.Contains(string(goImagesPage), "/assets/workflow.js") {
		t.Fatal("release processes do not use the same workflow-aware template")
	}
	response, err = ui.client.Get(ui.http.URL + testGoImagesAPI)
	if err != nil {
		t.Fatal(err)
	}
	var goImages processDetail
	decodeResponse(t, response, &goImages)
	if response.StatusCode != http.StatusOK || goImages.Workflow == nil || !goImages.Workflow.CanPrepare ||
		!goImages.Workflow.CanStart || len(goImages.Workflow.Inputs) != 2 || len(goImages.Workflow.Steps) != 4 {

		t.Fatalf("go-images process = %#v", goImages)
	}
	response, err = ui.client.Get(ui.http.URL + "/api/processes/go-infra")
	if err != nil {
		t.Fatal(err)
	}
	var process processDetail
	decodeResponse(t, response, &process)
	if response.StatusCode != http.StatusOK || process.ID != "go-infra" || len(process.Methods) != 0 ||
		process.Workflow == nil || !process.Workflow.HasPreflight || !process.Workflow.CanPrepare ||
		!process.Workflow.CanStart || len(process.Workflow.Inputs) != 3 || len(process.Workflow.Steps) != 1 {

		t.Fatalf("process = %#v", process)
	}
}

func TestProcessRoutesIsolatePreparedPlans(t *testing.T) {
	workflow := func(processID string) *ProcessWorkflow {
		return &ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Prepare",
			GetPlan: func(_ *Server, response http.ResponseWriter, _ *http.Request) {
				writeJSON(response, http.StatusOK, map[string]string{"process": processID})
			},
			Prepare: func(_ *Server, response http.ResponseWriter, _ *http.Request) {
				writeJSON(response, http.StatusOK, map[string]string{"prepared": processID})
			},
		}
	}
	registry, err := newProcessRegistry(
		ProcessDefinition{
			ID: "one", Name: "One", Mark: "O", Description: "First process", Status: "Available",
			Available: true, Workflow: workflow("one"),
		},
		ProcessDefinition{
			ID: "two", Name: "Two", Mark: "T", Description: "Second process", Status: "Available",
			Available: true, Workflow: workflow("two"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ui := newTestUI(t, Option(func(server *Server) { server.processes = registry }))
	assertPreparedProcess(t, ui, "one", http.StatusOK)
	assertPreparedProcess(t, ui, "two", http.StatusOK)

	response := postJSON(t, ui, "/api/processes/one/plan", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("prepare one status = %d", response.StatusCode)
	}
	assertPreparedProcess(t, ui, "one", http.StatusOK)
	assertPreparedProcess(t, ui, "two", http.StatusConflict)

	response = postJSON(t, ui, "/api/processes/two/plan", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("prepare two status = %d", response.StatusCode)
	}
	assertPreparedProcess(t, ui, "one", http.StatusConflict)
	assertPreparedProcess(t, ui, "two", http.StatusOK)
}

func assertPreparedProcess(t *testing.T, ui *testUI, processID string, wantStatus int) {
	t.Helper()
	response, err := ui.client.Get(ui.http.URL + "/api/processes/" + processID + "/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("process %s status = %d, want %d", processID, response.StatusCode, wantStatus)
	}
	if wantStatus != http.StatusOK {
		return
	}
	var plan map[string]string
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan["process"] != processID {
		t.Fatalf("process %s plan = %#v", processID, plan)
	}
}

func TestTrackOngoingReleases(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	readOnly := testReadOnly(&source, nil)
	queued := time.Date(2026, 8, 6, 12, 30, 0, 0, time.UTC)
	readOnly.ListOngoing = func(context.Context) ([]GoImagesOngoingRun, error) {
		return []GoImagesOngoingRun{{
			BuildID: 3035000, Mode: releasesteps.GoImagesReleaseModeTest,
			Status: "running", URL: "https://example/build/3035000", Queued: queued,
		}}, nil
	}
	ui := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(readOnly))
	response, err := ui.client.Get(ui.http.URL + "/api/releases/ongoing")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Releases []releaseSummary `json:"releases"`
	}
	decodeResponse(t, response, &result)
	if response.StatusCode != http.StatusOK || len(result.Releases) != 1 ||
		result.Releases[0].BuildID != "3035000" || result.Releases[0].Mode != releasesteps.GoImagesReleaseModeTest ||
		result.Releases[0].Href != "https://example/build/3035000" {

		t.Fatalf("ongoing releases = %#v", result.Releases)
	}
}

func TestPreflightIsLocalAndExecutionDisabled(t *testing.T) {
	var lookedUp []string
	ui := newTestUI(t, WithExecutableLookup(func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		if name == "az" {
			return "/test/bin/az", nil
		}
		return "", errors.New("not found")
	}))
	response, err := ui.client.Get(ui.http.URL + testGoImagesAPI + "/preflight")
	if err != nil {
		t.Fatal(err)
	}
	var report PreflightReport
	decodeResponse(t, response, &report)
	if report.ExternalExecutionEnabled || report.GoImagesExecutionEnabled || strings.Join(lookedUp, ",") != "gh,az" {
		t.Fatalf("report = %#v, looked up = %v", report, lookedUp)
	}
}

func TestExecutionOptionRequiresReadOnlyValidation(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	execution := GoImagesExecutionIntegration{
		DefinitionID: goImagesPipelineID,
		Preflight:    func(context.Context) (string, error) { return "ok", nil },
		NewService: func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
			return &fakeExecutionService{}, nil
		},
	}
	if _, err := New(context.Background(), WithSessionStore(store), WithGoImagesExecutionIntegration(execution)); err == nil {
		t.Fatal("execution was enabled without read-only validation")
	}
}

func TestPrepareReleaseModes(t *testing.T) {
	for _, test := range []struct {
		name         string
		body         string
		wantMode     releasesteps.GoImagesReleaseMode
		wantSource   string
		wantPrefix   string
		wantRollback bool
	}{
		{name: "normal", body: `{"mode":"normal"}`, wantMode: releasesteps.GoImagesReleaseModeNormal, wantSource: "$(Build.BuildId)", wantPrefix: "public/"},
		{name: "rollback", body: `{"mode":"rollback","sourceBuildId":"3019035"}`, wantMode: releasesteps.GoImagesReleaseModeRollback, wantSource: "3019035", wantPrefix: "public/", wantRollback: true},
		{name: "test", body: `{"mode":"test"}`, wantMode: releasesteps.GoImagesReleaseModeTest, wantSource: "$(Build.BuildId)", wantPrefix: "dev/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
			if err != nil {
				t.Fatal(err)
			}
			source := GoImagesSource{
				Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2", "1.25.12-1"},
			}
			rollbackCalls := 0
			ui := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, &rollbackCalls)))
			response := postJSON(t, ui, testGoImagesAPI+"/plan", test.body)
			var plan planResponse
			decodeResponse(t, response, &plan)
			if response.StatusCode != http.StatusOK || plan.Input.Mode != test.wantMode || len(plan.Steps) != 4 {
				t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
			}
			if !plan.Pipeline.Locked || plan.Pipeline.Parameters["sourceBuildPipelineRunId"] != test.wantSource ||
				plan.Pipeline.Parameters["publishRepoPrefix"] != test.wantPrefix {

				t.Fatalf("pipeline = %#v", plan.Pipeline)
			}
			if (plan.RollbackSource != nil) != test.wantRollback || rollbackCalls != boolInt(test.wantRollback) {
				t.Fatalf("rollback = %#v, calls = %d", plan.RollbackSource, rollbackCalls)
			}
			persisted, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Input.GoImagesReleaseMode != test.wantMode || persisted.Input.GoImagesSourceVersion != testSourceCommit {
				t.Fatalf("persisted input = %#v", persisted.Input)
			}
		})
	}
}

func TestPlanRejectsInputsOutsideSelectedMode(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	ui := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	for _, body := range []string{
		`{"mode":"normal","sourceBuildId":"123"}`,
		`{"mode":"rollback","sourceBuildId":"not-a-build"}`,
		`{"mode":"test","publishRepoPrefix":"public/"}`,
		`{"mode":"unknown"}`,
	} {
		response := postJSON(t, ui, testGoImagesAPI+"/plan", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, response.StatusCode)
		}
	}
}

func TestPersistAndRestoreModePlan(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	created := createTestPlan(t, first, `{"mode":"test"}`)
	if created.SessionID == "" || created.Restored {
		t.Fatalf("created = %#v", created)
	}
	second := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	response, err := second.client.Get(second.http.URL + testGoImagesAPI + "/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored planResponse
	decodeResponse(t, response, &restored)
	if !restored.Restored || restored.SessionID != created.SessionID || restored.Input.Mode != releasesteps.GoImagesReleaseModeTest {
		t.Fatalf("restored = %#v", restored)
	}
	response, err = second.client.Get(second.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardResponse
	decodeResponse(t, response, &dashboard)
	if len(dashboard.Ongoing) != 1 || dashboard.Ongoing[0].Status != "ready" {
		t.Fatalf("dashboard = %#v", dashboard)
	}
}

func TestRestoredQueuedReleaseAutomaticallyResumesMonitoring(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, first, `{"mode":"test"}`)
	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := document.State
	state.Day.GoImagesReleaseQueueAttempted = true
	state.Day.GoImagesReleaseBuildID = "888"
	document, err = document.WithState(&state, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), document); err != nil {
		t.Fatal(err)
	}

	service := &fakeExecutionService{mode: releasesteps.GoImagesReleaseModeTest}
	second := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "execution verified", nil },
			NewService: func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	waitForRelease(t, second)
	if service.mirrors != 0 || service.queued != 0 || service.polled != 1 {
		t.Fatalf("mirrors = %d, queued = %d, polled = %d", service.mirrors, service.queued, service.polled)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.State.Day.GoImagesReleaseComplete || persisted.State.Day.GoImagesReleaseResult != "succeeded" {
		t.Fatalf("persisted state = %#v", persisted.State.Day)
	}
}

func TestRevision5QueuedReleaseMigratesAndResumesMonitoring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-session.json")
	store, err := session.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, first, `{"mode":"test"}`)
	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	originalID := document.ID
	writeRevision5Session(t, path, document, "888")

	service := &fakeExecutionService{mode: releasesteps.GoImagesReleaseModeTest}
	second := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "execution verified", nil },
			NewService: func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	waitForRelease(t, second)
	if service.mirrors != 0 || service.queued != 0 || service.polled != 1 {
		t.Fatalf("mirrors = %d, queued = %d, polled = %d", service.mirrors, service.queued, service.polled)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ID != originalID || persisted.Plan.WorkflowRevision != session.CurrentWorkflowRevision ||
		persisted.Input.TargetAzDOGoImagesRepo != releasesteps.GoImagesInternalMirrorTarget ||
		!persisted.State.Day.GoImagesReleaseComplete || persisted.State.Day.GoImagesReleaseResult != "succeeded" {

		t.Fatalf("persisted migrated session = %#v", persisted)
	}
}

func TestRevision5ReleaseWithoutBuildIDFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release-session.json")
	store, err := session.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, first, `{"mode":"test"}`)
	document, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeRevision5Session(t, path, document, "")

	service := &fakeExecutionService{mode: releasesteps.GoImagesReleaseModeTest}
	_, err = New(
		context.Background(),
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "execution verified", nil },
			NewService: func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "queue status is uncertain") {
		t.Fatalf("error = %v", err)
	}
	if service.mirrors != 0 || service.queued != 0 || service.polled != 0 {
		t.Fatalf("mirrors = %d, queued = %d, polled = %d", service.mirrors, service.queued, service.polled)
	}
}

func writeRevision5Session(t *testing.T, path string, document *session.Document, buildID string) {
	t.Helper()
	document.Input.TargetAzDOGoImagesRepo = ""
	inputData, err := json.Marshal(document.Input)
	if err != nil {
		t.Fatal(err)
	}
	document.State.InputChecksum = crc32.ChecksumIEEE(inputData)
	document.State.Day.GoImagesReleaseQueueAttempted = true
	document.State.Day.GoImagesReleaseBuildID = buildID
	document.Plan = session.Plan{
		WorkflowRevision: session.MigratableWorkflowRevision,
		Digest:           legacyGoImagesWorkflowRevision5Digest,
		Steps: []session.PlanStep{
			{Name: "go-images.release.queue", TimeoutNanos: int64(10 * time.Minute)},
			{Name: "go-images.release.wait", DependsOn: []string{"go-images.release.queue"}, TimeoutNanos: int64(2 * time.Hour)},
			{Name: "go-images.release.complete", DependsOn: []string{"go-images.release.wait"}},
		},
	}
	executionData, err := json.Marshal(struct {
		Input            releasesteps.Input `json:"input"`
		PlanDigest       string             `json:"planDigest"`
		WorkflowRevision int                `json:"workflowRevision"`
	}{
		Input: document.Input, PlanDigest: document.Plan.Digest,
		WorkflowRevision: document.Plan.WorkflowRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	executionDigest := sha256.Sum256(executionData)
	document.ExecutionDigest = hex.EncodeToString(executionDigest[:])
	legacyData, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}
}

type fakeExecutionService struct {
	mirrors     int
	queued      int
	polled      int
	mode        releasesteps.GoImagesReleaseMode
	sourceBuild string
	mirrorErr   error
}

func (s *fakeExecutionService) PollAzDOMirror(
	_ context.Context,
	target,
	commit string,
	_ *releasesteps.Secret,
) error {
	s.mirrors++
	if target != releasesteps.GoImagesInternalMirrorTarget || commit != testSourceCommit {
		return errors.New("unexpected mirror source")
	}
	return s.mirrorErr
}

func (s *fakeExecutionService) TriggerBuildPipeline(
	_ context.Context,
	pipelineID int,
	parameters,
	optionalParameters map[string]string,
	_ *releasesteps.Secret,
) (string, error) {
	s.queued++
	if pipelineID != goImagesPipelineID || len(optionalParameters) != 0 {
		return "", errors.New("unsafe execution request")
	}
	want, err := releasesteps.GoImagesPipelineParametersForMode(s.mode, s.sourceBuild)
	if err != nil {
		return "", err
	}
	if !maps.Equal(parameters, want) {
		return "", errors.New("parameters do not match selected mode")
	}
	return "888", nil
}

func (s *fakeExecutionService) PollPipelineComplete(_ context.Context, buildID string, _ *releasesteps.Secret) error {
	s.polled++
	if buildID != "888" {
		return errors.New("unexpected build ID")
	}
	return nil
}

func TestRealReleaseRequiresExactIntent(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	service := &fakeExecutionService{mode: releasesteps.GoImagesReleaseModeNormal}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "execution verified", nil },
			NewService: func(request GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				if request.Mode != releasesteps.GoImagesReleaseModeNormal || request.SourceVersion != testSourceCommit ||
					len(request.ExecutionDigest) != 64 || request.SourceBuildID != "" {

					t.Fatalf("request = %#v", request)
				}
				return service, nil
			},
		}),
	)
	plan := createTestPlan(t, ui, `{"mode":"normal"}`)
	if !plan.Execution.Eligible || len(plan.Execution.PlanDigest) != 64 {
		t.Fatalf("execution = %#v", plan.Execution)
	}

	response := postJSON(t, ui, testGoImagesAPI+"/start", `{"planDigest":"wrong","confirmed":true}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 0 {
		t.Fatalf("wrong intent status = %d, queued = %d", response.StatusCode, service.queued)
	}
	body, err := json.Marshal(releaseStartRequest{PlanDigest: plan.Execution.PlanDigest})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, testGoImagesAPI+"/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || service.queued != 0 {
		t.Fatalf("unconfirmed status = %d, queued = %d", response.StatusCode, service.queued)
	}
	body, err = json.Marshal(releaseStartRequest{PlanDigest: plan.Execution.PlanDigest, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, ui, testGoImagesAPI+"/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForRelease(t, ui)
	if service.mirrors != 1 || service.queued != 1 || service.polled != 1 {
		t.Fatalf("mirrors = %d, queued = %d, polled = %d", service.mirrors, service.queued, service.polled)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.State.Day.GoImagesReleaseQueueAttempted || persisted.State.Day.GoImagesReleaseBuildID != "888" ||
		!persisted.State.Day.GoImagesReleaseComplete || persisted.State.Day.GoImagesReleaseResult != "succeeded" {

		t.Fatalf("persisted state = %#v", persisted.State.Day)
	}
	response, err = ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardResponse
	decodeResponse(t, response, &dashboard)
	if len(dashboard.Recent) != 1 || len(dashboard.Ongoing) != 0 {
		t.Fatalf("dashboard = %#v", dashboard)
	}
}

func TestReleaseDoesNotQueueWhenMirrorVerificationFails(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	service := &fakeExecutionService{
		mode:      releasesteps.GoImagesReleaseModeNormal,
		mirrorErr: errors.New("commit is not available in the internal mirror"),
	}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			NewService: func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) {
				return service, nil
			},
		}),
	)
	plan := createTestPlan(t, ui, `{"mode":"normal"}`)
	body, _ := json.Marshal(releaseStartRequest{PlanDigest: plan.Execution.PlanDigest, Confirmed: true})
	response := postJSON(t, ui, testGoImagesAPI+"/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	waitForReleaseStopped(t, ui)
	if service.mirrors != 1 || service.queued != 0 || service.polled != 0 {
		t.Fatalf("mirrors = %d, queued = %d, polled = %d", service.mirrors, service.queued, service.polled)
	}
}

func TestReleaseRejectsWhenMainAdvances(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	service := &fakeExecutionService{mode: releasesteps.GoImagesReleaseModeTest}
	ui := newTestUI(t,
		WithSessionStore(store),
		WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)),
		WithGoImagesExecutionIntegration(GoImagesExecutionIntegration{
			DefinitionID: goImagesPipelineID,
			Preflight:    func(context.Context) (string, error) { return "ok", nil },
			NewService:   func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error) { return service, nil },
		}),
	)
	plan := createTestPlan(t, ui, `{"mode":"test"}`)
	source.Commit = "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"
	body, _ := json.Marshal(releaseStartRequest{PlanDigest: plan.Execution.PlanDigest, Confirmed: true})
	response := postJSON(t, ui, testGoImagesAPI+"/start", string(body))
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || service.queued != 0 {
		t.Fatalf("status = %d, queued = %d", response.StatusCode, service.queued)
	}
}

func TestCreatePlanAndRunSimulation(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	ui := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	plan := createTestPlan(t, ui, `{"mode":"normal"}`)
	if plan.Execution.Enabled || len(plan.Steps) != 4 {
		t.Fatalf("plan = %#v", plan)
	}
	response := postJSON(t, ui, testGoImagesAPI+"/simulate", `{}`)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("simulation status = %d", response.StatusCode)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		response, err := ui.client.Get(ui.http.URL + testGoImagesAPI + "/state")
		if err != nil {
			t.Fatal(err)
		}
		var snapshot coordinator.Snapshot
		decodeResponse(t, response, &snapshot)
		if !snapshot.Active && len(snapshot.Steps) != 0 {
			for _, step := range snapshot.Steps {
				if step.Status != coordinator.StepStatusSucceeded {
					t.Fatalf("step = %#v", step)
				}
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for simulation")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestEventsSendInitialSnapshot(t *testing.T) {
	store, err := session.NewFileStore(filepath.Join(t.TempDir(), "release-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	ui := newTestUI(t, WithSessionStore(store), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, ui, `{"mode":"normal"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ui.http.URL+testGoImagesAPI+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := ui.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	var sawEvent, sawData bool
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: state" {
			sawEvent = true
		}
		if strings.HasPrefix(line, "data: ") {
			sawData = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawEvent || !sawData {
		t.Fatalf("event = %v, data = %v", sawEvent, sawData)
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
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
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
		t.Fatalf("non-loopback status = %d", response.StatusCode)
	}
}

func TestLaunchCookieSecurity(t *testing.T) {
	server, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	client := httpServer.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	launchURL, err := server.LaunchURL(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" {
		t.Fatalf("launch response = %d, location = %q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("launch cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" ||
		cookie.SameSite != http.SameSiteStrictMode {

		t.Fatalf("launch cookie = %#v", cookie)
	}
}

func waitForRelease(t *testing.T, ui *testUI) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		complete := ui.server.document.State.Day.GoImagesReleaseComplete
		active := ui.server.releaseRunning
		ui.server.mu.Unlock()
		if complete && !active {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for release")
		case <-time.After(time.Millisecond):
		}
	}
}

func waitForReleaseStopped(t *testing.T, ui *testUI) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		ui.server.mu.Lock()
		active := ui.server.releaseRunning
		ui.server.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for release to stop")
		case <-time.After(time.Millisecond):
		}
	}
}

func createTestPlan(t *testing.T, ui *testUI, body string) planResponse {
	t.Helper()
	response := postJSON(t, ui, testGoImagesAPI+"/plan", body)
	var plan planResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d", response.StatusCode)
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
		t.Fatalf("decode response with status %d: %v", response.StatusCode, err)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
