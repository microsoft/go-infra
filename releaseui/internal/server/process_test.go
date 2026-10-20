// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/localserver"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

func exampleProcess() *fakeProcess {
	process := &fakeProcess{
		definition: testProcessDefinition("example"),
		plan: &contract.Plan{
			Subtitle: "Run example", ExecutionButtonLabel: "Run example",
			Facts: []contract.PlanFact{{Label: "Value", Value: "fixed", Detail: "More information"}},
		},
		view: &contract.RunView{Summary: "Ready"},
	}
	process.prepare = func(context.Context, any) (*contract.StateSnapshot, error) {
		return &contract.StateSnapshot{
			Input: json.RawMessage(`{}`), State: json.RawMessage(`{"value":"fixed"}`),
		}, nil
	}
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error { return nil }), nil
	}
	return process
}

func exampleProcessSteps(action func(context.Context) error) []*coordinator.Step {
	return []*coordinator.Step{coordinator.NewRootStep("Run example", time.Minute, action)}
}

func prepareExample(t *testing.T, server *Server) webview.ProcessRunResponse[*contract.Plan] {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", response.Code, response.Body.String())
	}
	var plan webview.ProcessRunResponse[*contract.Plan]
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestProcessPlanUsesBrowserJSONShape(t *testing.T) {
	server, err := New(
		context.Background(),
		WithProcesses(newFakeProcessGroup(exampleProcess())),
		WithReleaseRunStore(newMemoryProcessRunStore()),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://localhost/api/processes/example/plan",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", response.Code, response.Body.String())
	}

	var payload struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(payload.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Subtitle", "Facts", "ExecutionButtonLabel"} {
		if _, ok := plan[key]; !ok {
			t.Errorf("plan JSON has no %q field: %s", key, payload.Plan)
		}
	}
	for _, key := range []string{"subtitle", "facts", "executionButtonLabel"} {
		if _, ok := plan[key]; ok {
			t.Errorf("plan JSON unexpectedly has %q field: %s", key, payload.Plan)
		}
	}

	var facts []map[string]json.RawMessage
	if err := json.Unmarshal(plan["Facts"], &facts); err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("plan facts = %s", plan["Facts"])
	}
	for _, key := range []string{"Label", "Value", "Detail"} {
		if _, ok := facts[0][key]; !ok {
			t.Errorf("plan fact JSON has no %q field: %s", key, plan["Facts"])
		}
	}
	for _, key := range []string{"label", "value", "detail"} {
		if _, ok := facts[0][key]; ok {
			t.Errorf("plan fact JSON unexpectedly has %q field: %s", key, plan["Facts"])
		}
	}
}

func TestCurrentPlanIncludesProcessOwnedRunLink(t *testing.T) {
	const runURL = "https://example.test/runs/888"
	process := exampleProcess()
	process.view.RunURL = runURL
	ui := newTestUIWithProcess(t, process, WithReleaseRunStore(newMemoryProcessRunStore()))
	prepared := postJSON(t, ui, "/api/processes/example/plan", `{}`)
	defer prepared.Body.Close()
	if prepared.StatusCode != http.StatusOK {
		t.Fatalf("prepare status = %d", prepared.StatusCode)
	}
	response, err := ui.client.Get(ui.http.URL + "/api/processes/example/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var current struct {
		Execution map[string]json.RawMessage `json:"execution"`
	}
	decodeResponse(t, response, &current)
	var got string
	if err := json.Unmarshal(current.Execution["runUrl"], &got); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || got != runURL {
		t.Fatalf("current status = %d, run URL = %q", response.StatusCode, got)
	}
}

func startExample(t *testing.T, server *Server, digest string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "http://localhost/api/processes/example/start",
		strings.NewReader(`{"planDigest":"`+digest+`","confirmed":true}`),
	)
	request.Header.Set("Origin", "http://localhost")
	server.handleStartProcessRun("example", response, request)
	return response
}

