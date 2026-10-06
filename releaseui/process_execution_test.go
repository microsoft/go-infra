// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
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

func waitForProcessRun(t *testing.T, server *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.mu.Lock()
		running := server.processRunning
		server.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for process run")
		}
		time.Sleep(time.Millisecond)
	}
}

func prepareExample(t *testing.T, server *Server) processRunResponse {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/processes/example/plan", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://localhost")
	server.handlePrepareProcessRun("example", response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status = %d, body = %s", response.Code, response.Body.String())
	}
	var plan processRunResponse
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
	waitForProcessRun(t, server)
	if !executed.Load() || builds.Load() != 2 {
		t.Fatalf("executed = %v, builds = %d", executed.Load(), builds.Load())
	}
	persisted := store.latest(t)
	if !persisted.Complete || persisted.Result != resultSucceeded ||
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
	record, err := store.Create(context.Background(), state, process.view, process.plan)
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
	if err := server.restoreProcessRunRecord(record); err != nil {
		t.Fatal(err)
	}
	waitForProcessRun(t, server)
	if !executed.Load() || store.latest(t).Result != resultSucceeded {
		t.Fatalf("executed = %v, run = %#v", executed.Load(), store.latest(t))
	}
}

func TestCheckpointFailureCancelsExecutionContext(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}

	run, err := process.Load(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{
		ctx: ctx, processRun: run, processRunState: state,
		processRunRecord: record, processRunStore: store, processPlan: process.plan,
	}
	store.updateErr = errors.New("checkpoint failed")
	checkpointer := server.newProcessCheckpointer(state.Digest, run, cancel)
	checkpointer.Checkpoint()
	checkpointer.Close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want cancellation", ctx.Err())
	}
	if !checkpointer.failed.Load() {
		t.Fatal("checkpoint failure was not recorded")
	}
}

func TestProcessRunOutcome(t *testing.T) {
	checkpointErr := errors.New("checkpoint failed")
	for _, test := range []struct {
		name                string
		executionErr        error
		snapshotErr         error
		checkpointFailed    bool
		checkpointPersisted bool
		complete            bool
		result              string
	}{
		{name: "success", complete: true, result: resultSucceeded},
		{name: "failure", executionErr: errors.New("step failed"), complete: true, result: resultFailed},
		{name: "canceled before checkpoint", executionErr: context.Canceled, complete: true, result: resultUncertain},
		{name: "deadline before checkpoint", executionErr: context.DeadlineExceeded, complete: true, result: resultUncertain},
		{name: "canceled after checkpoint", executionErr: context.Canceled, checkpointPersisted: true},
		{name: "deadline after checkpoint", executionErr: context.DeadlineExceeded, checkpointPersisted: true},
		{name: "snapshot", snapshotErr: errors.New("snapshot failed"), complete: true, result: resultUncertain},
		{name: "checkpoint", executionErr: checkpointErr, checkpointFailed: true, complete: true, result: resultUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			complete, result := processRunOutcome(
				test.executionErr, test.snapshotErr, test.checkpointFailed, test.checkpointPersisted,
			)
			if complete != test.complete || result != test.result {
				t.Fatalf("outcome = (%v, %q), want (%v, %q)", complete, result, test.complete, test.result)
			}
		})
	}
}

func TestCheckpointWaitsForPersistence(t *testing.T) {
	store := newMemoryProcessRunStore()
	process := exampleProcess()
	state := testProcessRun(t)
	state.Started = true
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	updateStarted := make(chan struct{})
	updateRelease := make(chan struct{})
	store.updateStarted = updateStarted
	store.updateRelease = updateRelease
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{
		ctx: ctx, processRun: run, processRunState: state,
		processRunRecord: record, processRunStore: store, processPlan: process.plan,
	}
	checkpointer := server.newProcessCheckpointer(state.Digest, run, cancel)
	returned := make(chan struct{})
	go func() {
		checkpointer.Checkpoint()
		close(returned)
	}()
	select {
	case <-updateStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence did not start")
	}
	select {
	case <-returned:
		t.Fatal("Checkpoint returned before persistence completed")
	default:
	}
	close(updateRelease)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Checkpoint did not return after persistence completed")
	}
	checkpointer.Close()
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
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: launchURL.Query().Get("token")})
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

