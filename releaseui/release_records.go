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

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const (
	dashboardActiveLimit       = 50
	dashboardRecentLimit       = 10
	maxReleaseRecordImportSize = 1 << 20
)

type releaseRecordExport struct {
	ID       int              `json:"id"`
	Revision int              `json:"revision"`
	URL      string           `json:"url"`
	Run      *ReleaseRunState `json:"run"`
}

func (s *Server) releaseDashboard(ctx context.Context) dashboardResponse {
	result := dashboardResponse{
		Ongoing:        make([]releaseSummary, 0),
		NeedsAttention: make([]releaseSummary, 0),
		Recent:         make([]releaseSummary, 0),
		Processes:      s.processes.summaries(),
	}
	active, err := s.processRunStore.Query(ctx, false, dashboardActiveLimit)
	if err != nil {
		result.TrackingError = err.Error()
		return result
	}
	recent, err := s.processRunStore.Query(ctx, true, dashboardRecentLimit)
	if err != nil {
		result.TrackingError = err.Error()
	}
	for _, record := range append(active, recent...) {
		summary, err := s.releaseRunSummary(record)
		if err != nil {
			if result.TrackingError == "" {
				result.TrackingError = err.Error()
			}
			continue
		}
		if record.Closed {
			result.Recent = append(result.Recent, summary)
		} else {
			addDashboardRelease(&result, summary)
		}
	}
	return result
}

func (s *Server) releaseRunSummary(record *ReleaseRunRecord) (releaseSummary, error) {
	if err := validateReleaseRunRecord(record); err != nil {
		return releaseSummary{}, err
	}
	definition, ok := s.processes.process(record.Run.ProcessID)
	if !ok {
		return releaseSummary{}, fmt.Errorf("release record %d has unknown process %q", record.ID, record.Run.ProcessID)
	}
	run, err := loadReleaseRun(definition.process, record.Run.Snapshot)
	if err != nil {
		return releaseSummary{}, fmt.Errorf("validate release record %d: %w", record.ID, err)
	}
	status, err := record.Run.Status()
	if err != nil {
		return releaseSummary{}, err
	}
	summary := s.processRunSummaryLocked(record.Run, run.TakeView())
	summary.Status = string(status)
	summary.UpdatedAt = record.UpdatedAt
	summary.RecordID = record.ID
	summary.RecordURL = record.URL
	return summary, nil
}

func (s *Server) handleSelectRelease(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	if s.processRunStore == nil {
		writeError(response, http.StatusServiceUnavailable, "release discovery is unavailable")
		return
	}

	s.selectionMu.Lock()
	defer s.selectionMu.Unlock()
	s.mu.Lock()
	if href, selected := s.selectedReleaseHrefLocked(id); selected {
		s.mu.Unlock()
		writeJSON(response, http.StatusOK, map[string]string{"href": href})
		return
	}
	if s.hasSelectedOrPreparedReleaseLocked() {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "restart without a selected or prepared release before selecting a different record")
		return
	}
	s.mu.Unlock()
	record, err := s.processRunStore.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release record: %v", err))
		return
	}
	s.mu.Lock()
	if href, selected := s.selectedReleaseHrefLocked(id); selected {
		s.mu.Unlock()
		writeJSON(response, http.StatusOK, map[string]string{"href": href})
		return
	}
	if s.hasSelectedOrPreparedReleaseLocked() {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a release was prepared while the record was loading")
		return
	}
	href, err := s.restoreReleaseRunRecord(record)
	if err != nil {
		s.clearSelectedReleaseLocked()
		s.mu.Unlock()
		writeError(response, http.StatusConflict, fmt.Sprintf("restore release record: %v", err))
		return
	}
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, map[string]string{"href": href})
}

func (s *Server) restoreReleaseRunRecord(record *ReleaseRunRecord) (string, error) {
	if err := validateReleaseRunRecord(record); err != nil {
		return "", err
	}
	if s.processRunStore == nil {
		return "", errors.New("release selection requires a release store")
	}
	if err := s.restoreProcessRunRecord(record); err != nil {
		return "", err
	}
	return processPath(record.Run.ProcessID), nil
}

