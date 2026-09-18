// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	releaseui "github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const testGoInfraHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGoInfraGitHub struct {
	mu sync.Mutex

	preflightCalls  int
	getPRCalls      int
	labelCalls      int
	dispatches      []bool
	pollCalls       int
	pollStarted     chan struct{}
	pollRelease     <-chan struct{}
	workflowRun     GoInfraWorkflowRun
	workflowUpdates []GoInfraWorkflowRun
	pullRequest     GoInfraPullRequest
	getPullRequest  func(context.Context, int) (GoInfraPullRequest, error)
	preflightErr    error
	labelErr        error
	dispatchErr     error
	pollErr         error
}

type fakeGoInfraGitHubStats struct {
	preflightCalls int
	getPRCalls     int
	labelCalls     int
	dispatches     []bool
	pollCalls      int
}

type goInfraTestPlanStep struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Status    string   `json:"status,omitempty"`
}

type goInfraTestPipelineRun struct {
	BuildID   string `json:"buildId,omitempty"`
	URL       string `json:"url,omitempty"`
	LinkLabel string `json:"linkLabel,omitempty"`
	Complete  bool   `json:"complete"`
}

type goInfraTestExecutionResponse struct {
	Enabled           bool                   `json:"enabled"`
	Eligible          bool                   `json:"eligible"`
	PlanDigest        string                 `json:"planDigest,omitempty"`
	UnavailableReason string                 `json:"unavailableReason,omitempty"`
	Run               goInfraTestPipelineRun `json:"run"`
}

type goInfraTestPlanResponse struct {
	Input     goInfraPlanInput             `json:"input"`
	Steps     []goInfraTestPlanStep        `json:"steps"`
	SessionID string                       `json:"sessionId"`
	Execution goInfraTestExecutionResponse `json:"execution"`
	View      contract.PlanView            `json:"view"`
}

type goInfraTestUI struct {
	http   *httptest.Server
	client *http.Client
}

type memoryProcessRunStore struct {
	mu        sync.Mutex
	nextID    int
	records   map[int]*releaseui.ReleaseRunRecord
	createErr error
	getErr    error
	updateErr error
}

func (f *fakeGoInfraGitHub) integration() GitHubService {
	return f
}

func (f *fakeGoInfraGitHub) Preflight(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return "verified fake GitHub integration", f.preflightErr
}

func (f *fakeGoInfraGitHub) GetPullRequest(ctx context.Context, number int) (GoInfraPullRequest, error) {
	f.mu.Lock()
	f.getPRCalls++
	getPullRequest := f.getPullRequest
	pullRequest := f.pullRequest
	f.mu.Unlock()
	if getPullRequest != nil {
		return getPullRequest(ctx, number)
	}
	if number != pullRequest.Number {
		return GoInfraPullRequest{}, errors.New("unexpected pull request")
	}
	return pullRequest, nil
}

func (f *fakeGoInfraGitHub) AddReleaseOnMergeLabel(_ context.Context, number int, expectedHeadSHA string) (GoInfraPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labelCalls++
	if number != f.pullRequest.Number || expectedHeadSHA != f.pullRequest.HeadSHA {
		return GoInfraPullRequest{}, errors.New("reviewed pull request changed")
	}
	return f.pullRequest, f.labelErr
}

func (f *fakeGoInfraGitHub) DispatchPatchRelease(_ context.Context, dryRun bool) (GoInfraWorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatches = append(f.dispatches, dryRun)
	if f.dispatchErr != nil {
		return GoInfraWorkflowRun{}, f.dispatchErr
	}
	if f.workflowRun.ID == 0 {
		f.workflowRun = testGoInfraWorkflowRun("queued", "")
	}
	return f.workflowRun, nil
}

func (f *fakeGoInfraGitHub) PollWorkflowRun(_ context.Context, id int64, report func(GoInfraWorkflowRun) error) (GoInfraWorkflowRun, error) {
	f.mu.Lock()
	f.pollCalls++
	workflowRun := f.workflowRun
	updates := append([]GoInfraWorkflowRun(nil), f.workflowUpdates...)
	started := f.pollStarted
	release := f.pollRelease
	pollErr := f.pollErr
	f.mu.Unlock()
	if id != workflowRun.ID {
		return GoInfraWorkflowRun{}, errors.New("unexpected workflow run")
	}
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	if len(updates) == 0 {
		updates = []GoInfraWorkflowRun{testGoInfraWorkflowRun("completed", "success")}
	}
	for _, update := range updates {
		if err := report(update); err != nil {
			return GoInfraWorkflowRun{}, err
		}
	}
	return updates[len(updates)-1], pollErr
}

