// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const (
	dashboardActiveLimit = 50
	dashboardRecentLimit = 10
)

type releaseWorkItemService interface {
	releaseWorkItemClient
	Get(context.Context, int) (*azdoworkitem.WorkItem, error)
	Query(context.Context, bool, int) ([]*azdoworkitem.WorkItem, error)
}

// WithReleaseWorkItems enables shared discovery, selection, and JSON repair routes.
func WithReleaseWorkItems(workItems releaseWorkItemService) Option {
	return func(server *Server) {
		server.workItems = workItems
	}
}

// WithReleaseWorkItem selects an already loaded work item to restore when the server starts.
func WithReleaseWorkItem(workItem *azdoworkitem.WorkItem) Option {
	return func(server *Server) {
		server.initialWorkItem = workItem
	}
}

type workItemExport struct {
	ID       int                    `json:"id"`
	Revision int                    `json:"revision"`
	URL      string                 `json:"url"`
	Snapshot *azdoworkitem.Snapshot `json:"snapshot"`
}

func (s *Server) workItemDashboard(ctx context.Context) dashboardResponse {
	result := dashboardResponse{
		Ongoing:        make([]releaseSummary, 0),
		NeedsAttention: make([]releaseSummary, 0),
		Recent:         make([]releaseSummary, 0),
		Processes:      s.processes.summaries(),
	}
	active, err := s.workItems.Query(ctx, false, dashboardActiveLimit)
	if err != nil {
		result.TrackingError = err.Error()
		return result
	}
	recent, err := s.workItems.Query(ctx, true, dashboardRecentLimit)
	if err != nil {
		result.TrackingError = err.Error()
	}
	items := append(active, recent...)
	for _, item := range items {
		summary, err := s.workItemSummary(item)
		if err != nil {
			if result.TrackingError == "" {
				result.TrackingError = err.Error()
			}
			continue
		}
		if item.State == "Closed" {
			result.Recent = append(result.Recent, summary)
		} else {
			addDashboardRelease(&result, summary)
		}
	}
	return result
}

func (s *Server) workItemSummary(item *azdoworkitem.WorkItem) (releaseSummary, error) {
	if item == nil || item.Snapshot == nil {
		return releaseSummary{}, errors.New("release work item is empty")
	}
	record, err := processRunRecord(item)
	if err != nil {
		return releaseSummary{}, err
	}
	definition, ok := s.processes.process(record.Run.ProcessID)
	if !ok {
		return releaseSummary{}, fmt.Errorf("release work item %d has unknown process %q", item.ID, record.Run.ProcessID)
	}
	run, err := loadReleaseRun(definition.process, record.Run.Snapshot)
	if err != nil {
		return releaseSummary{}, fmt.Errorf("validate release work item %d: %w", item.ID, err)
	}
	summary := s.processRunSummaryLocked(record.Run, run.TakeView())
	summary.Status = string(item.Snapshot.Status)
	summary.UpdatedAt = item.ChangedAt
	summary.WorkItemID = item.ID
	summary.WorkItemURL = item.URL
	return summary, nil
}

func (s *Server) handleSelectWorkItem(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := workItemID(response, request)
	if !ok {
		return
	}
	if s.workItems == nil {
		writeError(response, http.StatusServiceUnavailable, "release work item discovery is unavailable")
		return
	}

	s.selectionMu.Lock()
	defer s.selectionMu.Unlock()
	s.mu.Lock()
	if href, selected := s.selectedWorkItemHrefLocked(id); selected {
		s.mu.Unlock()
		writeJSON(response, http.StatusOK, map[string]string{"href": href})
		return
	}
	if s.hasSelectedOrPreparedReleaseLocked() {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "restart without a selected or prepared release before selecting a different work item")
		return
	}
	s.mu.Unlock()
	item, err := s.workItems.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release work item: %v", err))
		return
	}
	if item == nil || item.Snapshot == nil {
		writeError(response, http.StatusBadGateway, "release work item is empty")
		return
	}
	s.mu.Lock()
	if href, selected := s.selectedWorkItemHrefLocked(id); selected {
		s.mu.Unlock()
		writeJSON(response, http.StatusOK, map[string]string{"href": href})
		return
	}
	if s.hasSelectedOrPreparedReleaseLocked() {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a release was prepared while the work item was loading")
		return
	}
	href, err := s.restoreReleaseWorkItem(item)
	if err != nil {
		s.clearSelectedReleaseLocked()
		s.mu.Unlock()
		writeError(response, http.StatusConflict, fmt.Sprintf("restore release work item: %v", err))
		return
	}
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, map[string]string{"href": href})
}