func (s *Server) selectedReleaseHrefLocked(id int) (string, bool) {
	if s.processRunRecord != nil && s.processRunRecord.ID == id {
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

func (s *Server) handleExportRelease(response http.ResponseWriter, request *http.Request) {
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	if s.processRunStore == nil {
		writeError(response, http.StatusServiceUnavailable, "release discovery is unavailable")
		return
	}
	record, err := s.processRunStore.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release record: %v", err))
		return
	}
	if err := validateReleaseRunRecord(record); err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release record: %v", err))
		return
	}
	writeJSON(response, http.StatusOK, exportReleaseRunRecord(record))
}

func (s *Server) handleImportRelease(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	if s.processRunStore == nil {
		writeError(response, http.StatusServiceUnavailable, "release discovery is unavailable")
		return
	}
	var imported releaseRecordExport
	if err := decodeJSONLimit(response, request, &imported, maxReleaseRecordImportSize); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if imported.ID != id || imported.Revision <= 0 || imported.Run == nil {
		writeError(response, http.StatusBadRequest, "imported release record identity is invalid")
		return
	}
	if err := imported.Run.Validate(); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.selectionMu.Lock()
	defer s.selectionMu.Unlock()
	s.mu.Lock()
	selected := s.processRunRecord != nil && s.processRunRecord.ID == id
	running := s.processRunning
	s.mu.Unlock()
	if selected || running {
		writeError(response, http.StatusConflict, "restart without selecting this release before importing repaired state")
		return
	}

	current, err := s.processRunStore.Get(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release record: %v", err))
		return
	}
	if err := validateReleaseRunRecord(current); err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("load release record: %v", err))
		return
	}
	if current.Revision != imported.Revision {
		writeError(response, http.StatusConflict, "release record changed; export it again before importing")
		return
	}
	if current.Run.ProcessID != imported.Run.ProcessID || current.Run.Digest != imported.Run.Digest {
		writeError(response, http.StatusBadRequest, "import cannot change the release process or immutable intent")
		return
	}
	run, err := s.validateImportedRelease(current.Run, imported.Run)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.processRunStore.Update(request.Context(), current, imported.Run, run.TakeView(), run.Plan())
	if errors.Is(err, ErrReleaseRunConflict) {
		writeError(response, http.StatusConflict, "release record changed; export it again before importing")
		return
	}
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("update release record: %v", err))
		return
	}
	writeJSON(response, http.StatusOK, exportReleaseRunRecord(updated))
}

func (s *Server) validateImportedRelease(current, candidate *ReleaseRunState) (contract.Run, error) {
	if err := current.Validate(); err != nil {
		return nil, err
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if !bytes.Equal(current.Snapshot.Input, candidate.Snapshot.Input) {
		return nil, errors.New("import cannot change process input")
	}
	registered, ok := s.processes.process(candidate.ProcessID)
	if !ok {
		return nil, fmt.Errorf("release process %q is not configured", candidate.ProcessID)
	}
	currentRun, err := loadReleaseRun(registered.process, current.Snapshot)
	if err != nil {
		return nil, err
	}
	run, err := loadReleaseRun(registered.process, candidate.Snapshot)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentRun.Plan(), run.Plan()) {
		return nil, errors.New("import cannot change the reviewed plan")
	}
	return run, nil
}

func exportReleaseRunRecord(record *ReleaseRunRecord) releaseRecordExport {
	if record == nil {
		return releaseRecordExport{}
	}
	return releaseRecordExport{
		ID: record.ID, Revision: record.Revision, URL: record.URL, Run: record.Run.Clone(),
	}
}

func releaseRecordID(response http.ResponseWriter, request *http.Request) (int, bool) {
	id, err := strconv.Atoi(request.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(response, http.StatusBadRequest, "release record ID must be a positive integer")
		return 0, false
	}
	return id, true
}
