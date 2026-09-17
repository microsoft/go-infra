// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

// CheckpointFunc durably records process state before execution continues.
type CheckpointFunc func(context.Context, ReleaseCheckpoint) error

// ReleaseCheckpoint contains process-specific resumable state and optional external-run data.
type ReleaseCheckpoint struct {
	State    json.RawMessage
	External *ReleaseReference
	Progress ReleaseProgress
}

// ReleaseProgress is the process-neutral progress reported by an external action.
type ReleaseProgress struct {
	Summary   string
	Detail    string
	Completed int
	Total     int
}

type processInputError struct {
	err error
}

func (e *processInputError) Error() string {
	return e.err.Error()
}

func (e *processInputError) Unwrap() error {
	return e.err
}

// InvalidProcessInput marks a preparation error as invalid browser input.
func InvalidProcessInput(err error) error {
	return &processInputError{err: err}
}

func snapshotReleaseRun(run ReleaseRun) (*ReleaseRunState, error) {
	if run == nil {
		return nil, errors.New("release run is nil")
	}
	state := run.Snapshot()
	if err := validateProcessRun(state); err != nil {
		return nil, fmt.Errorf("validate release run snapshot: %w", err)
	}
	return state, nil
}

func restoreReleaseRun(process ReleaseProcess, state *ReleaseRunState) (ReleaseRun, error) {
	if state == nil {
		return nil, errors.New("release run state is nil")
	}
	if state.ProcessID != process.Definition().ID {
		return nil, fmt.Errorf("release run process %q does not match %q", state.ProcessID, process.Definition().ID)
	}
	run, err := process.Restore(CloneReleaseRunState(state))
	if err != nil {
		return nil, err
	}
	restored, err := snapshotReleaseRun(run)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(restored, state) {
		return nil, errors.New("restored release run changed its durable state")
	}
	return run, nil
}

type processRunResponse struct {
	Input     json.RawMessage   `json:"input"`
	Steps     []planStep        `json:"steps"`
	SessionID string            `json:"sessionId"`
	Execution executionResponse `json:"execution"`
	View      ProcessPlanView   `json:"view"`
}

type releaseStartRequest struct {
	PlanDigest string `json:"planDigest"`
	Confirmed  bool   `json:"confirmed"`
}

// WithReleaseRunStore enables durable intent, checkpoint, and result persistence.
func WithReleaseRunStore(store ReleaseRunStore) Option {
	return func(server *Server) {
		server.processRunStore = store
	}
}

func (s *Server) validateProcessExecutionConfiguration() error {
	return nil
}

func (s *Server) handleProcessRunPreflight(processID string, response http.ResponseWriter, request *http.Request) {
	report := PreflightReport{Checks: []PreflightCheck{{
		ID: "loopback-server", Name: "Loopback-only HTTP server", Status: CheckStatusPassed,
		Details: "The release UI accepts requests only through a local loopback address.",
	}}}
	registered, ok := s.processes.process(processID)
	if !ok {
		http.NotFound(response, request)
		return
	}
	if s.processRunStore == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: "release-tracking", Name: "Azure DevOps release tracking", Status: CheckStatusWarning,
			Details: "Not configured. Planning is available, but confirmed releases cannot start or be restored.",
		})
	} else {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: "release-tracking", Name: "Azure DevOps release tracking", Status: CheckStatusPassed,
			Details: "Enabled. Confirmed release state is stored in a revisioned work item; unconfirmed plans remain in memory.",
		})
	}
	readiness, err := registered.process.Preflight(request.Context())
	report.PlanningEnabled = readiness.PlanningEnabled
	report.ExternalExecutionEnabled = readiness.ExecutionEnabled && s.processRunStore != nil
	status := CheckStatusPassed
	details := readiness.Details
	if err != nil {
		status = CheckStatusWarning
		details = err.Error()
	}
	if !readiness.PlanningEnabled {
		status = CheckStatusUnavailable
	}
	if details == "" {
		details = "The process is not configured."
	}
	report.Checks = append(report.Checks, PreflightCheck{
		ID: processID + "-readiness", Name: registered.definition.Name + " readiness", Status: status, Details: details,
	})
	writeJSON(response, http.StatusOK, report)
}