func (s *Server) restoreReleaseWorkItem(item *azdoworkitem.WorkItem) (string, error) {
	if item == nil || item.Snapshot == nil {
		return "", errors.New("release work item is empty")
	}
	if s.processRunStore == nil {
		return "", errors.New("release work item selection requires a durable run store")
	}
	record, err := processRunRecord(item)
	if err != nil {
		return "", err
	}
	if err := s.restoreProcessRunRecord(record); err != nil {
		return "", err
	}
	return processPath(record.Run.ProcessID), nil
}

func (s *Server) selectedWorkItemHrefLocked(id int) (string, bool) {
	if s.processRunRecord != nil && s.processRunRecord.WorkItemID == id {
		return processPath(s.processRunRecord.Run.ProcessID), true
	}
	return "", false
}

func (s *Server) hasSelectedOrPreparedReleaseLocked() bool {
	return s.processRunning || len(s.steps) != 0 || s.processRun != nil
}

func (s *Server) clearSelectedReleaseLocked() {
	s.activeProcessID = ""
	s.steps = nil
	s.runner = &coordinator.StepRunner{}
	s.processRun = nil
	s.processRunState = nil
	s.processPlan = nil
	s.processRunRecord = nil
}

func (s *Server) handleExportWorkItem(response http.ResponseWriter, request *http.Request) {
	id, ok := workItemID(response, request)
	if !ok {
		return
	}
	if s.workItems == nil {
		writeError(response, http.StatusServiceUnavailable, "release work item discovery is unavailable")
		return
	}
	item, err := s.workItems.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release work item: %v", err))
		return
	}
	writeJSON(response, http.StatusOK, exportWorkItem(item))
}

func (s *Server) handleImportWorkItem(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := workItemID(response, request)
	if !ok {
		return
	}
	if s.workItems == nil {
		writeError(response, http.StatusServiceUnavailable, "release work item discovery is unavailable")
		return
	}
	var imported workItemExport
	if err := decodeJSONLimit(response, request, &imported, 2*azdoworkitem.MaxSnapshotSize); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if imported.ID != id || imported.Revision <= 0 || imported.Snapshot == nil {
		writeError(response, http.StatusBadRequest, "imported work item identity is invalid")
		return
	}
	if err := imported.Snapshot.Validate(); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.selectionMu.Lock()
	defer s.selectionMu.Unlock()
	s.mu.Lock()
	selected := s.processRunRecord != nil && s.processRunRecord.WorkItemID == id
	running := s.processRunning
	s.mu.Unlock()
	if selected || running {
		writeError(response, http.StatusConflict, "restart without selecting this work item before importing repaired state")
		return
	}

	current, err := s.workItems.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release work item: %v", err))
		return
	}
	if current.Revision != imported.Revision {
		writeError(response, http.StatusConflict, "release work item changed; export it again before importing")
		return
	}
	if current.Snapshot.ProcessID != imported.Snapshot.ProcessID ||
		current.Snapshot.IntentDigest != imported.Snapshot.IntentDigest {

		writeError(response, http.StatusBadRequest, "import cannot change the release process or immutable intent")
		return
	}
	if err := s.validateImportedSnapshot(current, imported.Snapshot); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.workItems.Update(request.Context(), current, imported.Snapshot)
	if errors.Is(err, azdoworkitem.ErrRevisionConflict) {
		writeError(response, http.StatusConflict, "release work item changed; export it again before importing")
		return
	}
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("update release work item: %v", err))
		return
	}
	writeJSON(response, http.StatusOK, exportWorkItem(updated))
}

func (s *Server) validateImportedSnapshot(current *azdoworkitem.WorkItem, snapshot *azdoworkitem.Snapshot) error {
	currentRecord, err := processRunRecord(current)
	if err != nil {
		return err
	}
	candidate := *current
	candidate.Snapshot = snapshot
	record, err := processRunRecord(&candidate)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentRecord.Run.Snapshot.Input, record.Run.Snapshot.Input) {
		return errors.New("import cannot change process input")
	}
	registered, ok := s.processes.process(record.Run.ProcessID)
	if !ok {
		return fmt.Errorf("release process %q is not configured", record.Run.ProcessID)
	}
	currentRun, err := loadReleaseRun(registered.process, currentRecord.Run.Snapshot)
	if err != nil {
		return err
	}
	run, err := loadReleaseRun(registered.process, record.Run.Snapshot)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(currentRun.Plan(), run.Plan()) {
		return errors.New("import cannot change the reviewed plan")
	}
	snapshot.Description = processRunDescription(record.Run, run.TakeView(), run.Plan())
	return nil
}

func exportWorkItem(item *azdoworkitem.WorkItem) workItemExport {
	return workItemExport{ID: item.ID, Revision: item.Revision, URL: item.URL, Snapshot: item.Snapshot}
}

func workItemID(response http.ResponseWriter, request *http.Request) (int, bool) {
	id, err := strconv.Atoi(request.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(response, http.StatusBadRequest, "release work item ID must be a positive integer")
		return 0, false
	}
	return id, true
}
