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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

type memoryReleaseWorkItemService struct {
	mu    sync.Mutex
	items map[int]*azdoworkitem.WorkItem
	order []int
}

func newMemoryReleaseWorkItemService(items ...*azdoworkitem.WorkItem) *memoryReleaseWorkItemService {
	service := &memoryReleaseWorkItemService{items: make(map[int]*azdoworkitem.WorkItem)}
	for _, item := range items {
		service.items[item.ID] = cloneReleaseWorkItem(item)
		service.order = append(service.order, item.ID)
	}
	return service
}

func (s *memoryReleaseWorkItemService) Create(
	_ context.Context,
	_, _ string,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := len(s.items) + 1
	item := &azdoworkitem.WorkItem{
		ID: id, Revision: 1, URL: fmt.Sprintf("https://example.invalid/workitems/%d", id),
		State: "Active", ChangedAt: time.Now().UTC(), Snapshot: cloneReleaseSnapshot(snapshot),
	}
	s.items[id] = item
	s.order = append(s.order, id)
	return cloneReleaseWorkItem(item), nil
}

func (s *memoryReleaseWorkItemService) Get(_ context.Context, id int) (*azdoworkitem.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return nil, fmt.Errorf("work item %d not found", id)
	}
	return cloneReleaseWorkItem(item), nil
}

func (s *memoryReleaseWorkItemService) Update(
	_ context.Context,
	current *azdoworkitem.WorkItem,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[current.ID]
	if !ok || item.Revision != current.Revision {
		return nil, azdoworkitem.ErrRevisionConflict
	}
	item.Revision++
	item.ChangedAt = item.ChangedAt.Add(time.Minute)
	item.State = testWorkItemState(snapshot.Status)
	item.Snapshot = cloneReleaseSnapshot(snapshot)
	return cloneReleaseWorkItem(item), nil
}

