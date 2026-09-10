// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

const (
	dashboardActiveLimit = 50
	dashboardRecentLimit = 10
)

type releaseWorkItemService interface {
	releaseWorkItemClient
	Query(context.Context, bool, int) ([]*azdoworkitem.WorkItem, error)
}

// WithReleaseWorkItems enables shared discovery, selection, and JSON repair routes.
func WithReleaseWorkItems(workItems releaseWorkItemService) Option {
	return func(server *Server) {
		server.workItems = workItems
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
		addDashboardRelease(&result, summary)
	}
	return result
}

func (s *Server) workItemSummary(item *azdoworkitem.WorkItem) (releaseSummary, error) {
	if item == nil || item.Snapshot == nil {
		return releaseSummary{}, errors.New("release work item is empty")
	}
	summary := releaseSummary{
		Status:      string(item.Snapshot.Status),
		UpdatedAt:   item.ChangedAt,
		WorkItemID:  item.ID,
		WorkItemURL: item.URL,
	}
	if item.Snapshot.ProcessID == goImagesProcessID {
		record, err := goImagesSessionRecord(item)
		if err != nil {
			return releaseSummary{}, err
		}
		summary.Mark = "GI"
		summary.Name = "Go images"
		summary.Mode = string(record.Document.Input.Mode)
		summary.RunID = record.Document.State.BuildID
		summary.RunLabel = "Azure build"
		summary.Href = processPath(goImagesProcessID)
		return summary, nil
	}
	record, err := processRunRecord(item)
	if err != nil {
		return releaseSummary{}, err
	}
	definition, ok := s.processes.process(record.Run.ProcessID)
	if !ok {
		return releaseSummary{}, fmt.Errorf("release work item %d has unknown process %q", item.ID, record.Run.ProcessID)
	}
	summary.Mark = definition.Mark
	summary.Name = definition.Name
	summary.Href = processPath(record.Run.ProcessID)
	summary.RunLabel = "Target"
	summary.RunID = record.Run.Target.ID
	if record.Run.External != nil {
		summary.RunID = record.Run.External.ID
	}
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
	var href string
	if item.Snapshot.ProcessID == goImagesProcessID {
		s.goImagesWorkItemID = id
		var record *GoImagesSessionRecord
		record, err = goImagesSessionRecord(item)
		if err == nil {
			err = s.restoreGoImagesSession(record)
		}
		if err == nil {
			err = s.resumeRestoredMonitoring()
		}
		href = processPath(goImagesProcessID)
	} else {
		s.processRunItemID = id
		var record *ProcessRunRecord
		record, err = processRunRecord(item)
		if err == nil {
			err = s.restoreProcessRunRecord(record)
		}
		href = processPath(item.Snapshot.ProcessID)
	}
	if err != nil {
		s.clearSelectedReleaseLocked()
		s.mu.Unlock()
		writeError(response, http.StatusConflict, fmt.Sprintf("restore release work item: %v", err))
		return
	}
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, map[string]string{"href": href})
}

func (s *Server) selectedWorkItemHrefLocked(id int) (string, bool) {
	if s.goImages.record != nil && s.goImages.record.WorkItemID == id {
		return processPath(goImagesProcessID), true
	}
	if s.processRunRecord != nil && s.processRunRecord.WorkItemID == id {
		return processPath(s.processRunRecord.Run.ProcessID), true
	}
	return "", false
}

func (s *Server) hasSelectedOrPreparedReleaseLocked() bool {
	return s.simulationRunning || s.releaseRunning || s.processRunning || len(s.steps) != 0 ||
		s.goImages.document != nil || s.processRun != nil
}

func (s *Server) clearSelectedReleaseLocked() {
	s.activeProcessID = ""
	s.steps = nil
	s.runner = &coordinator.StepRunner{}
	s.goImages = goImagesRuntime{}
	s.goImagesWorkItemID = 0
	s.processRun = nil
	s.processRunRecord = nil
	s.processRunItemID = 0
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
	selected := s.goImages.record != nil && s.goImages.record.WorkItemID == id ||
		s.processRunRecord != nil && s.processRunRecord.WorkItemID == id
	running := s.simulationRunning || s.releaseRunning || s.processRunning
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
	if err := validateImportedSnapshot(current, imported.Snapshot); err != nil {
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

func validateImportedSnapshot(current *azdoworkitem.WorkItem, snapshot *azdoworkitem.Snapshot) error {
	candidate := *current
	candidate.State = workItemStateForStatus(snapshot.Status)
	candidate.Snapshot = snapshot
	if snapshot.ProcessID == goImagesProcessID {
		record, err := goImagesSessionRecord(&candidate)
		if err == nil {
			snapshot.Description = goImagesDescription(record.Document)
		}
		return err
	}
	record, err := processRunRecord(&candidate)
	if err == nil {
		snapshot.Description = processRunDescription(record.Run)
	}
	return err
}

func workItemStateForStatus(status azdoworkitem.Status) string {
	if status == azdoworkitem.StatusSucceeded {
		return "Closed"
	}
	return "Active"
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
