// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

const testGoInfraHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGoInfraGitHub struct {
	mu sync.Mutex

	preflightCalls  int
	getPRCalls      int
	labelCalls      int
	dispatches      []bool
	pollCalls       int
	workflowRun     GoInfraWorkflowRun
	workflowUpdates []GoInfraWorkflowRun
	pullRequest     GoInfraPullRequest
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

func (f *fakeGoInfraGitHub) integration() GitHubService {
	return f
}

func (f *fakeGoInfraGitHub) Preflight(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflightCalls++
	return "verified fake GitHub integration", f.preflightErr
}

func (f *fakeGoInfraGitHub) GetPullRequest(_ context.Context, number int) (GoInfraPullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getPRCalls++
	if number != f.pullRequest.Number {
		return GoInfraPullRequest{}, errors.New("unexpected pull request")
	}
	return f.pullRequest, nil
}

func (f *fakeGoInfraGitHub) AddReleaseOnMergeLabel(
	_ context.Context,
	number int,
	expectedHeadSHA string,
) (GoInfraPullRequest, error) {
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

func (f *fakeGoInfraGitHub) PollWorkflowRun(
	_ context.Context,
	id int64,
	report func(GoInfraWorkflowRun) error,
) (GoInfraWorkflowRun, error) {
	f.mu.Lock()
	f.pollCalls++
	workflowRun := f.workflowRun
	updates := append([]GoInfraWorkflowRun(nil), f.workflowUpdates...)
	pollErr := f.pollErr
	f.mu.Unlock()
	if id != workflowRun.ID {
		return GoInfraWorkflowRun{}, errors.New("unexpected workflow run")
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
		preflightCalls: f.preflightCalls, getPRCalls: f.getPRCalls,
		labelCalls: f.labelCalls, dispatches: append([]bool(nil), f.dispatches...),
		pollCalls: f.pollCalls,
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

func TestProcessGroupDefinesConcreteReleaseProcesses(t *testing.T) {
	group := NewProcess((&fakeGoInfraGitHub{}).integration())
	processes := group.Processes()
	if len(processes) != 3 {
		t.Fatalf("process count = %d, want 3", len(processes))
	}
	got := []string{
		processes[0].Definition().ID,
		processes[1].Definition().ID,
		processes[2].Definition().ID,
	}
	want := []string{"go-infra-release-on-merge", "go-infra-dry-run", "go-infra-publish"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("process IDs = %v, want %v", got, want)
		}
	}
}

func TestReleaseOnMergeProcess(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	process := NewProcess(github.integration()).Processes()[0]
	inputs := &releaseflag.InputSet{}
	form := process.InputForm(inputs).(*releaseOnMergeInput)
	if err := inputs.Parse(json.RawMessage(`{"pullRequest":"42"}`)); err != nil {
		t.Fatal(err)
	}
	if form.PullRequest != 42 {
		t.Fatalf("pull request = %d, want 42", form.PullRequest)
	}
	snapshot, err := process.Prepare(context.Background(), form)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan := run.Plan(); plan == nil || plan.ExecutionButtonLabel != "Apply release-on-merge" {
		t.Fatalf("plan = %#v", plan)
	}
	steps, err := run.Build(context.Background(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&coordinator.StepRunner{}).Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if stats := github.stats(); stats.getPRCalls != 1 || stats.labelCalls != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestDryRunCheckpointsWorkflowState(t *testing.T) {
	github := &fakeGoInfraGitHub{
		workflowUpdates: []GoInfraWorkflowRun{
			testGoInfraWorkflowRun("in_progress", ""),
			testGoInfraWorkflowRun("completed", "success"),
		},
	}
	process := NewProcess(github.integration()).Processes()[1]
	snapshot, err := process.Prepare(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := 0
	steps, err := run.Build(context.Background(), func() {
		checkpoints++
		if _, err := process.Load(run.TakeSnapshot()); err != nil {
			t.Errorf("load checkpoint: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&coordinator.StepRunner{}).Execute(context.Background(), steps); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 3 {
		t.Fatalf("checkpoint count = %d, want 3", checkpoints)
	}
	view := run.TakeView()
	if !view.Test || view.Summary != "GitHub workflow completed" {
		t.Fatalf("view = %#v", view)
	}
}

func TestGoInfraServerRunsSelectedConcreteProcess(t *testing.T) {
	github := &fakeGoInfraGitHub{}
	store := newMemoryProcessRunStore()
	server, err := releaseui.New(
		context.Background(),
		releaseui.WithProcesses(NewProcess(github.integration())),
		releaseui.WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	defer httpServer.Close()
	client := httpServer.Client()
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	launchURL, err := server.LaunchURL(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	post := func(path, body string) *http.Response {
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", httpServer.URL)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response = post("/api/processes/go-infra-dry-run/plan", `{}`)
	var prepared struct {
		Execution struct {
			PlanDigest string `json:"planDigest"`
		} `json:"execution"`
	}
	if err := json.NewDecoder(response.Body).Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response = post(
		"/api/processes/go-infra-dry-run/start",
		fmt.Sprintf(`{"planDigest":%q,"confirmed":true}`, prepared.Execution.PlanDigest),
	)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for store.latest() == nil || !store.latest().Complete {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for release")
		}
		time.Sleep(time.Millisecond)
	}
	if stats := github.stats(); len(stats.dispatches) != 1 || !stats.dispatches[0] || stats.pollCalls != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

type memoryProcessRunStore struct {
	mu      sync.Mutex
	nextID  int
	records map[int]*releaseui.ReleaseRunRecord
}

func newMemoryProcessRunStore() *memoryProcessRunStore {
	return &memoryProcessRunStore{nextID: 1, records: make(map[int]*releaseui.ReleaseRunRecord)}
}

func (s *memoryProcessRunStore) Create(
	_ context.Context,
	run *releaseui.ReleaseRunState,
	_ *contract.RunView,
	_ *contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := &releaseui.ReleaseRunRecord{
		ID: s.nextID, Revision: 1,
		URL:       fmt.Sprintf("https://example.invalid/releases/%d", s.nextID),
		UpdatedAt: run.UpdatedAt, Run: run.Clone(),
	}
	s.records[record.ID] = record
	s.nextID++
	return cloneRecord(record), nil
}

func (s *memoryProcessRunStore) Get(_ context.Context, id int) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("release record not found")
	}
	return cloneRecord(record), nil
}

func (s *memoryProcessRunStore) Query(context.Context, bool, int) ([]*releaseui.ReleaseRunRecord, error) {
	return []*releaseui.ReleaseRunRecord{}, nil
}

func (s *memoryProcessRunStore) Update(
	_ context.Context,
	current *releaseui.ReleaseRunRecord,
	run *releaseui.ReleaseRunState,
	_ *contract.RunView,
	_ *contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := &releaseui.ReleaseRunRecord{
		ID: current.ID, Revision: current.Revision + 1,
		URL: current.URL, UpdatedAt: time.Now().UTC(), Run: run.Clone(),
	}
	s.records[record.ID] = record
	return cloneRecord(record), nil
}

func (s *memoryProcessRunStore) latest() *releaseui.ReleaseRunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		return record.Run.Clone()
	}
	return nil
}

func cloneRecord(record *releaseui.ReleaseRunRecord) *releaseui.ReleaseRunRecord {
	clone := *record
	clone.Run = record.Run.Clone()
	return &clone
}

var _ releaseui.ReleaseRunStore = (*memoryProcessRunStore)(nil)