func (s *memoryReleaseWorkItemService) Query(_ context.Context, closed bool, limit int) ([]*azdoworkitem.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*azdoworkitem.WorkItem, 0, len(s.order))
	for _, id := range s.order {
		item := s.items[id]
		if (item.State == "Closed") != closed {
			continue
		}
		items = append(items, cloneReleaseWorkItem(item))
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

type testUI struct {
	server *Server
	http   *httptest.Server
	client *http.Client
}

func newTestUI(t *testing.T, options ...Option) *testUI {
	t.Helper()
	process := &fakeProcess{
		definition: exampleProcessDefinition(),
		build: func(_ context.Context, run *ReleaseRunState, _ CheckpointFunc) ([]*coordinator.Step, error) {
			return exampleProcessSteps(run, func(context.Context) error { return nil }), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	options = append([]Option{WithProcesses(process), WithDemoDelay(0)}, options...)
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

func TestDashboardQueriesReleaseWorkItems(t *testing.T) {
	starting := testProcessRun(t)
	starting.Started = true
	canceled := testProcessRun(t)
	canceled.Started = true
	canceled.Complete = true
	canceled.Result = "canceled"
	succeeded := testProcessRun(t)
	succeeded.Started = true
	succeeded.Complete = true
	succeeded.Result = "succeeded"

	service := newMemoryReleaseWorkItemService(
		testReleaseWorkItem(t, 3, canceled, time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC)),
		testReleaseWorkItem(t, 2, starting, time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)),
		testReleaseWorkItem(t, 1, succeeded, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)),
	)
	service.items[3].State = "Closed"
	ui := newTestUI(t, WithReleaseWorkItems(service))
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
	if dashboard.Ongoing[0].WorkItemID != 2 || dashboard.Ongoing[0].Status != "starting" ||
		dashboard.Recent[0].WorkItemID != 3 || dashboard.Recent[0].Status != "canceled" ||
		dashboard.Recent[1].WorkItemID != 1 || dashboard.Recent[1].Status != "succeeded" {

		t.Fatalf("dashboard groups = %#v, %#v, %#v", dashboard.Ongoing, dashboard.NeedsAttention, dashboard.Recent)
	}
}

func TestSelectReleaseWorkItemRestoresRun(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	run.Complete = true
	run.Result = "succeeded"
	service := newMemoryReleaseWorkItemService(
		testReleaseWorkItem(t, 42, run, time.Now().UTC()),
		testReleaseWorkItem(t, 43, run, time.Now().UTC()),
	)
	store, err := NewReleaseRunWorkItemStore(service, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	ui := newTestUI(t, WithReleaseWorkItems(service), WithReleaseRunStore(store))
	response := postJSON(t, ui, "/api/release-work-items/42/select", `{}`)
	defer response.Body.Close()
	var selected map[string]string
	decodeResponse(t, response, &selected)
	if response.StatusCode != http.StatusOK || selected["href"] != "/example" {
		t.Fatalf("status = %d, selected = %#v", response.StatusCode, selected)
	}
	response, err = ui.client.Get(ui.http.URL + "/api/processes/example/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var plan processRunResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK || plan.Execution.WorkItem == nil || plan.Execution.WorkItem.ID != 42 {
		t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
	}
	response = postJSON(t, ui, "/api/release-work-items/42/select", `{}`)
	defer response.Body.Close()
	decodeResponse(t, response, &selected)
	if response.StatusCode != http.StatusOK || selected["href"] != "/example" {
		t.Fatalf("reopen status = %d, selected = %#v", response.StatusCode, selected)
	}
	response = postJSON(t, ui, "/api/release-work-items/43/select", `{}`)
	closeResponse(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("different work item status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
}

func TestExportImportReleaseWorkItem(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	service := newMemoryReleaseWorkItemService(testReleaseWorkItem(t, 42, run, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseWorkItems(service))

	response, err := ui.client.Get(ui.http.URL + "/api/release-work-items/42/export")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var exported workItemExport
	decodeResponse(t, response, &exported)
	if response.StatusCode != http.StatusOK || exported.ID != 42 || exported.Revision != 1 {
		t.Fatalf("status = %d, export = %#v", response.StatusCode, exported)
	}
	var repaired ReleaseRunState
	if err := json.Unmarshal(exported.Snapshot.Payload, &repaired); err != nil {
		t.Fatal(err)
	}
	repaired.Complete = true
	repaired.Result = "uncertain"
	exported.Snapshot.Status = azdoworkitem.StatusUncertain
	exported.Snapshot.Payload, err = json.Marshal(&repaired)
	if err != nil {
		t.Fatal(err)
	}
	response = postJSONValue(t, ui, "/api/release-work-items/42/import", exported)
	defer response.Body.Close()
	var updated workItemExport
	decodeResponse(t, response, &updated)
	if response.StatusCode != http.StatusOK || updated.Revision != 2 {
		t.Fatalf("status = %d, update = %#v", response.StatusCode, updated)
	}
	item, err := service.Get(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	description, err := azdoworkitem.RenderDescription(item.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(description, "<strong>Actions</strong>") {
		t.Fatalf("description = %s", description)
	}
	response = postJSONValue(t, ui, "/api/release-work-items/42/import", exported)
	closeResponse(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale import status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
}

func TestImportRejectsChangedIntentAndInconsistentStatus(t *testing.T) {
	run := testProcessRun(t)
	run.Started = true
	service := newMemoryReleaseWorkItemService(testReleaseWorkItem(t, 42, run, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseWorkItems(service))
	for _, test := range []struct {
		name   string
		change func(*workItemExport)
	}{
		{name: "intent", change: func(exported *workItemExport) {
			exported.Snapshot.IntentDigest = strings.Repeat("b", 64)
		}},
		{name: "status", change: func(exported *workItemExport) {
			exported.Snapshot.Status = azdoworkitem.StatusSucceeded
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item, err := service.Get(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			exported := exportWorkItem(item)
			test.change(&exported)
			response := postJSONValue(t, ui, "/api/release-work-items/42/import", exported)
			closeResponse(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
		})
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

func testReleaseWorkItem(t *testing.T, id int, run *ReleaseRunState, changedAt time.Time) *azdoworkitem.WorkItem {
	t.Helper()
	snapshot, err := processRunSnapshot(run)
	if err != nil {
		t.Fatal(err)
	}
	return &azdoworkitem.WorkItem{
		ID: id, Revision: 1, URL: fmt.Sprintf("https://example.invalid/workitems/%d", id),
		State: testWorkItemState(snapshot.Status), ChangedAt: changedAt, Snapshot: cloneReleaseSnapshot(snapshot),
	}
}

func testWorkItemState(status azdoworkitem.Status) string {
	if status == azdoworkitem.StatusSucceeded {
		return "Closed"
	}
	return "Active"
}

func cloneReleaseWorkItem(item *azdoworkitem.WorkItem) *azdoworkitem.WorkItem {
	clone := *item
	clone.Snapshot = cloneReleaseSnapshot(item.Snapshot)
	return &clone
}

func cloneReleaseSnapshot(snapshot *azdoworkitem.Snapshot) *azdoworkitem.Snapshot {
	clone := *snapshot
	clone.Payload = slices.Clone(snapshot.Payload)
	return &clone
}

var _ releaseWorkItemService = (*memoryReleaseWorkItemService)(nil)