func (s *Server) handleGetProcessRun(processID string, response http.ResponseWriter) {
	s.mu.Lock()
	if s.processRun == nil {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	run, err := snapshotReleaseRun(s.processRun)
	if err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if run.ProcessID != processID {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	result := s.processRunResponseLocked()
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handlePrepareProcessRun(processID string, response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var input json.RawMessage
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	registered, ok := s.processes.process(processID)
	if !ok {
		http.NotFound(response, request)
		return
	}
	s.mu.Lock()
	if s.simulationRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.processRun != nil {
		current, err := snapshotReleaseRun(s.processRun)
		if err != nil {
			s.mu.Unlock()
			writeError(response, http.StatusInternalServerError, err.Error())
			return
		}
		if current.Result == "uncertain" {
			s.mu.Unlock()
			writeError(response, http.StatusConflict, "a previous external action has uncertain status; inspect the target service and repair its release work item before retrying")
			return
		}
	}
	s.mu.Unlock()
	normalizedInput, err := normalizeProcessInputs(registered.definition.Workflow.Inputs, input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	releaseRun, err := registered.process.Prepare(request.Context(), normalizedInput)
	if err != nil {
		status := http.StatusConflict
		var inputErr *processInputError
		if errors.As(err, &inputErr) {
			status = http.StatusBadRequest
		}
		writeError(response, status, err.Error())
		return
	}
	run, err := snapshotReleaseRun(releaseRun)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("snapshot prepared release run: %v", err))
		return
	}
	if run.ProcessID != processID {
		writeError(response, http.StatusInternalServerError, "prepared release run has the wrong process ID")
		return
	}
	releaseRun, err = restoreReleaseRun(registered.process, run)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("restore prepared release run: %v", err))
		return
	}
	steps, err := releaseRun.Steps(request.Context(), nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("build process graph: %v", err))
		return
	}
	if err := matchProcessRunGraph(run, steps); err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("validate process graph: %v", err))
		return
	}
	s.mu.Lock()
	if s.simulationRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	s.processRun = releaseRun
	s.processRunRecord = nil
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	result := s.processRunResponseLocked()
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleStartProcessRun(processID string, response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var start releaseStartRequest
	if err := decodeJSON(response, request, &start); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	registered, ok := s.processes.process(processID)
	if !ok {
		http.NotFound(response, request)
		return
	}
	s.mu.Lock()
	if s.processRun == nil {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "review this external action first")
		return
	}
	current, err := snapshotReleaseRun(s.processRun)
	if err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if current.ProcessID != processID {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "review this external action first")
		return
	}
	if !secureEqual(start.PlanDigest, current.Digest) {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "run request does not match the reviewed external action")
		return
	}
	if !start.Confirmed {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirm the external action before starting it")
		return
	}
	if current.Started || s.processRunning || s.simulationRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the reviewed external action has already started")
		return
	}
	if s.processRunStore == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "release tracking is required before starting an external action")
		return
	}
	run := CloneReleaseRunState(current)
	s.processRunning = true
	s.mu.Unlock()

	readiness, err := registered.process.Preflight(request.Context())
	if err != nil || !readiness.ExecutionEnabled {
		s.stopProcessStart(run.Digest)
		if err == nil {
			err = errors.New("external execution is not ready")
		}
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("external action preflight failed before mutation: %v", err))
		return
	}
	run.Started = true
	releaseRun, err := restoreReleaseRun(registered.process, run)
	if err != nil {
		s.stopProcessStart(run.Digest)
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("validate started process run: %v", err))
		return
	}
	steps, err := releaseRun.Steps(request.Context(), s.processCheckpoint(run.Digest, registered.process))
	if err != nil {
		s.stopProcessStart(run.Digest)
		writeError(response, http.StatusConflict, fmt.Sprintf("build process graph: %v", err))
		return
	}
	if err := matchProcessRunGraph(run, steps); err != nil {
		s.stopProcessStart(run.Digest)
		writeError(response, http.StatusConflict, "execution graph no longer matches the reviewed plan")
		return
	}
	record, err := s.processRunStore.Create(request.Context(), run)
	if err != nil {
		s.stopProcessStart(run.Digest)
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create release work item before mutation: %v", err))
		return
	}
	s.mu.Lock()
	current, stateErr := snapshotReleaseRun(s.processRun)
	if stateErr != nil || current.Started || !secureEqual(current.Digest, run.Digest) {
		s.processRunning = false
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "reviewed external action changed before start")
		return
	}
	persistedRun, err := restoreReleaseRun(registered.process, record.Run)
	if err != nil {
		s.processRunning = false
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("restore persisted release run: %v", err))
		return
	}
	s.processRun = persistedRun
	s.processRunRecord = record
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	runner := s.runner
	s.mu.Unlock()

	go s.executeProcessRun(run.Digest, runner, steps, registered.process)
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "external action started"})
}

