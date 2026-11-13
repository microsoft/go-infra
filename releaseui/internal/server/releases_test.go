// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
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
	t.Cleanup(func() { cancel(); httpServer.Close() })
	return &testUI{server: server, http: httpServer, client: client}
}

func TestPublishedViewRoutesDoNotLoadProcessOrCheckpoint(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			state := testProcessRun(t)
			state.Started = true
			store := newMemoryProcessRunStore()
			seedReleaseRecord(store, testReleaseRunRecord(42, state, false, time.Now().UTC()))
			if foreign {
				store.views[42].ProcessID = "foreign-project"
			}
			store.records[42].Run = nil
			store.getErr = errors.New("checkpoint cannot be decoded by this version")
			process := exampleProcess()
			process.preflight = func(context.Context) (error, error) { t.Error("viewer called preflight"); return nil, nil }
			process.build = func(*fakeReleaseRun) ([]*coordinator.Step, error) {
				t.Error("viewer built an executable graph")
				return nil, errors.New("forbidden")
			}
			ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
			for _, path := range []string{"/releases/42", "/api/releases/42/view", "/api/dashboard"} {
				response, err := ui.client.Get(ui.http.URL + path)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusOK {
					t.Errorf("%s status=%d", path, response.StatusCode)
				}
				closeResponse(t, response)
			}
			response, err := ui.client.Get(ui.http.URL + "/api/releases/42/view")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var data webview.ReleaseViewResponse[*runstore.ViewRecord]
			decodeResponse(t, response, &data)
			if data.Record.View.Graph.Steps[0].Name != "Run example" ||
				foreign && data.ResumeURL != "" || !foreign && data.ResumeURL == "" {

				t.Fatalf("published view=%#v", data)
			}
			if store.records[42].Revision != 1 || ui.server.runs.Running("example") {
				t.Fatal("reading published data changed execution state")
			}
		})
	}
}

func TestDashboardUsesPublishedLifecycleStatus(t *testing.T) {
	store := newMemoryProcessRunStore()
	for id, status := range []runstore.Status{
		runstore.StatusRunning, runstore.StatusRunning, runstore.StatusSucceeded, runstore.StatusCanceled, runstore.StatusRunning,
	} {
		run := testProcessRun(t)
		run.Started = true
		run.Executing = id == 0 || id == 4
		if status != runstore.StatusRunning {
			run.Complete, run.Result = true, string(status)
		}
		seedReleaseRecord(store, testReleaseRunRecord(id+1, run, id == 3 || id == 4, time.Now().Add(time.Duration(id)*time.Second)))
	}
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if dashboard.TrackingError != "" || len(dashboard.Ongoing) != 4 || len(dashboard.Recent) != 1 ||
		dashboard.Ongoing[0].RecordID != 5 || !dashboard.Ongoing[0].Closed || dashboard.Ongoing[0].Status != "running" ||
		dashboard.Ongoing[1].Status != "succeeded" || dashboard.Ongoing[1].Closed || dashboard.Ongoing[2].Status != "saved" {

		t.Fatalf("dashboard=%#v", dashboard)
	}
}

func TestDashboardRetainsValidViewsAndReadOnlyProblems(t *testing.T) {
	store := newMemoryProcessRunStore()
	state := testProcessRun(t)
	state.Started = true
	seedReleaseRecord(store, testReleaseRunRecord(42, state, false, time.Now()))
	store.queryErr = errors.Join(errors.New("tracking temporarily unavailable"), &runstore.RecordError{
		ID: 43, Title: "Other release", Closed: true, SavedStatus: "failed", RecordState: "Closed",
		Message: "Cannot open in this session.", Guidance: "Open the work item.", Err: errors.New("unsupported view"),
	})
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if len(dashboard.Ongoing) != 1 || len(dashboard.Recent) != 1 ||
		dashboard.Recent[0].Issue == nil || !strings.Contains(dashboard.TrackingError, "tracking temporarily unavailable") {

		t.Fatalf("partial dashboard=%#v", dashboard)
	}
}

func TestClosedAndRecentlyReservedViewsCannotOfferResume(t *testing.T) {
	for _, closed := range []bool{false, true} {
		store := newMemoryProcessRunStore()
		run := testProcessRun(t)
		run.Started = true
		run.Executing = !closed
		seedReleaseRecord(store, testReleaseRunRecord(42, run, closed, time.Now()))
		ui := newTestUI(t, WithReleaseRunStore(store))
		response, err := ui.client.Get(ui.http.URL + "/api/releases/42/view")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var data webview.ReleaseViewResponse[*runstore.ViewRecord]
		decodeResponse(t, response, &data)
		if data.ResumeURL != "" {
			t.Fatal("closed or owned record offered execution")
		}
	}
}