func TestProcessPlanIsReadBeforeBuildAndExecutionUsesFreshRun(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	var builds atomic.Int32
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		run *fakeReleaseRun,
		checkpoint contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		builds.Add(1)
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			run.setState(json.RawMessage(`{"value":"executed"}`))
			checkpoint()
			return nil
		}), nil
	}
	server, err := New(
		context.Background(),
		WithProcesses(newFakeProcessGroup(process)),
		WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := prepareExample(t, server)
	if plan.Plan == nil || plan.Plan.Subtitle != "Run example" || builds.Load() != 1 {
		t.Fatalf("plan = %#v, builds = %d", plan.Plan, builds.Load())
	}
	response := startExample(t, server, plan.Execution.PlanDigest)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status = %d, body = %s", response.Code, response.Body.String())
	}
	store.waitForCompleted(t, 1)
	if !executed.Load() || builds.Load() != 2 {
		t.Fatalf("executed = %v, builds = %d", executed.Load(), builds.Load())
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != string(runstore.StatusSucceeded) ||
		string(persisted.Snapshot.State) != `{"value":"executed"}` {

		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestBlockingPreflightPreventsPreparation(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	process.preflight = func(context.Context) (error, error) {
		return nil, errors.New("not configured")
	}
	server, err := New(
		context.Background(),
		WithProcesses(newFakeProcessGroup(process)),
		WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusPreconditionFailed || store.count() != 0 {
		t.Fatalf("prepare status = %d, records = %d", response.Code, store.count())
	}
}

func TestProcessRunCreationFailurePreventsExecution(t *testing.T) {
	store := newMemoryProcessRunStore()
	store.createErr = errors.New("tracking unavailable")
	process := exampleProcess()
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			return nil
		}), nil
	}
	server, err := New(
		context.Background(),
		WithProcesses(newFakeProcessGroup(process)),
		WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := prepareExample(t, server)
	response := startExample(t, server, plan.Execution.PlanDigest)
	if response.Code != http.StatusInternalServerError || executed.Load() || store.count() != 0 {
		t.Fatalf("status = %d, executed = %v, records = %d", response.Code, executed.Load(), store.count())
	}
}

func TestRestoreIncompleteProcessRunResumesExecution(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	var executed atomic.Bool
	process.build = func(
		_ context.Context,
		_ *fakeReleaseRun,
		_ contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context) error {
			executed.Store(true)
			return nil
		}), nil
	}

	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), "Examples \u00b7 example", state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(
		context.Background(),
		WithProcesses(newFakeProcessGroup(process)),
		WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		response,
		authenticatedRequest(
			t, server, http.MethodPost, "/api/releases/"+strconv.Itoa(record.ID)+"/select", `{}`,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("select status = %d, body = %s", response.Code, response.Body.String())
	}
	store.waitForCompleted(t, 1)
	if !executed.Load() || store.latest(t).Result != string(runstore.StatusSucceeded) {
		t.Fatalf("executed = %v, run = %#v", executed.Load(), store.latest(t))
	}
}

type blockingResponseWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockingResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func authenticatedRequest(t *testing.T, server *Server, method, path, body string) *http.Request {
	t.Helper()
	launch, err := server.LaunchURL("http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	launchURL, err := url.Parse(launch)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost")
	request.AddCookie(&http.Cookie{
		Name: localserver.SessionCookieName, Value: launchURL.Query().Get("token"),
	})
	return request
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not reach its synchronization point")
	}
}

func assertProcessReadResponsive(t *testing.T, server *Server, path string) <-chan struct{} {
	t.Helper()
	response := httptest.NewRecorder()
	request := authenticatedRequest(t, server, http.MethodGet, path, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(response, request)
	}()
	select {
	case <-done:
		if response.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, body = %s", path, response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Errorf("GET %s waited for the blocked operation to finish", path)
	}
	return done
}

func TestProcessPlanResponseDoesNotBlockStateReads(t *testing.T) {
	process := exampleProcess()
	server, err := New(
		context.Background(), WithProcesses(newFakeProcessGroup(process)),
		WithReleaseRunStore(newMemoryProcessRunStore()),
	)
	if err != nil {
		t.Fatal(err)
	}
	prepareExample(t, server)
	release := make(chan struct{})
	writer := &blockingResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		entered:          make(chan struct{}),
		release:          release,
	}
	request := authenticatedRequest(t, server, http.MethodGet, "/api/processes/example/plan", "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(writer, request)
	}()
	var reader <-chan struct{}
	t.Cleanup(func() {
		close(release)
		<-done
		if reader != nil {
			<-reader
		}
	})
	waitForSignal(t, writer.entered)
	reader = assertProcessReadResponsive(t, server, "/api/processes/example/state")
}

