// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

const (
	dashboardActiveLimit = 50
	dashboardRecentLimit = 10
)

// History provides access to durable release records managed by a Manager.
type History struct {
	manager *Manager
}

// Dashboard returns the configured processes and tracked releases shown on the dashboard.
func (h *History) Dashboard(ctx context.Context) webview.DashboardResponse {
	result := webview.DashboardResponse{
		Ongoing:        make([]webview.ReleaseSummary, 0),
		NeedsAttention: make([]webview.ReleaseSummary, 0),
		Recent:         make([]webview.ReleaseSummary, 0),
		Processes:      h.processSummaries(),
	}
	active, err := h.manager.store.Query(ctx, false, dashboardActiveLimit)
	if err != nil {
		result.TrackingError = err.Error()
		return result
	}
	recent, err := h.manager.store.Query(ctx, true, dashboardRecentLimit)
	if err != nil {
		result.TrackingError = err.Error()
	}
	for _, record := range append(active, recent...) {
		summary, err := h.releaseSummary(record)
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

// Select restores a durable release record into its process session.
func (h *History) Select(ctx context.Context, id int) (string, error) {
	record, err := h.manager.store.Get(ctx, id)
	if err != nil {
		return "", errorf(ErrorStore, "load release record: %v", err)
	}
	if record.Run == nil {
		return "", newError(ErrorConflict, errors.New("restore release record: release run record is empty"))
	}
	session, ok := h.manager.processSession(record.Run.ProcessID)
	if !ok {
		return "", errorf(
			ErrorConflict,
			"restore release record: stored process %q is not configured",
			record.Run.ProcessID,
		)
	}
	session.selectionMu.Lock()
	defer session.selectionMu.Unlock()
	href, selected, err := releaseSelection(session, id)
	if err != nil {
		return "", newError(ErrorConflict, err)
	}
	if selected {
		return href, nil
	}
	href, err = h.restore(record)
	if err != nil {
		return "", errorf(ErrorConflict, "restore release record: %v", err)
	}
	return href, nil
}

// Export returns the portable representation of a durable release record.
func (h *History) Export(
	ctx context.Context,
	id int,
) (webview.ReleaseRecordExport[*runstore.State], error) {
	record, err := h.manager.store.Get(ctx, id)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorStore, "load release record: %v", err)
	}
	if err := runstore.ValidateRecord(record); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorStore, "load release record: %v", err)
	}
	return exportRecord(record), nil
}

// Import validates and saves a repaired durable release record.
func (h *History) Import(
	ctx context.Context,
	id int,
	imported webview.ReleaseRecordExport[*runstore.State],
) (webview.ReleaseRecordExport[*runstore.State], error) {
	if imported.ID != id || imported.Revision <= 0 || imported.Run == nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorInvalid, errors.New("imported release record identity is invalid"))
	}
	if err := imported.Run.Validate(); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}

	current, err := h.manager.store.Get(ctx, id)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorStore, "load release record: %v", err)
	}
	if err := runstore.ValidateRecord(current); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorStore, "load release record: %v", err)
	}
	if current.Revision != imported.Revision {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorConflict, errors.New("release record changed; export it again before importing"))
	}
	if current.Run.ProcessID != imported.Run.ProcessID || current.Run.Digest != imported.Run.Digest {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorInvalid, errors.New("import cannot change the release process or immutable intent"))
	}
	session, ok := h.manager.processSession(current.Run.ProcessID)
	if !ok {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorInvalid, "release process %q is not configured", current.Run.ProcessID)
	}
	session.selectionMu.Lock()
	defer session.selectionMu.Unlock()
	session.mu.Lock()
	selected := session.record != nil && session.record.ID == id
	running := session.running
	session.mu.Unlock()
	if selected || running {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(
				ErrorConflict,
				errors.New("restart without selecting this release before importing repaired state"),
			)
	}
	run, err := h.validateImport(current.Run, imported.Run)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}
	updated, err := h.manager.store.Update(ctx, current, imported.Run, run.TakeView(), run.Plan())
	if errors.Is(err, runstore.ErrConflict) {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorConflict, errors.New("release record changed; export it again before importing"))
	}
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{},
			errorf(ErrorStore, "update release record: %v", err)
	}
	return exportRecord(updated), nil
}

func (h *History) restore(record *runstore.Record) (string, error) {
	if err := runstore.ValidateRecord(record); err != nil {
		return "", err
	}
	if err := h.manager.restoreProcessRunRecord(record); err != nil {
		return "", err
	}
	return processregistry.Path(record.Run.ProcessID), nil
}

