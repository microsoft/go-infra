// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

type testUI struct {
	server *Server
	http   *httptest.Server
	client *http.Client
}

func newTestUI(t *testing.T, options ...Option) *testUI {
	t.Helper()
	return newTestUIWithProcess(t, exampleProcess(), options...)
}

func newTestUIWithProcess(t *testing.T, process contract.Process, options ...Option) *testUI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	options = append([]Option{WithProcesses(newFakeProcessGroup(process))}, options...)
	server, err := New(ctx, options...)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server.Handler())
	jar, err := cookiejar.New(nil)
	if err != nil {
		httpServer.Close()
		cancel()
		t.Fatal(err)
	}
	client := httpServer.Client()
	client.Jar = jar
	launchURL, err := server.LaunchURL(httpServer.URL)
	if err != nil {
		httpServer.Close()
		cancel()
		t.Fatal(err)
	}
	response, err := client.Get(launchURL)
	if err != nil {
		httpServer.Close()
		cancel()
		t.Fatal(err)
	}
	closeResponse(t, response)
	t.Cleanup(func() {
		cancel()
		httpServer.Close()
	})
	return &testUI{server: server, http: httpServer, client: client}
}

func TestDashboardQueriesReleaseRecords(t *testing.T) {
	starting := testProcessRun(t)
	starting.Started = true
	canceled := testProcessRun(t)
	canceled.Started = true
	canceled.Complete = true
	canceled.Result = resultCanceled
	succeeded := testProcessRun(t)
	succeeded.Started = true
	succeeded.Complete = true
	succeeded.Result = resultSucceeded

	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(3, canceled, true, time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)))
	seedReleaseRecord(store, testReleaseRunRecord(2, starting, false, time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)))
	seedReleaseRecord(store, testReleaseRunRecord(1, succeeded, true, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)))
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard dashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || dashboard.TrackingError != "" ||
		len(dashboard.Ongoing) != 1 || len(dashboard.NeedsAttention) != 0 || len(dashboard.Recent) != 2 {

		t.Fatalf("dashboard = %#v", dashboard)
	}
	if dashboard.Ongoing[0].RecordID != 2 || dashboard.Ongoing[0].Status != resultRunning ||
		dashboard.Recent[0].RecordID != 3 || dashboard.Recent[0].Status != resultCanceled ||
		dashboard.Recent[1].RecordID != 1 || dashboard.Recent[1].Status != resultSucceeded {

		t.Fatalf("dashboard groups = %#v, %#v, %#v", dashboard.Ongoing, dashboard.NeedsAttention, dashboard.Recent)
	}
}

