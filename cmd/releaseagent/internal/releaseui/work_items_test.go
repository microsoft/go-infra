package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
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
	context.Context,
	string,
	string,
	*azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	return nil, errors.New("unexpected Create call")
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
	item.State = workItemStateForStatus(snapshot.Status)
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

func TestDashboardQueriesReleaseWorkItems(t *testing.T) {
	goImagesDocument := testGoImagesDocument(t)
	goImagesState := goImagesDocument.State
	goImagesState.Complete = true
	goImagesState.Result = "succeeded"
	var err error
	goImagesDocument, err = goImagesDocument.WithState(&goImagesState, goImagesDocument.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	goImagesSnapshot, err := goImagesSnapshot(goImagesDocument)
	if err != nil {
		t.Fatal(err)
	}
	processRun := testStoredGoInfraRun(
		t,
		goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun},
		nil,
	)
	processRun.Started = true
	processSnapshot, err := processRunSnapshot(processRun)
	if err != nil {
		t.Fatal(err)
	}
	service := newMemoryReleaseWorkItemService(
		testReleaseWorkItem(2, processSnapshot, time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)),
		testReleaseWorkItem(1, goImagesSnapshot, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)),
	)
	ui := newTestUI(t, WithReleaseWorkItems(service))
	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard dashboardResponse
	decodeResponse(t, response, &dashboard)
	if response.StatusCode != http.StatusOK || dashboard.TrackingError != "" ||
		len(dashboard.Ongoing) != 1 || len(dashboard.Recent) != 1 {

		t.Fatalf("dashboard = %#v", dashboard)
	}
	if dashboard.Ongoing[0].WorkItemID != 2 || dashboard.Ongoing[0].Status != "starting" ||
		dashboard.Recent[0].WorkItemID != 1 || dashboard.Recent[0].Status != "succeeded" {

		t.Fatalf("ongoing = %#v, recent = %#v", dashboard.Ongoing, dashboard.Recent)
	}
}

func TestSelectReleaseWorkItemRestoresProcess(t *testing.T) {
	document := testGoImagesDocument(t)
	snapshot, err := goImagesSnapshot(document)
	if err != nil {
		t.Fatal(err)
	}
	service := newMemoryReleaseWorkItemService(testReleaseWorkItem(42, snapshot, time.Now().UTC()))
	store, err := NewGoImagesWorkItemStore(service, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	ui := newTestUI(t, WithReleaseWorkItems(service), WithSessionStore(store))
	response := postJSON(t, ui, "/api/release-work-items/42/select", `{}`)
	var selected map[string]string
	decodeResponse(t, response, &selected)
	if response.StatusCode != http.StatusOK || selected["href"] != "/go-images" {
		t.Fatalf("status = %d, selected = %#v", response.StatusCode, selected)
	}
	response, err = ui.client.Get(ui.http.URL + testGoImagesAPI + "/plan")
	if err != nil {
		t.Fatal(err)
	}
	var plan planResponse
	decodeResponse(t, response, &plan)
	if response.StatusCode != http.StatusOK || plan.SessionID != document.ID || plan.Execution.WorkItem == nil ||
		plan.Execution.WorkItem.ID != 42 || plan.Execution.WorkItem.URL != "https://example.invalid/workitems/42" {

		t.Fatalf("status = %d, plan = %#v", response.StatusCode, plan)
	}
}

func TestExportImportReleaseWorkItem(t *testing.T) {
	document := testGoImagesDocument(t)
	snapshot, err := goImagesSnapshot(document)
	if err != nil {
		t.Fatal(err)
	}
	service := newMemoryReleaseWorkItemService(testReleaseWorkItem(42, snapshot, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseWorkItems(service))

	response, err := ui.client.Get(ui.http.URL + "/api/release-work-items/42/export")
	if err != nil {
		t.Fatal(err)
	}
	var exported workItemExport
	decodeResponse(t, response, &exported)
	if response.StatusCode != http.StatusOK || exported.ID != 42 || exported.Revision != 1 {
		t.Fatalf("status = %d, export = %#v", response.StatusCode, exported)
	}

	var repairedDocument goimagessession.Document
	if err := json.Unmarshal(exported.Snapshot.Payload, &repairedDocument); err != nil {
		t.Fatal(err)
	}
	repairedState := repairedDocument.State
	repairedState.QueueAttempted = true
	repaired, err := repairedDocument.WithState(&repairedState, repairedDocument.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	exported.Snapshot.Payload, err = json.Marshal(repaired)
	if err != nil {
		t.Fatal(err)
	}
	response = postJSONValue(t, ui, "/api/release-work-items/42/import", exported)
	var updated workItemExport
	decodeResponse(t, response, &updated)
	if response.StatusCode != http.StatusOK || updated.Revision != 2 {
		t.Fatalf("status = %d, update = %#v", response.StatusCode, updated)
	}

	response = postJSONValue(t, ui, "/api/release-work-items/42/import", exported)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("stale import status = %d, want %d", response.StatusCode, http.StatusConflict)
	}
}

func TestImportRejectsChangedIntentAndInconsistentStatus(t *testing.T) {
	document := testGoImagesDocument(t)
	snapshot, err := goImagesSnapshot(document)
	if err != nil {
		t.Fatal(err)
	}
	service := newMemoryReleaseWorkItemService(testReleaseWorkItem(42, snapshot, time.Now().UTC()))
	ui := newTestUI(t, WithReleaseWorkItems(service))

	for _, test := range []struct {
		name   string
		change func(*workItemExport)
	}{
		{name: "intent", change: func(exported *workItemExport) {
			exported.Snapshot.IntentDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func postJSONValue(t *testing.T, ui *testUI, path string, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return postJSON(t, ui, path, string(data))
}

func testReleaseWorkItem(id int, snapshot *azdoworkitem.Snapshot, changedAt time.Time) *azdoworkitem.WorkItem {
	return &azdoworkitem.WorkItem{
		ID: id, Revision: 1, URL: fmt.Sprintf("https://example.invalid/workitems/%d", id),
		Title: fmt.Sprintf("Release %d", id), State: workItemStateForStatus(snapshot.Status),
		ChangedAt: changedAt, Snapshot: cloneReleaseSnapshot(snapshot),
	}
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