func (s *Server) stopProcessStart(digest string) {
	s.mu.Lock()
	if s.processRun != nil {
		run, err := snapshotReleaseRun(s.processRun)
		if err == nil && secureEqual(run.Digest, digest) && !run.Started {
			s.processRunning = false
		}
	}
	s.mu.Unlock()
}

func (s *Server) processCheckpoint(digest string, process ReleaseProcess) CheckpointFunc {
	return func(ctx context.Context, checkpoint ReleaseCheckpoint) error {
		if !json.Valid(checkpoint.State) {
			return errors.New("process checkpoint state is invalid JSON")
		}
		if checkpoint.External != nil {
			if err := validateProcessRunReference(*checkpoint.External); err != nil {
				return err
			}
		}
		s.mu.Lock()
		current, err := snapshotReleaseRun(s.processRun)
		if err != nil || !secureEqual(current.Digest, digest) {
			s.mu.Unlock()
			return errors.New("external run no longer matches the active process plan")
		}
		run := CloneReleaseRunState(current)
		run.Checkpoint = append(json.RawMessage(nil), checkpoint.State...)
		run.External = nil
		if checkpoint.External != nil {
			external := *checkpoint.External
			run.External = &external
		}
		if _, err := restoreReleaseRun(process, run); err != nil {
			s.mu.Unlock()
			return err
		}
		if s.processRunRecord == nil {
			s.mu.Unlock()
			return errors.New("external run has no release work item")
		}
		record, err := s.processRunStore.Update(ctx, s.processRunRecord, run)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("persist external run checkpoint: %w", err)
		}
		persistedRun, err := restoreReleaseRun(process, record.Run)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("restore persisted external run checkpoint: %w", err)
		}
		s.processRunRecord = record
		s.processRun = persistedRun
		s.mu.Unlock()
		coordinator.ReportProgress(ctx, coordinator.StepProgress{
			Summary: checkpoint.Progress.Summary, Detail: checkpoint.Progress.Detail,
			Completed: checkpoint.Progress.Completed, Total: checkpoint.Progress.Total,
		})
		return nil
	}
}