func TestStaleReleaseResumesThroughExistingEndpoint(t *testing.T) {
	store := newMemoryProcessRunStore()
	state := testProcessRun(t)
	state.Started, state.Checkpointed, state.Executing = true, true, true
	seedReleaseRecord(store, testReleaseRunRecord(42, state, false, time.Now().Add(-2*time.Minute)))
	executions := new(atomic.Int32)
	process := exampleProcess()
	process.build = func(*fakeReleaseRun) ([]*coordinator.Step, error) {
		return exampleProcessSteps(func(context.Context, coordinator.StepControl) error {
			executions.Add(1)
			return coordinator.ErrCanceled
		}), nil
	}
	ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/releases/42/view")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var saved webview.ReleaseViewResponse[*runstore.ViewRecord]
	decodeResponse(t, response, &saved)
	if saved.ResumeURL != "/api/processes/example/resume" || saved.Record.Revision != 1 ||
		!saved.Record.View.Executing || executions.Load() != 0 {

		t.Fatalf("opening the stale release changed or hid it: %#v", saved)
	}
	request := webview.ReleaseStartRequest{RecordID: 42, Revision: 1, PlanDigest: state.Digest}
	response = postJSONValue(t, ui, saved.ResumeURL, request)
	closeResponse(t, response)
	if response.StatusCode != http.StatusBadRequest || executions.Load() != 0 {
		t.Fatal("Resume ran without confirmation")
	}
	request.Confirmed = true
	response = postJSONValue(t, ui, saved.ResumeURL, request)
	closeResponse(t, response)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("Resume status = %d, want accepted", response.StatusCode)
	}
	store.waitForCompleted(t, 1)
	record, err := store.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if record.Run.Executing || record.Run.Result != string(runstore.StatusCanceled) ||
		record.Revision != 3 || record.Closed || store.count() != 1 || executions.Load() != 1 {

		t.Fatalf("Resume did not reuse and finish the stale record: %#v, executions=%d", record, executions.Load())
	}
}

func TestExportImportReleaseRecord(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, run, false, time.Now()))
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/releases/42/export")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var exported webview.ReleaseRecordExport[*runstore.State]
	decodeResponse(t, response, &exported)
	exported.Run.Complete, exported.Run.Result = true, string(runstore.StatusUncertain)
	response = postJSONValue(t, ui, "/api/releases/42/import", exported)
	defer response.Body.Close()
	var updated webview.ReleaseRecordExport[*runstore.State]
	decodeResponse(t, response, &updated)
	if response.StatusCode != http.StatusOK || updated.Revision != 2 || updated.Run.Result != "uncertain" {
		t.Fatalf("repair=%#v, status=%d", updated, response.StatusCode)
	}
	response = postJSONValue(t, ui, "/api/releases/42/import", exported)
	closeResponse(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatal("stale repair accepted")
	}
}

func TestImportRejectsChangedIntentAndInvalidState(t *testing.T) {
	for _, change := range []func(*runstore.State){
		func(run *runstore.State) { run.Digest = "changed" },
		func(run *runstore.State) { run.Snapshot.Input = json.RawMessage(`{"changed":true}`) },
		func(run *runstore.State) { run.Complete = true },
		func(run *runstore.State) { run.Executing = true },
	} {
		run := testProcessRun(t)
		run.Started = true
		store := newMemoryProcessRunStore()
		record := testReleaseRunRecord(42, run, false, time.Now())
		seedReleaseRecord(store, record)
		ui := newTestUI(t, WithReleaseRunStore(store))
		exported := testExportReleaseRunRecord(record)
		change(exported.Run)
		response := postJSONValue(t, ui, "/api/releases/42/import", exported)
		closeResponse(t, response)
		if response.StatusCode != http.StatusBadRequest || store.records[42].Revision != 1 {
			t.Fatalf("invalid repair status=%d", response.StatusCode)
		}
	}
}

func testExportReleaseRunRecord(record *runstore.Record) webview.ReleaseRecordExport[*runstore.State] {
	return webview.ReleaseRecordExport[*runstore.State]{ID: record.ID, Revision: record.Revision, URL: record.URL, Run: record.Run.Clone()}
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
		t.Errorf("discard response: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("close response: %v", err)
	}
}

func testReleaseRunRecord(id int, run *runstore.State, closed bool, updatedAt time.Time) *runstore.Record {
	return &runstore.Record{
		ID: id, Revision: 1, URL: fmt.Sprintf("https://example.invalid/releases/%d", id),
		Closed: closed, UpdatedAt: updatedAt, Run: run.Clone(),
	}
}

func seedReleaseRecord(store *memoryProcessRunStore, record *runstore.Record) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[record.ID] = cloneReleaseRunRecord(record)
	store.views[record.ID] = testStoredView(record.Run, record.UpdatedAt)
	if store.nextID <= record.ID {
		store.nextID = record.ID + 1
	}
}

func testStoredView(run *runstore.State, updatedAt time.Time) *runstore.View {
	status := runstore.StatusRunning
	if run.Complete {
		status = runstore.Status(run.Result)
	}
	return &runstore.View{
		ProcessID: run.ProcessID, Digest: run.Digest, Name: "Examples", Mark: "EX",
		Status: status, Executing: run.Executing, UpdatedAt: updatedAt,
		Graph: coordinator.Snapshot{Steps: []coordinator.StepSnapshot{{Name: "Run example", Status: coordinator.StepStatusWaiting}}},
	}
}