func TestProcessPersistenceDoesNotBlockReads(t *testing.T) {
	for _, phase := range []string{"checkpoint", "completion"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			process := exampleProcess()
			store := newMemoryProcessRunStore()
			server, err := New(ctx, WithProcesses(newFakeProcessGroup(process)), WithReleaseRunStore(store))
			if err != nil {
				t.Fatal(err)
			}
			state := testProcessRun(t)
			state.Started = true
			record, err := store.Create(ctx, state, process.view, process.plan)
			if err != nil {
				t.Fatal(err)
			}
			run, err := process.Load(state.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			server.activeProcessID = "example"
			server.processRun = run
			server.processRunState = record.Run.Clone()
			server.processRunRecord = record
			server.processPlan = process.plan
			server.processRunning = true
			server.steps = exampleProcessSteps(func(context.Context) error { return nil })
			entered := make(chan struct{})
			release := make(chan struct{})
			store.updateStarted = entered
			store.updateRelease = release
			checkpointer := server.newProcessCheckpointer(state.Digest, run, cancel)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if phase == "checkpoint" {
					checkpointer.Checkpoint()
				} else {
					server.executeProcessRun(state.Digest, ctx, server.runner, server.steps, run, checkpointer)
				}
			}()
			var readers []<-chan struct{}
			t.Cleanup(func() {
				close(release)
				<-done
				checkpointer.Close()
				for _, reader := range readers {
					<-reader
				}
			})
			waitForSignal(t, entered)
			for _, endpoint := range []string{"state", "plan"} {
				readers = append(readers, assertProcessReadResponsive(t, server, "/api/processes/example/"+endpoint))
			}
		})
	}
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

func TestConcurrentPreparationKeepsActiveProcessAndPlanTogether(t *testing.T) {
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
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedRequest(t, server, http.MethodGet, "/api/processes/two/plan", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("latest plan: status = %d, body = %s", response.Code, response.Body.String())
	}
	var plan processRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.VariantID != "two" {
		t.Fatalf("latest plan belongs to %q, want two", plan.VariantID)
	}
}

func TestProcessReadKeepsRunnerCapturedAtValidation(t *testing.T) {
	for _, endpoint := range []string{"state", "events"} {
		t.Run(endpoint, func(t *testing.T) {
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
			response := httptest.NewRecorder()
			server.prepareProcess("one")(response, authenticatedRequest(t, server, http.MethodPost, "/api/processes/one/plan", `{}`))
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			if err := server.runner.Execute(context.Background(), []*coordinator.Step{
				coordinator.NewRootStep("one runner", time.Minute, func(context.Context) error { return nil }),
			}); err != nil {
				t.Fatal(err)
			}
			validated := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			response = httptest.NewRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := authenticatedRequest(t, server, http.MethodGet, "/api/processes/one/"+endpoint, "").WithContext(ctx)
			handler := server.requireActiveProcess("one", func(w http.ResponseWriter, request *http.Request, bound processRequest) {
				close(validated)
				<-release
				if endpoint == "state" {
					server.handleState(w, request, bound)
				} else {
					server.handleEvents(w, request, bound)
				}
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler(response, request)
			}()
			t.Cleanup(func() { unblock(); cancel(); <-done })
			waitForSignal(t, validated)
			prepared := httptest.NewRecorder()
			server.prepareProcess("two")(prepared, authenticatedRequest(t, server, http.MethodPost, "/api/processes/two/plan", `{}`))
			if prepared.Code != http.StatusOK {
				t.Fatal(prepared.Body.String())
			}
			if err := server.runner.Execute(context.Background(), []*coordinator.Step{
				coordinator.NewRootStep("two runner", time.Minute, func(context.Context) error { return nil }),
			}); err != nil {
				t.Fatal(err)
			}
			cancel()
			unblock()
			<-done
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, "one runner") || strings.Contains(body, "two runner") {
				t.Fatalf("request changed runner after validation: status = %d, body = %s", response.Code, body)
			}
		})
	}
}

