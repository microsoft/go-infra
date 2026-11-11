// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

const (
	dashboardActiveLimit  = 50
	dashboardRecentLimit  = 10
	staleHeartbeatTimeout = time.Minute
)

// History reads published views. Checkpoint export and repair are explicit administrative actions.
type History struct {
	manager *Manager
	reader  runstore.Reader
}

func (h *History) Dashboard(ctx context.Context) webview.DashboardResponse {
	result := webview.DashboardResponse{
		Ongoing: []webview.ReleaseSummary{}, NeedsAttention: []webview.ReleaseSummary{},
		Recent: []webview.ReleaseSummary{}, Processes: h.processSummaries(),
	}
	active, activeErr := h.reader.QueryViews(ctx, false, dashboardActiveLimit)
	recent, recentErr := h.reader.QueryViews(ctx, true, dashboardRecentLimit)
	addTrackingError(&result, errors.Join(activeErr, recentErr))
	for _, record := range append(active, recent...) {
		if err := runstore.ValidateViewRecord(record); err != nil {
			addTrackingError(&result, err)
			continue
		}
		view := record.View
		status := string(view.Status)
		if view.Status == runstore.StatusRunning && !view.Executing {
			status = "saved"
		}
		summary := webview.ReleaseSummary{
			Name: view.Name, Mark: view.Mark, Mode: view.Detail,
			Status: status, SavedStatus: string(view.Status), UpdatedAt: view.UpdatedAt,
			RunURL: view.RunURL, RecordID: record.ID, RecordURL: record.URL,
			Closed: record.Closed, Href: releaseViewPath(record.ID),
		}
		if record.Closed {
			result.Recent = append(result.Recent, summary)
		} else {
			addDashboardRelease(&result, summary)
		}
	}
	return result
}

// View only requests published data; even an unavailable process checkpoint remains observable.
func (h *History) View(ctx context.Context, id int) (webview.ReleaseViewResponse[*runstore.ViewRecord], error) {
	record, err := h.reader.GetView(ctx, id)
	if err != nil {
		return webview.ReleaseViewResponse[*runstore.ViewRecord]{}, errorf(ErrorStore, "load published release: %v", err)
	}
	if err := runstore.ValidateViewRecord(record); err != nil {
		return webview.ReleaseViewResponse[*runstore.ViewRecord]{}, newError(ErrorStore, err)
	}
	result := webview.ReleaseViewResponse[*runstore.ViewRecord]{Record: record}
	if _, configured := h.manager.processes.Lookup(record.View.ProcessID); configured &&
		!record.Closed && record.View.Status == runstore.StatusRunning &&
		(!record.View.Executing || time.Since(record.View.UpdatedAt) >= staleHeartbeatTimeout) {

		result.ResumeURL = "/api/processes/" + record.View.ProcessID + "/resume"
	}
	return result, nil
}

func (h *History) Export(ctx context.Context, id int) (webview.ReleaseRecordExport[*runstore.State], error) {
	record, err := h.manager.store.Get(ctx, id)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, errorf(ErrorStore, "load release checkpoint: %v", err)
	}
	if err := runstore.ValidateRecord(record); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorStore, err)
	}
	return exportRecord(record), nil
}

func (h *History) Import(
	ctx context.Context,
	id int,
	imported webview.ReleaseRecordExport[*runstore.State],
) (webview.ReleaseRecordExport[*runstore.State], error) {
	if imported.ID != id || imported.Revision <= 0 || imported.Run == nil || imported.Run.Executing {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorInvalid, errors.New("repair must identify a record and cannot claim an executing runner"))
	}
	if err := imported.Run.Validate(); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}
	current, err := h.manager.store.Get(ctx, id)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, errorf(ErrorStore, "load release checkpoint: %v", err)
	}
	if err := runstore.ValidateRecord(current); err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorStore, err)
	}
	if current.Closed {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorConflict, errors.New("closed release records cannot be repaired"))
	}
	if current.Revision != imported.Revision {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorConflict, errors.New("release record changed; export it again before importing"))
	}
	if current.Run.ProcessID != imported.Run.ProcessID || current.Run.Digest != imported.Run.Digest ||
		!bytes.Equal(current.Run.Snapshot.Input, imported.Run.Snapshot.Input) {

		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorInvalid, errors.New("import cannot change the release process, input or immutable intent"))
	}
	session, ok := h.manager.processSession(current.Run.ProcessID)
	if !ok {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorInvalid, errors.New("repair requires the matching release process"))
	}
	session.selectionMu.Lock()
	defer session.selectionMu.Unlock()
	session.mu.Lock()
	running := session.running
	session.mu.Unlock()
	if running {
		return webview.ReleaseRecordExport[*runstore.State]{},
			newError(ErrorConflict, errors.New("stop this release runner before repairing its checkpoint"))
	}
	run, steps, err := h.validateImport(ctx, current.Run, imported.Run)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}
	runner, err := coordinator.NewStepRunner(steps)
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}
	view, err := h.manager.publishedView(imported.Run, run.TakeView(), run.Plan(), runner.Snapshot())
	if err != nil {
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(ErrorInvalid, err)
	}
	updated, err := h.manager.store.Update(ctx, current, imported.Run, view)
	if err != nil {
		kind := ErrorStore
		if errors.Is(err, runstore.ErrConflict) {
			kind = ErrorConflict
		}
		return webview.ReleaseRecordExport[*runstore.State]{}, newError(kind, err)
	}
	return exportRecord(updated), nil
}