func (f *fakeGoInfraGitHub) stats() fakeGoInfraGitHubStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeGoInfraGitHubStats{
		preflightCalls: f.preflightCalls,
		getPRCalls:     f.getPRCalls,
		labelCalls:     f.labelCalls,
		dispatches:     append([]bool(nil), f.dispatches...),
		pollCalls:      f.pollCalls,
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

func newGoInfraTestUI(t *testing.T, integration GitHubService, options ...releaseui.Option) *goInfraTestUI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverOptions := []releaseui.Option{
		releaseui.WithDemoDelay(0),
		releaseui.WithProcesses(NewProcess(integration)),
	}
	serverOptions = append(serverOptions, options...)
	server, err := releaseui.New(ctx, serverOptions...)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		cancel()
		httpServer.Close()
		t.Fatal(err)
	}
	client := httpServer.Client()
	client.Jar = jar
	launchURL, err := server.LaunchURL(httpServer.URL)
	if err != nil {
		cancel()
		httpServer.Close()
		t.Fatal(err)
	}
	response, err := client.Get(launchURL)
	if err != nil {
		cancel()
		httpServer.Close()
		t.Fatal(err)
	}
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusOK {
		cancel()
		httpServer.Close()
		t.Fatalf("launch status = %d", status)
	}
	t.Cleanup(func() {
		cancel()
		httpServer.Close()
	})
	return &goInfraTestUI{http: httpServer, client: client}
}

func postJSON(t *testing.T, ui *goInfraTestUI, path, body string) *http.Response {
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

func getResponse(t *testing.T, ui *goInfraTestUI, path string) *http.Response {
	t.Helper()
	response, err := ui.client.Get(ui.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response with status %d: %v", response.StatusCode, err)
	}
}

func closeResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Errorf("discard response body: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*releaseui.ReleaseRunRecord)}
}

func (s *memoryProcessRunStore) Create(_ context.Context, run *contract.State) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &releaseui.ReleaseRunRecord{
		WorkItemID: s.nextID,
		Revision:   1,
		URL:        fmt.Sprintf("https://example.invalid/workitems/%d", s.nextID),
		Run:        cloneProcessRun(run),
	}
	s.records[record.WorkItemID] = cloneProcessRunRecord(record)
	s.nextID++
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("process run record not found")
	}
	return cloneProcessRunRecord(record), nil
}

func (s *memoryProcessRunStore) Update(
	_ context.Context,
	current *releaseui.ReleaseRunRecord,
	run *contract.State,
) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if current == nil {
		return nil, errors.New("current process run record is nil")
	}
	record, ok := s.records[current.WorkItemID]
	if !ok || record.Revision != current.Revision {
		return nil, errors.New("process run revision conflict")
	}
	updated := &releaseui.ReleaseRunRecord{
		WorkItemID: record.WorkItemID,
		Revision:   record.Revision + 1,
		URL:        record.URL,
		Run:        cloneProcessRun(run),
	}
	s.records[updated.WorkItemID] = cloneProcessRunRecord(updated)
	return cloneProcessRunRecord(updated), nil
}

func (s *memoryProcessRunStore) seed(t *testing.T, run *contract.State) int {
	t.Helper()
	record, err := s.Create(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	return record.WorkItemID
}

func (s *memoryProcessRunStore) latest(t *testing.T) *contract.State {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(s.records))
	}
	for _, record := range s.records {
		return cloneProcessRun(record.Run)
	}
	panic("unreachable")
}

func (s *memoryProcessRunStore) current() (*contract.State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		return nil, false
	}
	for _, record := range s.records {
		return cloneProcessRun(record.Run), true
	}
	return nil, false
}