func checkpointTestRun(t *testing.T) (*Server, *memoryProcessRunStore, contract.Run) {
	t.Helper()
	process := exampleProcess()
	state := testProcessRun(t)
	state.Started = true
	store := newMemoryProcessRunStore()
	record, err := store.Create(context.Background(), state, process.view, process.plan)
	if err != nil {
		t.Fatal(err)
	}
	run, err := process.Load(state.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		ctx: context.Background(), processRun: run, processRunState: record.Run.Clone(),
		processRunRecord: record, processRunStore: store, processPlan: process.plan,
	}, store, run
}

func TestCheckpointDoesNotOverwriteChangedRun(t *testing.T) {
	for _, change := range []string{"digest", "record", "revision"} {
		t.Run(change, func(t *testing.T) {
			server, store, run := checkpointTestRun(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			store.updateStarted = entered
			store.updateRelease = release
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checkpointer := server.newProcessCheckpointer(server.processRunState.Digest, run, cancel)
			done := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			go func() {
				defer close(done)
				checkpointer.Checkpoint()
			}()
			t.Cleanup(func() { unblock(); <-done; checkpointer.Close() })
			waitForSignal(t, entered)
			server.mu.Lock()
			newState := server.processRunState.Clone()
			newRecord := cloneReleaseRunRecord(server.processRunRecord)
			switch change {
			case "digest":
				newState.Digest = strings.Repeat("b", 64)
			case "record":
				newRecord.ID++
			case "revision":
				newRecord.Revision++
			}
			server.processRunState = newState
			server.processRunRecord = newRecord
			server.mu.Unlock()
			unblock()
			waitForSignal(t, done)
			if !checkpointer.failed.Load() || checkpointer.persisted.Load() || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("stale checkpoint: failed = %v, persisted = %v, context = %v",
					checkpointer.failed.Load(), checkpointer.persisted.Load(), ctx.Err())
			}
			server.mu.Lock()
			unchanged := server.processRunState == newState && server.processRunRecord == newRecord
			server.mu.Unlock()
			if !unchanged {
				t.Fatal("the stale storage response replaced the changed run")
			}
		})
	}
}

func TestConcurrentCheckpointsStaySerialized(t *testing.T) {
	server, store, run := checkpointTestRun(t)
	store.updateStarted = make(chan struct{})
	release := make(chan struct{})
	store.updateRelease = release
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checkpointer := server.newProcessCheckpointer(server.processRunState.Digest, run, cancel)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		checkpointer.Checkpoint()
	}()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	secondRunning := false
	t.Cleanup(func() {
		unblock()
		<-firstDone
		if secondRunning {
			<-secondDone
		}
		checkpointer.Close()
	})
	waitForSignal(t, store.updateStarted)
	secondRunning = true
	go func() {
		defer close(secondDone)
		close(secondStarted)
		checkpointer.Checkpoint()
	}()
	waitForSignal(t, secondStarted)
	select {
	case <-secondDone:
		t.Fatal("the second checkpoint returned while the first write was blocked")
	default:
	}
	store.updateStarted = nil
	unblock()
	waitForSignal(t, firstDone)
	waitForSignal(t, secondDone)
	checkpointer.Close()
	if checkpointer.failed.Load() || ctx.Err() != nil {
		t.Fatalf("serialized checkpoints: failed = %v, context = %v", checkpointer.failed.Load(), ctx.Err())
	}
	record, err := store.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if record.Revision != 3 || !record.Run.Checkpointed {
		t.Fatalf("record after two checkpoints = %#v", record)
	}
}