func (s *Server) executeProcessRun(
	digest string,
	runner *coordinator.StepRunner,
	steps []*coordinator.Step,
	process ReleaseProcess,
) {
	err := runner.Execute(s.ctx, steps)
	s.mu.Lock()
	current, stateErr := snapshotReleaseRun(s.processRun)
	if stateErr == nil && secureEqual(current.Digest, digest) {
		run := CloneReleaseRunState(current)
		resumable := len(run.Checkpoint) > 0 && !run.Complete &&
			(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
		switch {
		case err == nil:
			run.Complete = true
			run.Result = "succeeded"
		case resumable:
			run.Result = ""
		default:
			run.Complete = true
			if run.External != nil && run.External.Terminal {
				run.Result = "failed"
			} else {
				run.Result = "uncertain"
			}
		}
		_, validateErr := restoreReleaseRun(process, run)
		if validateErr != nil {
			run.Complete = true
			run.Result = "uncertain"
		}
		var restoredRun ReleaseRun
		record, saveErr := s.processRunStore.Update(context.Background(), s.processRunRecord, run)
		if saveErr != nil {
			run.Complete = true
			run.Result = "uncertain"
			restoredRun, _ = restoreReleaseRun(process, run)
		} else {
			s.processRunRecord = record
			run = record.Run
			restoredRun, _ = restoreReleaseRun(process, run)
		}
		if restoredRun != nil {
			s.processRun = restoredRun
		}
	}
	s.processRunning = false
	s.mu.Unlock()
}

func (s *Server) restoreProcessRunRecord(record *ReleaseRunRecord) error {
	run := record.Run
	registered, ok := s.processes.process(run.ProcessID)
	if !ok {
		return fmt.Errorf("stored process %q is not configured", run.ProcessID)
	}
	releaseRun, err := restoreReleaseRun(registered.process, run)
	if err != nil {
		return fmt.Errorf("validate stored process run: %w", err)
	}
	if run.Started && !run.Complete && len(run.Checkpoint) == 0 {
		run.Complete = true
		run.Result = "uncertain"
		record, err = s.processRunStore.Update(s.ctx, record, run)
		if err != nil {
			return fmt.Errorf("mark interrupted process run uncertain: %w", err)
		}
		run = record.Run
		releaseRun, err = restoreReleaseRun(registered.process, run)
		if err != nil {
			return fmt.Errorf("restore interrupted process run: %w", err)
		}
	}
	steps, err := releaseRun.Steps(s.ctx, s.processCheckpoint(run.Digest, registered.process))
	if err != nil {
		return fmt.Errorf("reconstruct process graph: %w", err)
	}
	if err := matchProcessRunGraph(run, steps); err != nil {
		return fmt.Errorf("restore process graph: %w", err)
	}
	s.processRun = releaseRun
	s.processRunRecord = record
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	s.activeProcessID = run.ProcessID
	if run.Started && !run.Complete {
		s.processRunning = true
		go s.executeProcessRun(run.Digest, s.runner, steps, registered.process)
	}
	return nil
}

func (s *Server) processRunResponseLocked() processRunResponse {
	run, err := snapshotReleaseRun(s.processRun)
	if err != nil {
		return processRunResponse{}
	}
	steps := describeSteps(s.steps)
	if run.Complete {
		status := "succeeded"
		if run.Result != "succeeded" {
			status = "failed"
		}
		for index := range steps {
			steps[index].Status = status
		}
	}
	execution := executionResponse{
		Enabled: s.processRunStore != nil, Eligible: run.Digest != "", PlanDigest: run.Digest,
	}
	if s.processRunRecord != nil {
		execution.WorkItem = &workItemReference{ID: s.processRunRecord.WorkItemID, URL: s.processRunRecord.URL}
	}
	if s.processRunStore == nil {
		execution.UnavailableReason = "Release tracking is unavailable. The workflow can still be simulated."
	}
	if run.Started {
		reference := run.Target
		if run.External != nil {
			reference = *run.External
		}
		execution.Run = pipelineRun{
			BuildID: reference.ID, URL: reference.URL, LinkLabel: reference.LinkLabel,
			Result: run.Result, Complete: run.Complete,
		}
	}
	return processRunResponse{
		Input: append(json.RawMessage(nil), run.Input...), Steps: steps, SessionID: run.SessionID,
		Execution: execution, View: run.View,
	}
}

func matchProcessRunGraph(run *ReleaseRunState, steps []*coordinator.Step) error {
	described, err := describeProcessRunSteps(steps)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(run.Steps, described) {
		return errors.New("constructed process graph does not match the reviewed graph")
	}
	return nil
}

func describeProcessRunSteps(steps []*coordinator.Step) ([]ReleaseStep, error) {
	if len(steps) == 0 {
		return nil, errors.New("process graph has no steps")
	}
	described := make([]ReleaseStep, 0, len(steps))
	seen := make(map[*coordinator.Step]struct{}, len(steps))
	for _, step := range steps {
		if step == nil || step.Func == nil {
			return nil, errors.New("process graph contains an incomplete step")
		}
		entry := ReleaseStep{Name: step.Name, Timeout: step.Timeout}
		if len(step.DependsOn) > 0 {
			entry.DependsOn = make([]string, len(step.DependsOn))
		}
		for index, dependency := range step.DependsOn {
			if _, ok := seen[dependency]; !ok {
				return nil, fmt.Errorf("process step %q depends on a missing or later step", step.Name)
			}
			entry.DependsOn[index] = dependency.Name
		}
		described = append(described, entry)
		seen[step] = struct{}{}
	}
	if err := validateProcessRunSteps(described); err != nil {
		return nil, err
	}
	return described, nil
}