func (h *History) validateImport(current, candidate *runstore.State) (contract.Run, error) {
	if err := current.Validate(); err != nil {
		return nil, err
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if !bytes.Equal(current.Snapshot.Input, candidate.Snapshot.Input) {
		return nil, errors.New("import cannot change process input")
	}
	registered, ok := h.manager.processes.Lookup(candidate.ProcessID)
	if !ok {
		return nil, fmt.Errorf("release process %q is not configured", candidate.ProcessID)
	}
	currentRun, err := loadReleaseRun(registered.Implementation, current.Snapshot)
	if err != nil {
		return nil, err
	}
	run, err := loadReleaseRun(registered.Implementation, candidate.Snapshot)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentRun.Plan(), run.Plan()) {
		return nil, errors.New("import cannot change the reviewed plan")
	}
	return run, nil
}

func (h *History) releaseSummary(record *runstore.Record) (webview.ReleaseSummary, error) {
	if err := runstore.ValidateRecord(record); err != nil {
		return webview.ReleaseSummary{}, err
	}
	registered, ok := h.manager.processes.Lookup(record.Run.ProcessID)
	if !ok {
		return webview.ReleaseSummary{},
			fmt.Errorf("release record %d has unknown process %q", record.ID, record.Run.ProcessID)
	}
	run, err := loadReleaseRun(registered.Implementation, record.Run.Snapshot)
	if err != nil {
		return webview.ReleaseSummary{}, fmt.Errorf("validate release record %d: %w", record.ID, err)
	}
	status, err := record.Run.Status()
	if err != nil {
		return webview.ReleaseSummary{}, err
	}
	summary := h.runSummary(record.Run, run.TakeView())
	summary.Status = string(status)
	summary.UpdatedAt = record.UpdatedAt
	summary.RecordID = record.ID
	summary.RecordURL = record.URL
	return summary, nil
}

func (h *History) runSummary(run *runstore.State, view *contract.RunView) webview.ReleaseSummary {
	registered, _ := h.manager.processes.Lookup(run.ProcessID)
	identity := registered.Group.Identity
	status := "ready"
	if run.Started {
		status = "starting"
		if view != nil && view.Summary != "" {
			status = view.Summary
		}
	}
	if run.Complete {
		status = run.Result
	}
	updatedAt := run.UpdatedAt
	mode := ""
	if view != nil {
		mode = view.Detail
		if !view.UpdatedAt.IsZero() {
			updatedAt = view.UpdatedAt
		}
	}
	return webview.ReleaseSummary{
		Mark: identity.Mark, Name: identity.Name,
		Mode: mode, Status: status, UpdatedAt: updatedAt, Href: processregistry.Path(run.ProcessID),
	}
}

func (h *History) processSummaries() []webview.ProcessSummary {
	groups := h.manager.processes.Groups()
	summaries := make([]webview.ProcessSummary, 0, len(groups))
	for _, group := range groups {
		defaultProcess := group.Processes[0]
		summaries = append(summaries, webview.ProcessSummary{
			ID: defaultProcess.Definition.ID, Name: group.Identity.Name, Mark: group.Identity.Mark,
			Description: group.Identity.Description, Href: processregistry.Path(defaultProcess.Definition.ID),
		})
	}
	return summaries
}

func releaseSelection(session *processSession, id int) (string, bool, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if href, selected := session.selectedReleaseHrefLocked(id); selected {
		return href, true, nil
	}
	if session.hasSelectedOrPreparedReleaseLocked() {
		return "", false, errors.New("restart without a selected or prepared release for this process before selecting a different record")
	}
	return "", false, nil
}

func exportRecord(record *runstore.Record) webview.ReleaseRecordExport[*runstore.State] {
	if record == nil {
		return webview.ReleaseRecordExport[*runstore.State]{}
	}
	return webview.ReleaseRecordExport[*runstore.State]{
		ID: record.ID, Revision: record.Revision, URL: record.URL, Run: record.Run.Clone(),
	}
}

func addDashboardRelease(result *webview.DashboardResponse, summary webview.ReleaseSummary) {
	switch summary.Status {
	case resultSucceeded:
		result.Recent = append(result.Recent, summary)
	case resultFailed, resultCanceled, resultUncertain:
		result.NeedsAttention = append(result.NeedsAttention, summary)
	default:
		result.Ongoing = append(result.Ongoing, summary)
	}
}
