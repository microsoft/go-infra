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
	"sync"
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
	canceled.Result = string(runstore.StatusCanceled)
	succeeded := testProcessRun(t)
	succeeded.Started = true
	succeeded.Complete = true
	succeeded.Result = string(runstore.StatusSucceeded)

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
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || dashboard.TrackingError != "" ||
		len(dashboard.Ongoing) != 1 || len(dashboard.NeedsAttention) != 0 || len(dashboard.Recent) != 2 {

		t.Fatalf("dashboard = %#v", dashboard)
	}
	if dashboard.Ongoing[0].RecordID != 2 || dashboard.Ongoing[0].Status != string(runstore.StatusRunning) ||
		dashboard.Recent[0].RecordID != 3 || dashboard.Recent[0].Status != string(runstore.StatusCanceled) ||
		dashboard.Recent[1].RecordID != 1 || dashboard.Recent[1].Status != string(runstore.StatusSucceeded) {

		t.Fatalf("dashboard groups = %#v, %#v, %#v", dashboard.Ongoing, dashboard.NeedsAttention, dashboard.Recent)
	}
}

func TestDashboardRetainsReleasesWhenQueryReturnsPartialResults(t *testing.T) {
	running := testProcessRun(t)
	running.Started = true
	completed := testProcessRun(t)
	completed.Started, completed.Complete, completed.Result = true, true, string(runstore.StatusSucceeded)
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, running, false, time.Now().UTC()))
	seedReleaseRecord(store, testReleaseRunRecord(43, completed, true, time.Now().UTC()))
	store.queryErr = errors.New("release record 41: unsupported stored payload")
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || len(dashboard.Ongoing) != 1 || len(dashboard.Recent) != 1 {
		t.Fatalf("status = %d, dashboard = %#v", response.StatusCode, dashboard)
	}
	if dashboard.Ongoing[0].RecordID != 42 || dashboard.Recent[0].RecordID != 43 ||
		!strings.Contains(dashboard.TrackingError, "release record 41") {

		t.Fatalf("partial dashboard = %#v", dashboard)
	}
}

func TestDashboardGroupsReadOnlyRecordsByWorkItemState(t *testing.T) {
	running := testProcessRun(t)
	running.Started = true
	unknownProcess := running.Clone()
	unknownProcess.ProcessID = "no-longer-configured"
	store := newMemoryProcessRunStore()
	seedReleaseRecord(store, testReleaseRunRecord(42, running, false, time.Now().UTC()))
	unknownRecord := testReleaseRunRecord(45, unknownProcess, false, time.Now().UTC())
	unknownRecord.Title = "Older infrastructure dry run"
	seedReleaseRecord(store, unknownRecord)
	updatedAt := time.Date(2026, 9, 16, 23, 6, 48, 0, time.UTC)
	store.queryErr = fmt.Errorf("query failed for some records: %w", errors.Join(
		&runstore.RecordError{
			ID: 41, URL: "https://example.invalid/workitems/41", RecordState: "Active", SavedStatus: "canceled",
			Title: "Go images test release: 1.26.8-1, 1.27.1-2", UpdatedAt: updatedAt,
			Message: "Saved-data format 8 is not supported.", Guidance: "Leave the saved JSON unchanged.",
			Err: errors.New(`decode process run: json: unknown field "schemaVersion"`),
		},
		&runstore.RecordError{
			ID: 44, URL: "https://example.invalid/workitems/44", RecordState: "Closed", SavedStatus: "running",
			Closed: true,
			Title:  "Earlier Go images test release", UpdatedAt: updatedAt,
			Message: "Work-item state and saved release status disagree.", Guidance: "Check the external run first.",
			Err: errors.New(`state "Closed" is incompatible with release status "running"`),
		},
		errors.New("tracking service unavailable"),
	))
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || len(dashboard.Ongoing) != 1 ||
		len(dashboard.NeedsAttention) != 2 || len(dashboard.Recent) != 1 {

		t.Fatalf("status = %d, dashboard = %#v", response.StatusCode, dashboard)
	}
	for _, release := range append(dashboard.NeedsAttention, dashboard.Recent...) {
		if release.Issue == nil || release.RecordURL == "" || release.Href != "" ||
			release.Issue.Message == "" || release.Issue.Guidance == "" || release.Issue.Detail == "" {

			t.Errorf("unreadable card = %#v", release)
			continue
		}
		switch release.RecordID {
		case 41:
			if release.Status != "canceled" || release.Issue.RecordState != "Active" {
				t.Errorf("unsupported-format card = %#v", release)
			}
			if release.Name != "Go images test release: 1.26.8-1, 1.27.1-2" || !release.UpdatedAt.Equal(updatedAt) {
				t.Errorf("unsupported-format card lost its readable identity: %#v", release)
			}
		case 44:
			if release.Status != "running" || release.Issue.RecordState != "Closed" {
				t.Errorf("state-conflict card = %#v", release)
			}
			if release.Name != "Earlier Go images test release" {
				t.Errorf("read-only history card title = %q", release.Name)
			}
		case 45:
			if release.Status != "running" {
				t.Errorf("unconfigured-process card = %#v", release)
			}
			if release.Name != "Older infrastructure dry run" {
				t.Errorf("unconfigured process lost its saved title: %#v", release)
			}
		default:
			t.Errorf("unexpected card = %#v", release)
		}
	}
	if !strings.Contains(dashboard.TrackingError, "tracking service unavailable") ||
		strings.Contains(dashboard.TrackingError, "schemaVersion") || strings.Contains(dashboard.TrackingError, "incompatible") {

		t.Fatalf("unclassified tracking error = %q", dashboard.TrackingError)
	}
}