func (s *memoryProcessRunStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func cloneProcessRunRecord(record *releaseui.ReleaseRunRecord) *releaseui.ReleaseRunRecord {
	if record == nil {
		return nil
	}
	cloned := *record
	cloned.Run = cloneProcessRun(record.Run)
	return &cloned
}

func cloneProcessRun(run *contract.State) *contract.State {
	if run == nil {
		return nil
	}
	cloned := *run
	cloned.Input = append(json.RawMessage(nil), run.Input...)
	cloned.Payload = append(json.RawMessage(nil), run.Payload...)
	cloned.Checkpoint = append(json.RawMessage(nil), run.Checkpoint...)
	cloned.View.Facts = append([]contract.PlanFact(nil), run.View.Facts...)
	if run.View.Request != nil {
		request := *run.View.Request
		request.Fields = append([]contract.RequestField(nil), run.View.Request.Fields...)
		cloned.View.Request = &request
	}
	if run.External != nil {
		external := *run.External
		cloned.External = &external
	}
	return &cloned
}

func testStoredGoInfraRun(t *testing.T, integration GitHubService, input goInfraPlanInput) *contract.State {
	t.Helper()
	process := NewProcess(integration)
	prepared, err := process.Prepare(context.Background(), goInfraTestSelection(t, input))
	if err != nil {
		t.Fatal(err)
	}
	run := prepared.Snapshot()
	if _, err := process.Restore(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func goInfraTestSelection(t *testing.T, input goInfraPlanInput) contract.Selection {
	t.Helper()
	variantID := input.Action
	values := map[string]string{}
	switch input.Action {
	case goInfraActionReleaseOnMerge:
		values["pullRequest"] = input.PullRequest
	case goInfraActionManualDispatch:
		variantID = input.DispatchMode
		if input.PullRequest != "" {
			values["pullRequest"] = input.PullRequest
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return contract.Selection{VariantID: variantID, Input: data}
}

func goInfraTestSelectionJSON(t *testing.T, input goInfraPlanInput) string {
	t.Helper()
	data, err := json.Marshal(goInfraTestSelection(t, input))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitForGoInfraAction(t *testing.T, store *memoryProcessRunStore) *contract.State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if run, ok := store.current(); ok && run.Complete {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for go-infra action")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGoInfraPreflightDisabled(t *testing.T) {
	ui := newGoInfraTestUI(t, nil)
	response := getResponse(t, ui, "/api/processes/go-infra/preflight")
	defer response.Body.Close()
	var report releaseui.PreflightReport
	decodeResponse(t, response, &report)
	if response.StatusCode != http.StatusOK || report.PlanningEnabled || report.ExternalExecutionEnabled {
		t.Fatalf("report = %#v", report)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusConflict {
		t.Fatalf("plan status = %d", status)
	}
}

func TestGoInfraPlanningDoesNotRequireExecutionReadiness(t *testing.T) {
	github := &fakeGoInfraGitHub{preflightErr: errors.New("workflow contract is not ready")}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := getResponse(t, ui, "/api/processes/go-infra/preflight")
	defer response.Body.Close()
	var preflight releaseui.PreflightReport
	decodeResponse(t, response, &preflight)
	if response.StatusCode != http.StatusOK || !preflight.PlanningEnabled || preflight.ExternalExecutionEnabled {
		t.Fatalf("preflight = %#v", preflight)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	stats := github.stats()
	if response.StatusCode != http.StatusOK || len(plan.Execution.PlanDigest) != 64 || stats.preflightCalls != 1 {
		t.Fatalf("status = %d, plan = %#v, preflight calls = %d", response.StatusCode, plan, stats.preflightCalls)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusPreconditionFailed {
		t.Fatalf("start status = %d", status)
	}
	stats = github.stats()
	if stats.preflightCalls != 2 || len(stats.dispatches) != 0 || store.count() != 0 {
		t.Fatalf("preflight calls = %d, dispatches = %v, work items = %d", stats.preflightCalls, stats.dispatches, store.count())
	}
}

func TestGoInfraReleaseOnMergeRequiresExactConfirmation(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := getResponse(t, ui, "/api/processes/go-infra/preflight")
	defer response.Body.Close()
	var preflight releaseui.PreflightReport
	decodeResponse(t, response, &preflight)
	if !preflight.PlanningEnabled || !preflight.ExternalExecutionEnabled {
		t.Fatalf("preflight = %#v", preflight)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionReleaseOnMerge, PullRequest: "42",
	}))
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	stats := github.stats()
	if response.StatusCode != http.StatusOK || plan.Input.PullRequest != "42" || len(plan.Execution.PlanDigest) != 64 ||
		!plan.Execution.Eligible || len(plan.Steps) != 1 || stats.getPRCalls != 1 {

		t.Fatalf("status = %d, plan = %#v, get calls = %d", response.StatusCode, plan, stats.getPRCalls)
	}

	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"wrong","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusConflict || github.stats().labelCalls != 0 {
		t.Fatalf("wrong digest status = %d, label calls = %d", status, github.stats().labelCalls)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`"}`)
	status = response.StatusCode
	closeResponse(t, response)
	if status != http.StatusBadRequest || github.stats().labelCalls != 0 {
		t.Fatalf("unconfirmed status = %d, label calls = %d", status, github.stats().labelCalls)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status = response.StatusCode
	closeResponse(t, response)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	persisted := waitForGoInfraAction(t, store)
	stats = github.stats()
	if stats.labelCalls != 1 || len(stats.dispatches) != 0 || persisted.Result != "succeeded" {
		t.Fatalf("label calls = %d, dispatches = %v, persisted = %#v", stats.labelCalls, stats.dispatches, persisted)
	}

	response = getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || plan.Execution.Run.URL != github.pullRequest.URL ||
		len(plan.Steps) != 1 || plan.Steps[0].Status != "succeeded" {

		t.Fatalf("completed plan = %#v", plan)
	}
}

func TestGoInfraReleaseOnMergeValidatesPullRequest(t *testing.T) {
	valid := testGoInfraPullRequest()
	tests := []struct {
		name        string
		pullRequest GoInfraPullRequest
	}{
		{name: "number", pullRequest: func() GoInfraPullRequest { value := valid; value.Number = 41; return value }()},
		{name: "base", pullRequest: func() GoInfraPullRequest { value := valid; value.BaseRef = "release"; return value }()},
		{name: "fork", pullRequest: func() GoInfraPullRequest { value := valid; value.Fork = true; return value }()},
		{name: "head SHA", pullRequest: func() GoInfraPullRequest { value := valid; value.HeadSHA = "not-a-sha"; return value }()},
		{name: "URL", pullRequest: func() GoInfraPullRequest {
			value := valid
			value.URL = "https://github.com/other/repo/pull/42"
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			github := &fakeGoInfraGitHub{pullRequest: valid}
			github.getPullRequest = func(context.Context, int) (GoInfraPullRequest, error) {
				return test.pullRequest, nil
			}
			process := NewProcess(github)
			if _, err := process.Prepare(context.Background(), goInfraTestSelection(t, goInfraPlanInput{
				Action: goInfraActionReleaseOnMerge, PullRequest: "42",
			})); err == nil {
				t.Fatalf("invalid pull request was accepted: %#v", test.pullRequest)
			}
		})
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
			store := newMemoryProcessRunStore()
			ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
			response := postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
				Action: goInfraActionManualDispatch, DispatchMode: test.mode,
			}))
			defer response.Body.Close()
			var plan goInfraTestPlanResponse
			decodeResponse(t, response, &plan)
			if response.StatusCode != http.StatusOK || plan.Input.DispatchMode != test.mode || len(plan.Execution.PlanDigest) != 64 {
				t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
			}
			response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
			status := response.StatusCode
			closeResponse(t, response)
			if status != http.StatusAccepted {
				t.Fatalf("start status = %d", status)
			}
			persisted := waitForGoInfraAction(t, store)
			stats := github.stats()
			if len(stats.dispatches) != 1 || stats.dispatches[0] != test.dryRun || stats.pollCalls != 1 || stats.labelCalls != 0 {
				t.Fatalf("dispatches = %v, polls = %d, label calls = %d", stats.dispatches, stats.pollCalls, stats.labelCalls)
			}
			if persisted.Result != "succeeded" || persisted.External == nil || persisted.External.ID != "123" {
				t.Fatalf("persisted = %#v", persisted)
			}
			response = getResponse(t, ui, "/api/processes/go-infra/plan")
			defer response.Body.Close()
			decodeResponse(t, response, &plan)
			if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "succeeded" ||
				plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {

				t.Fatalf("completed plan = %#v", plan)
			}
		})
	}
}

func TestGoInfraTestClassification(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	process := NewProcess(github.integration())
	for _, test := range []struct {
		name  string
		input goInfraPlanInput
		want  bool
	}{
		{name: "dry-run", input: goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun}, want: true},
		{name: "publish", input: goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish}},
		{name: "release-on-merge", input: goInfraPlanInput{Action: goInfraActionReleaseOnMerge, PullRequest: "42"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := process.Prepare(context.Background(), goInfraTestSelection(t, test.input))
			if err != nil {
				t.Fatal(err)
			}
			if got := prepared.Snapshot().Test; got != test.want {
				t.Fatalf("prepared test classification = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGoInfraStartRejectsRapidSecondConfirmation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), pollStarted: started, pollRelease: release,
	}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusAccepted {
		t.Fatalf("first start status = %d", status)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workflow polling to start")
	}
	response = getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	decodeResponse(t, response, &plan)
	if plan.Execution.Run.Complete || plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" {
		t.Fatalf("queued plan = %#v", plan)
	}
	response = getResponse(t, ui, "/api/processes/go-infra/state")
	defer response.Body.Close()
	var snapshot coordinator.Snapshot
	decodeResponse(t, response, &snapshot)
	if !snapshot.Active || len(snapshot.Steps) != 1 || snapshot.Steps[0].Status != coordinator.StepStatusRunning {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status = response.StatusCode
	closeResponse(t, response)
	if status != http.StatusConflict {
		t.Fatalf("second start status = %d", status)
	}
	close(release)
	waitForGoInfraAction(t, store)
	stats := github.stats()
	if len(stats.dispatches) != 1 || stats.pollCalls != 1 || store.count() != 1 {
		t.Fatalf("dispatches = %v, polls = %d, work items = %d", stats.dispatches, stats.pollCalls, store.count())
	}
}

func TestGoInfraMutationFailureIsTerminal(t *testing.T) {
	github := &fakeGoInfraGitHub{
		pullRequest: testGoInfraPullRequest(), dispatchErr: errors.New("fake dispatch rejected"),
	}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	persisted := waitForGoInfraAction(t, store)
	response = getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "failed" ||
		persisted.Result != "uncertain" || persisted.External != nil {

		t.Fatalf("plan = %#v, persisted = %#v", plan, persisted)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status = response.StatusCode
	closeResponse(t, response)
	stats := github.stats()
	if status != http.StatusConflict || len(stats.dispatches) != 1 {
		t.Fatalf("retry status = %d, dispatches = %v", status, stats.dispatches)
	}
}

func TestGoInfraWorkflowFailureIsTerminal(t *testing.T) {
	github := &fakeGoInfraGitHub{
		pullRequest:     testGoInfraPullRequest(),
		workflowUpdates: []GoInfraWorkflowRun{testGoInfraWorkflowRun("completed", "failure")},
		pollErr:         errors.New("workflow failed"),
	}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	defer response.Body.Close()
	var plan goInfraTestPlanResponse
	decodeResponse(t, response, &plan)
	response = postJSON(t, ui, "/api/processes/go-infra/start", `{"planDigest":"`+plan.Execution.PlanDigest+`","confirmed":true}`)
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusAccepted {
		t.Fatalf("start status = %d", status)
	}
	persisted := waitForGoInfraAction(t, store)
	response = getResponse(t, ui, "/api/processes/go-infra/plan")
	defer response.Body.Close()
	decodeResponse(t, response, &plan)
	if !plan.Execution.Run.Complete || len(plan.Steps) != 1 || plan.Steps[0].Status != "failed" ||
		plan.Execution.Run.URL != "https://github.com/microsoft/go-infra/actions/runs/123" || persisted.Result != "failed" ||
		persisted.External == nil || !persisted.External.Terminal || persisted.External.Succeeded {

		t.Fatalf("plan = %#v, persisted = %#v", plan, persisted)
	}
}

func TestGoInfraPlanRejectsUnsafeInputs(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	store := newMemoryProcessRunStore()
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	for _, body := range []string{
		`{"variantId":"release-on-merge","input":{"pullRequest":"0"}}`,
		`{"variantId":"release-on-merge","input":{}}`,
		`{"variantId":"release-on-merge","input":{"pullRequest":"42","dispatchMode":"publish"}}`,
		`{"variantId":"publish","input":{"pullRequest":"42"}}`,
		`{"variantId":"other","input":{}}`,
	} {
		response := postJSON(t, ui, "/api/processes/go-infra/plan", body)
		status := response.StatusCode
		closeResponse(t, response)
		if status != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, status)
		}
	}
	stats := github.stats()
	if stats.labelCalls != 0 || len(stats.dispatches) != 0 || store.count() != 0 {
		t.Fatalf("label calls = %d, dispatches = %v, work items = %d", stats.labelCalls, stats.dispatches, store.count())
	}
}

func TestGoInfraPlanDoesNotPersistBeforeStart(t *testing.T) {
	store := newMemoryProcessRunStore()
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newGoInfraTestUI(t, github.integration(), releaseui.WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/processes/go-infra/plan", goInfraTestSelectionJSON(t, goInfraPlanInput{
		Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun,
	}))
	status := response.StatusCode
	closeResponse(t, response)
	if status != http.StatusOK {
		t.Fatalf("plan status = %d", status)
	}
	if count := store.count(); count != 0 {
		t.Fatalf("process run count = %d, want 0", count)
	}
}

var _ releaseui.ReleaseRunStore = (*memoryProcessRunStore)(nil)