func TestConcurrentPreparationKeepsPlansIndependent(t *testing.T) {
	one, two := exampleProcess(), exampleProcess()
	one.definition = testProcessDefinition("one")
	two.definition = testProcessDefinition("two")
	server, err := New(
		context.Background(), WithProcesses(newFakeProcessGroup(one, two)),
		WithReleaseRunStore(newMemoryProcessRunStore()),
	)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	writer := &blockingResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		entered:          make(chan struct{}),
		release:          release,
	}
	first := authenticatedRequest(t, server, http.MethodPost, "/api/processes/one/plan", `{}`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler().ServeHTTP(writer, first)
	}()
	t.Cleanup(func() { unblock(); <-done })
	waitForSignal(t, writer.entered)
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, authenticatedRequest(t, server, http.MethodPost, "/api/processes/two/plan", `{}`))
	if second.Code != http.StatusOK {
		t.Fatalf("second preparation: status = %d, body = %s", second.Code, second.Body.String())
	}
	unblock()
	<-done
	for _, processID := range []string{"one", "two"} {
		response := httptest.NewRecorder()
		path := "/api/processes/" + processID + "/plan"
		server.Handler().ServeHTTP(response, authenticatedRequest(t, server, http.MethodGet, path, ""))
		if response.Code != http.StatusOK {
			t.Fatalf("%s plan: status = %d, body = %s", processID, response.Code, response.Body.String())
		}
		var plan webview.ProcessRunResponse[*contract.Plan]
		if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		if plan.VariantID != processID {
			t.Fatalf("%s plan belongs to %q", processID, plan.VariantID)
		}
	}
}

func TestConfiguredProcessesCanRunConcurrently(t *testing.T) {
	entered := map[string]chan struct{}{"one": make(chan struct{}), "two": make(chan struct{})}
	release := map[string]chan struct{}{"one": make(chan struct{}), "two": make(chan struct{})}
	processes := make([]contract.Process, 0, 2)
	for _, processID := range []string{"one", "two"} {
		process := exampleProcess()
		process.definition = testProcessDefinition(processID)
		process.build = func(
			_ context.Context,
			_ *fakeReleaseRun,
			_ contract.CheckpointFunc,
		) ([]*coordinator.Step, error) {
			return exampleProcessSteps(func(context.Context) error {
				close(entered[processID])
				<-release[processID]
				return nil
			}), nil
		}
		processes = append(processes, process)
	}
	store := newMemoryProcessRunStore()
	server, err := New(
		context.Background(), WithProcesses(newFakeProcessGroup(processes...)),
		WithReleaseRunStore(store),
	)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	releaseRuns := func() {
		releaseOnce.Do(func() {
			close(release["one"])
			close(release["two"])
		})
	}
	t.Cleanup(releaseRuns)

	for _, processID := range []string{"one", "two"} {
		planResponse := httptest.NewRecorder()
		planPath := "/api/processes/" + processID + "/plan"
		server.Handler().ServeHTTP(
			planResponse,
			authenticatedRequest(t, server, http.MethodPost, planPath, `{}`),
		)
		if planResponse.Code != http.StatusOK {
			t.Fatalf("prepare %s: status = %d, body = %s", processID, planResponse.Code, planResponse.Body.String())
		}
		var plan webview.ProcessRunResponse[*contract.Plan]
		if err := json.Unmarshal(planResponse.Body.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		startBody, err := json.Marshal(webview.ReleaseStartRequest{PlanDigest: plan.Execution.PlanDigest, Confirmed: true})
		if err != nil {
			t.Fatal(err)
		}
		startResponse := httptest.NewRecorder()
		startPath := "/api/processes/" + processID + "/start"
		server.Handler().ServeHTTP(
			startResponse,
			authenticatedRequest(t, server, http.MethodPost, startPath, string(startBody)),
		)
		if startResponse.Code != http.StatusAccepted {
			t.Fatalf("start %s: status = %d, body = %s", processID, startResponse.Code, startResponse.Body.String())
		}
		waitForSignal(t, entered[processID])
	}

	releaseRuns()
	store.waitForCompleted(t, 2)
	records, err := store.Query(context.Background(), false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	for _, record := range records {
		if want := "Examples \u00b7 " + record.Run.ProcessID; record.Title != want {
			t.Fatalf("record title = %q, want %q", record.Title, want)
		}
	}
}