func TestDashboardWithoutDescriptiveMetadataDoesNotUseTheIDAsItsName(t *testing.T) {
	store := newMemoryProcessRunStore()
	store.queryErr = &runstore.RecordError{
		ID: 41, URL: "https://example.invalid/workitems/41",
		Message: "Saved release metadata is unavailable.", Guidance: "Open the tracking item.",
		Err: errors.New("unreadable record"),
	}
	ui := newTestUI(t, WithReleaseRunStore(store))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if len(dashboard.NeedsAttention) != 1 || dashboard.NeedsAttention[0].Name != "Saved release" {
		t.Fatalf("missing-metadata card = %#v", dashboard.NeedsAttention)
	}
	data, err := json.Marshal(dashboard.NeedsAttention[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"updatedAt"`) {
		t.Fatalf("missing update time became a fabricated date: %s", data)
	}
}

func TestDashboardInspectsStaleHistoryWithoutExecutionOrWrites(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        string
		closed        bool
		inspectionErr error
		wantStatus    string
	}{
		{name: "closed canceled", status: "canceled", closed: true, wantStatus: "canceled"},
		{name: "closed still running", status: "running", closed: true, wantStatus: "running"},
		{name: "open running", status: "running", wantStatus: "running"},
		{name: "unavailable", closed: true, inspectionErr: errors.New("Azure status lookup unavailable"), wantStatus: "uncertain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const runURL = "https://example.invalid/runs/888"
			var inspections, builds atomic.Int32
			process := &fakeProcess{
				definition: testProcessDefinition("example"),
				view:       &contract.RunView{Test: true, RunURL: runURL},
				inspect: func(ctx context.Context, _ *contract.StateSnapshot) (*contract.RunInspection, error) {
					inspections.Add(1)
					if _, ok := ctx.Deadline(); !ok {
						t.Error("read-only inspection has no deadline")
					}
					return &contract.RunInspection{RunURL: runURL, Status: test.status}, test.inspectionErr
				},
				build: func(context.Context, *fakeReleaseRun, contract.CheckpointFunc) ([]*coordinator.Step, error) {
					builds.Add(1)
					return nil, errors.New("dashboard must never build executable steps")
				},
			}
			running := testProcessRun(t)
			running.Started, running.Checkpointed = true, true
			store := newMemoryProcessRunStore()
			seedReleaseRecord(store, testReleaseRunRecord(42, running, test.closed, time.Now().UTC()))
			ui := newTestUIWithProcess(t, process, WithReleaseRunStore(store))
			response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var dashboard webview.DashboardResponse
			decodeResponse(t, response, &dashboard)
			var release webview.ReleaseSummary
			if test.closed {
				if len(dashboard.Recent) != 1 || len(dashboard.NeedsAttention) != 0 || len(dashboard.Ongoing) != 0 {
					t.Fatalf("closed dashboard = %#v", dashboard)
				}
				release = dashboard.Recent[0]
				if release.Issue == nil || release.Href != "" || !release.Closed {
					t.Fatalf("closed read-only card = %#v", release)
				}
			} else {
				if len(dashboard.Ongoing) != 1 || len(dashboard.NeedsAttention) != 0 {
					t.Fatalf("open dashboard = %#v", dashboard)
				}
				release = dashboard.Ongoing[0]
				if release.Issue != nil || release.Href == "" {
					t.Fatalf("open running card lost its existing selection action: %#v", release)
				}
			}
			if release.Status != test.wantStatus || release.SavedStatus != "running" || release.RunURL != runURL {
				t.Fatalf("status and link = %#v", release)
			}
			if test.inspectionErr == nil && release.ExternalStatus != test.status {
				t.Fatalf("verified status = %#v", release)
			}
			if test.inspectionErr != nil && (release.ExternalStatus != "" || !strings.Contains(release.Issue.Detail, test.inspectionErr.Error())) {
				t.Fatalf("inspection failure was hidden: %#v", release)
			}
			if inspections.Load() != 1 || builds.Load() != 0 {
				t.Fatalf("inspections = %d, executable graphs = %d", inspections.Load(), builds.Load())
			}
			stored, err := store.Get(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Revision != 1 || stored.Run.Complete || stored.Run.Result != "" || stored.Closed != test.closed {
				t.Fatalf("read-only lookup changed the record: %#v", stored)
			}
			if test.closed {
				selected := postJSON(t, ui, "/api/releases/42/select", `{}`)
				defer selected.Body.Close()
				if selected.StatusCode != http.StatusConflict || builds.Load() != 0 {
					t.Fatalf("closed unfinished record resumed: status=%d, builds=%d", selected.StatusCode, builds.Load())
				}
			}
		})
	}
}

func TestSelectReleaseRecordRestoresRun(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	run.Complete = true
	run.Result = string(runstore.StatusSucceeded)
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
	var plan webview.ProcessRunResponse[*contract.Plan]
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
	run.Started, run.Complete, run.Result = true, true, string(runstore.StatusSucceeded)
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
	run.Started, run.Complete, run.Result = true, true, string(runstore.StatusSucceeded)
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
	var plan webview.ProcessRunResponse[*contract.Plan]
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
	var exported webview.ReleaseRecordExport[*runstore.State]
	decodeResponse(t, response, &exported)
	if response.StatusCode != http.StatusOK || exported.ID != 42 || exported.Revision != 1 {
		t.Fatalf("status = %d, export = %#v", response.StatusCode, exported)
	}
	exported.Run.Complete = true
	exported.Run.Result = string(runstore.StatusUncertain)
	response = postJSONValue(t, ui, "/api/releases/42/import", exported)
	defer response.Body.Close()
	var updated webview.ReleaseRecordExport[*runstore.State]
	decodeResponse(t, response, &updated)
	if response.StatusCode != http.StatusOK || updated.Revision != 2 || updated.Run.Result != string(runstore.StatusUncertain) {
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
	store.records[42] = &runstore.Record{ID: 42, Revision: 1}
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
		change func(*webview.ReleaseRecordExport[*runstore.State])
	}{
		{name: "intent", change: func(exported *webview.ReleaseRecordExport[*runstore.State]) {
			exported.Run.Digest = strings.Repeat("b", 64)
		}},
		{name: "input", change: func(exported *webview.ReleaseRecordExport[*runstore.State]) {
			exported.Run.Snapshot.Input = json.RawMessage(`{"changed":true}`)
		}},
		{name: "status", change: func(exported *webview.ReleaseRecordExport[*runstore.State]) {
			exported.Run.Complete = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, err := store.Get(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			exported := testExportReleaseRunRecord(record)
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
	exported := testExportReleaseRunRecord(record)
	exported.Run.Snapshot.State = json.RawMessage(`{"value":"changed"}`)
	response := postJSONValue(t, ui, "/api/releases/42/import", exported)
	closeResponse(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func testExportReleaseRunRecord(record *runstore.Record) webview.ReleaseRecordExport[*runstore.State] {
	return webview.ReleaseRecordExport[*runstore.State]{
		ID: record.ID, Revision: record.Revision, URL: record.URL, Run: record.Run.Clone(),
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
	if store.nextID <= record.ID {
		store.nextID = record.ID + 1
	}
}