func TestSelectReleaseRecordRestoresRun(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	run.Complete = true
	run.Result = resultSucceeded
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, run, true, time.Now().UTC()))
	seedReleaseRecord(store, testReleaseRunRecord(43, run, true, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/releases/42/select", `{}`)
	defer response.Body.Close()
	var selected map[string]string
	decodeResponse(t, response, &selected)
	if response.StatusCode != http.StatusOK || selected["href"] != "/example" {
		t.Fatalf("status = %d, selected = %#v", response.StatusCode, selected)
	}
	response, err := ui.client.Get(ui.http.URL + "/api/processes/example/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var plan processRunResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK || plan.Execution.Record == nil || plan.Execution.Record.ID != 42 {
		t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
	}
	response = postJSON(t, ui, "/api/releases/42/select", `{}`)
	defer response.Body.Close()
	decodeResponse(t, response, &selected)
	if response.StatusCode != http.StatusOK || selected["href"] != "/example" {
		t.Fatalf("reopen status = %d, selected = %#v", response.StatusCode, selected)
	}
	response = postJSON(t, ui, "/api/releases/43/select", `{}`)
	closeResponse(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("different record status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
}

func TestRestoringReleaseDoesNotBlockReads(t *testing.T) {
	process := exampleProcess()
	entered := make(chan struct{})
	release := make(chan struct{})
	process.build = func(
		ctx context.Context,
		run *fakeReleaseRun,
		checkpoint contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		close(entered)
		<-release
		return exampleProcessSteps(func(context.Context) error { return nil }), nil
	}
	store := newMemoryProcessRunStore()
	run := testProcessRun(t)
	run.Started, run.Complete, run.Result = true, true, resultSucceeded
	seedReleaseRecord(store, testReleaseRunRecord(42, run, true, time.Now().UTC()))
	ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
	request := authenticatedRequest(t, ui.server, http.MethodPost, "/api/releases/42/select", `{}`)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ui.server.Handler().ServeHTTP(response, request)
	}()
	var reader <-chan struct{}
	t.Cleanup(func() {
		close(release)
		<-done
		if reader != nil {
			<-reader
		}
	})
	waitForSignal(t, entered)
	reader = assertProcessReadResponsive(t, ui.server, "/api/processes/example/state")
}

func TestRestoringReleaseDoesNotReplaceConcurrentPreparation(t *testing.T) {
	process := exampleProcess()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	process.build = func(
		ctx context.Context,
		run *fakeReleaseRun,
		checkpoint contract.CheckpointFunc,
	) ([]*coordinator.Step, error) {
		if checkpoint != nil {
			close(entered)
			<-release
		}
		return exampleProcessSteps(func(context.Context) error { return nil }), nil
	}
	store := newMemoryProcessRunStore()
	run := testProcessRun(t)
	run.Started, run.Complete, run.Result = true, true, resultSucceeded
	seedReleaseRecord(store, testReleaseRunRecord(42, run, true, time.Now().UTC()))
	ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
	selected := httptest.NewRecorder()
	selectionRequest := authenticatedRequest(t, ui.server, http.MethodPost, "/api/releases/42/select", `{}`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ui.server.Handler().ServeHTTP(selected, selectionRequest)
	}()
	prepared := httptest.NewRecorder()
	preparationDone := make(chan struct{})
	preparationStarted := false
	t.Cleanup(func() {
		unblock()
		<-done
		if preparationStarted {
			<-preparationDone
		}
	})
	waitForSignal(t, entered)
	request := authenticatedRequest(t, ui.server, http.MethodPost, "/api/processes/example/plan", `{}`)
	preparationStarted = true
	go func() {
		defer close(preparationDone)
		ui.server.Handler().ServeHTTP(prepared, request)
	}()
	select {
	case <-preparationDone:
		if prepared.Code != http.StatusOK {
			t.Fatalf("preparation: status = %d, body = %s", prepared.Code, prepared.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Error("preparation waited for restore reconstruction")
		unblock()
		<-preparationDone
	}
	unblock()
	<-done
	if selected.Code != http.StatusConflict {
		t.Errorf("restore replaced the concurrent plan: status = %d, body = %s", selected.Code, selected.Body.String())
	}
	latest := httptest.NewRecorder()
	ui.server.Handler().ServeHTTP(latest, authenticatedRequest(t, ui.server, http.MethodGet, "/api/processes/example/plan", ""))
	var plan processRunResponse
	if err := json.Unmarshal(latest.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if latest.Code != http.StatusOK || plan.Execution.Record != nil || plan.Execution.Run.Complete {
		t.Fatalf("prepared plan was lost: status = %d, plan = %#v", latest.Code, plan)
	}
}

func TestExportImportReleaseRecord(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, run, false, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseRunStore(store))

	response, err := ui.client.Get(ui.http.URL + "/api/releases/42/export")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var exported releaseRecordExport
	decodeResponse(t, response, &exported)
	if response.StatusCode != http.StatusOK || exported.ID != 42 || exported.Revision != 1 {
		t.Fatalf("status = %d, export = %#v", response.StatusCode, exported)
	}
	exported.Run.Complete = true
	exported.Run.Result = resultUncertain
	response = postJSONValue(t, ui, "/api/releases/42/import", exported)
	defer response.Body.Close()
	var updated releaseRecordExport
	decodeResponse(t, response, &updated)
	if response.StatusCode != http.StatusOK || updated.Revision != 2 || updated.Run.Result != resultUncertain {
		t.Fatalf("status = %d, update = %#v", response.StatusCode, updated)
	}
	response = postJSONValue(t, ui, "/api/releases/42/import", exported)
	closeResponse(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale import status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
}

func TestExportRejectsMalformedStoreRecord(t *testing.T) {
	store := newMemoryProcessRunStore()
	store.records[42] = &releaseRunRecord{ID: 42, Revision: 1}
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/releases/42/export")
	if err != nil {
		t.Fatal(err)
	}
	closeResponse(t, response)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadGateway)
	}
}

func TestImportRejectsChangedIntentAndInvalidState(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, run, false, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseRunStore(store))
	for _, test := range []struct {
		name   string
		change func(*releaseRecordExport)
	}{
		{name: "intent", change: func(exported *releaseRecordExport) {
			exported.Run.Digest = strings.Repeat("b", 64)
		}},
		{name: "input", change: func(exported *releaseRecordExport) {
			exported.Run.Snapshot.Input = json.RawMessage(`{"changed":true}`)
		}},
		{name: "status", change: func(exported *releaseRecordExport) {
			exported.Run.Complete = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, err := store.Get(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			exported := exportReleaseRunRecord(record)
			test.change(&exported)
			response := postJSONValue(t, ui, "/api/releases/42/import", exported)
			closeResponse(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestImportRejectsChangedReviewedPlan(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, run, false, time.Now().UTC()))
	process := exampleProcess()
	process.planForRun = func(snapshot *contract.StateSnapshot) *contract.Plan {
		return &contract.Plan{Subtitle: string(snapshot.State)}
	}
	ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
	record, err := store.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	exported := exportReleaseRunRecord(record)
	exported.Run.Snapshot.State = json.RawMessage(`{"value":"changed"}`)
	response := postJSONValue(t, ui, "/api/releases/42/import", exported)
	closeResponse(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
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

func postJSONValue(t *testing.T, ui *testUI, path string, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return postJSON(t, ui, path, string(data))
}

func decodeResponse(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer closeResponse(t, response)
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

func testReleaseRunRecord(id int, run *releaseRunState, closed bool, updatedAt time.Time) *releaseRunRecord {
	return &releaseRunRecord{
		ID: id, Revision: 1, URL: fmt.Sprintf("https://example.invalid/releases/%d", id),
		Closed: closed, UpdatedAt: updatedAt, Run: run.Clone(),
	}
}

func seedReleaseRecord(store *memoryProcessRunStore, record *releaseRunRecord) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[record.ID] = cloneReleaseRunRecord(record)
	if store.nextID <= record.ID {
		store.nextID = record.ID + 1
	}
}