func (h *History) validateImport(
	ctx context.Context, current, candidate *runstore.State,
) (contract.Run, []*coordinator.Step, error) {
	registered, ok := h.manager.processes.Lookup(candidate.ProcessID)
	if !ok {
		return nil, nil, errors.New("release process is not configured")
	}
	previous, err := loadReleaseRun(registered.Implementation, current.Snapshot)
	if err != nil {
		return nil, nil, err
	}
	run, err := loadReleaseRun(registered.Implementation, candidate.Snapshot)
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(previous.Plan(), run.Plan()) {
		return nil, nil, errors.New("import cannot change the reviewed plan")
	}
	steps, err := run.Build()
	if err != nil {
		return nil, nil, err
	}
	if err := validateProcessRunGraph(steps); err != nil {
		return nil, nil, err
	}
	snapshot, err := h.manager.snapshotReleaseRun(candidate.ProcessID, run)
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(snapshot, candidate.Snapshot) {
		return nil, nil, errors.New("building the repaired graph changed its checkpoint")
	}
	return run, steps, nil
}

func (h *History) processSummaries() []webview.ProcessSummary {
	groups := h.manager.processes.Groups()
	summaries := make([]webview.ProcessSummary, 0, len(groups))
	for _, group := range groups {
		process := group.Processes[0]
		summaries = append(summaries, webview.ProcessSummary{
			ID: process.Definition.ID, Name: group.Identity.Name, Mark: group.Identity.Mark,
			Description: group.Identity.Description, Href: processregistry.Path(process.Definition.ID),
		})
	}
	return summaries
}

func releaseViewPath(id int) string { return fmt.Sprintf("/releases/%d", id) }

func exportRecord(record *runstore.Record) webview.ReleaseRecordExport[*runstore.State] {
	return webview.ReleaseRecordExport[*runstore.State]{
		ID: record.ID, Revision: record.Revision, URL: record.URL, Run: record.Run.Clone(),
	}
}

func addDashboardRelease(result *webview.DashboardResponse, summary webview.ReleaseSummary) {
	switch summary.Status {
	case resultSucceeded:
		result.Recent = append(result.Recent, summary)
	case resultFailed, resultCanceled, resultUncertain, "saved":
		result.NeedsAttention = append(result.NeedsAttention, summary)
	default:
		result.Ongoing = append(result.Ongoing, summary)
	}
}

func addTrackingError(result *webview.DashboardResponse, err error) {
	if err == nil {
		return
	}
	var problem *runstore.RecordError
	if !errors.As(err, &problem) {
		if result.TrackingError != "" {
			result.TrackingError += "\n"
		}
		result.TrackingError += err.Error()
		return
	}
	switch err := err.(type) {
	case *runstore.RecordError:
		for _, summaries := range [][]webview.ReleaseSummary{result.NeedsAttention, result.Recent} {
			for _, summary := range summaries {
				if summary.Issue != nil && summary.RecordID == err.ID {
					return
				}
			}
		}
		name := strings.TrimSpace(err.Title)
		if name == "" {
			name = "Saved release"
		}
		summary := webview.ReleaseSummary{
			Mark: "!", Name: name, UpdatedAt: err.UpdatedAt,
			Status: err.SavedStatus, SavedStatus: err.SavedStatus, RecordID: err.ID, RecordURL: err.URL,
			Closed: err.Closed,
			Issue: &webview.ReleaseIssue{
				RecordState: err.RecordState, Message: err.Message, Guidance: err.Guidance, Detail: err.Error(),
			},
		}
		if summary.Closed {
			result.Recent = append(result.Recent, summary)
		} else {
			result.NeedsAttention = append(result.NeedsAttention, summary)
		}
	case interface{ Unwrap() []error }:
		for _, child := range err.Unwrap() {
			addTrackingError(result, child)
		}
	case interface{ Unwrap() error }:
		addTrackingError(result, err.Unwrap())
	}
}
