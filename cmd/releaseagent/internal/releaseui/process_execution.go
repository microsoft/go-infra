// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

// ProcessExecutor supplies only the policy needed to prepare and run one durable process action.
type ProcessExecutor struct {
	Preflight func(context.Context) (string, error)
	Prepare   func(context.Context, json.RawMessage) (ProcessPreparedRun, error)
	Execute   func(context.Context, json.RawMessage, ProcessCheckpointFunc) error
	Resume    func(context.Context, json.RawMessage, json.RawMessage, ProcessCheckpointFunc) error
	Validate  func(*ProcessRun) error
}

// ProcessCheckpointFunc durably records an external run before execution continues.
type ProcessCheckpointFunc func(context.Context, ProcessRunCheckpoint) error

// ProcessRunCheckpoint contains process-specific resumable state and process-neutral display data.
type ProcessRunCheckpoint struct {
	State    json.RawMessage
	External ProcessRunReference
	Progress ProcessRunProgress
}

// ProcessRunProgress is the process-neutral progress reported by an external action.
type ProcessRunProgress struct {
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

type processRunResponse struct {
	Input     json.RawMessage   `json:"input"`
	Steps     []planStep        `json:"steps"`
	SessionID string            `json:"sessionId"`
	Execution executionResponse `json:"execution"`
	View      ProcessPlanView   `json:"view"`
}

// WithProcessExecutor enables a process-specific policy adapter behind the shared durable lifecycle.
func WithProcessExecutor(processID string, executor ProcessExecutor) Option {
	return func(server *Server) {
		if server.processExecutors == nil {
			server.processExecutors = make(map[string]ProcessExecutor)
		}
		server.processExecutors[processID] = executor
	}
}

// WithProcessRunStore enables durable intent, checkpoint, and result persistence.
func WithProcessRunStore(store ProcessRunStore) Option {
	return func(server *Server) {
		server.processRunStore = store
	}
}

func (s *Server) validateProcessExecutionConfiguration() error {
	for processID, executor := range s.processExecutors {
		definition, ok := s.processes.byID[processID]
		if !ok || definition.Workflow == nil || !definition.Workflow.DurableAction {
			return fmt.Errorf("process executor %q has no durable workflow definition", processID)
		}
		if executor.Preflight == nil || executor.Prepare == nil || executor.Execute == nil ||
			executor.Resume == nil || executor.Validate == nil {

			return fmt.Errorf("process executor %q is incomplete", processID)
		}
	}
	if len(s.processExecutors) > 0 && s.processRunStore == nil {
		return errors.New("process execution requires a durable run store")
	}
	if s.processRunStore != nil && len(s.processExecutors) == 0 {
		return errors.New("process run store requires at least one executor")
	}
	return nil
}

func (s *Server) handleProcessRunPreflight(
	processID, processName string,
	response http.ResponseWriter,
	request *http.Request,
) {
	report := PreflightReport{Checks: []PreflightCheck{{
		ID: "loopback-server", Name: "Loopback-only HTTP server", Status: CheckStatusPassed,
		Details: "The release UI accepts requests only through a local loopback address.",
	}}}
	s.mu.Lock()
	executor, enabled := s.processExecutors[processID]
	s.mu.Unlock()
	if !enabled {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: processID + "-execution", Name: processName + " external action", Status: CheckStatusUnavailable,
			Details: "External execution is disabled. Restart with the process execution option enabled.",
		})
		writeJSON(response, http.StatusOK, report)
		return
	}
	report.PlanningEnabled = true
	details, err := executor.Preflight(request.Context())
	if err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: processID + "-execution", Name: processName + " external action", Status: CheckStatusWarning,
			Details: "Planning is available, but external execution is not ready: " + err.Error(),
		})
		writeJSON(response, http.StatusOK, report)
		return
	}
	report.ExternalExecutionEnabled = true
	report.Checks = append(report.Checks, PreflightCheck{
		ID: processID + "-execution", Name: processName + " external action", Status: CheckStatusPassed,
		Details: details,
	})
	writeJSON(response, http.StatusOK, report)
}

func (s *Server) handleGetProcessRun(processID string, response http.ResponseWriter) {
	s.mu.Lock()
	if s.processRun == nil || s.processRun.ProcessID != processID {
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

	s.mu.Lock()
	executor, enabled := s.processExecutors[processID]
	definition, defined := s.processes.process(processID)
	if !enabled {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "external process execution is not enabled")
		return
	}
	if !defined || definition.Workflow == nil || !definition.Workflow.DurableAction {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, "durable process definition is unavailable")
		return
	}
	if s.simulationRunning || s.releaseRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.processRun != nil && s.processRun.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a previous external action has uncertain status; inspect the target service and remove the process run journal before retrying")
		return
	}
	s.mu.Unlock()
	normalizedInput, err := normalizeProcessInputs(definition.Workflow.Inputs, input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	prepared, err := executor.Prepare(request.Context(), normalizedInput)
	if err != nil {
		status := http.StatusConflict
		var inputErr *processInputError
		if errors.As(err, &inputErr) {
			status = http.StatusBadRequest
		}
		writeError(response, status, err.Error())
		return
	}
	run, err := newProcessRun(processID, prepared)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("build process run: %v", err))
		return
	}
	if err := executor.Validate(run); err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("validate process run: %v", err))
		return
	}
	step := processExecutionStep(run, func(context.Context) error { return nil })

	s.mu.Lock()
	if s.simulationRunning || s.releaseRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.processRun != nil && s.processRun.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a previous external action has uncertain status; inspect the target service and remove the process run journal before retrying")
		return
	}
	if err := s.processRunStore.Save(request.Context(), run); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist reviewed process run: %v", err))
		return
	}
	s.processRun = run
	s.steps = []*coordinator.Step{step}
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

	s.mu.Lock()
	executor, enabled := s.processExecutors[processID]
	if !enabled {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "external process execution is not enabled")
		return
	}
	if s.processRun == nil || s.processRun.ProcessID != processID {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "review this external action first")
		return
	}
	if !secureEqual(start.PlanDigest, s.processRun.Digest) {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "run request does not match the reviewed external action")
		return
	}
	if !start.Confirmed {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirm the external action before starting it")
		return
	}
	if s.processRun.Started || s.processRunning || s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the reviewed external action has already started")
		return
	}
	run := cloneProcessRun(s.processRun)
	step := processExecutionStep(run, func(ctx context.Context) error {
		if _, err := executor.Preflight(ctx); err != nil {
			return fmt.Errorf("external action preflight failed before mutation: %w", err)
		}
		return executor.Execute(ctx, run.Payload, s.processCheckpoint(run.Digest, executor))
	})
	s.steps = []*coordinator.Step{step}
	s.runner = &coordinator.StepRunner{}
	runner := s.runner
	run.Started = true
	if err := executor.Validate(run); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("validate started process run: %v", err))
		return
	}
	if err := s.processRunStore.Save(request.Context(), run); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("checkpoint process run before mutation: %v", err))
		return
	}
	s.processRun = run
	s.processRunning = true
	s.mu.Unlock()

	go s.executeProcessRun(run.Digest, runner, step, executor)
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "external action started"})
}

func (s *Server) processCheckpoint(digest string, executor ProcessExecutor) ProcessCheckpointFunc {
	return func(ctx context.Context, checkpoint ProcessRunCheckpoint) error {
		if !json.Valid(checkpoint.State) {
			return errors.New("process checkpoint state is invalid JSON")
		}
		s.mu.Lock()
		if s.processRun == nil || !secureEqual(s.processRun.Digest, digest) {
			s.mu.Unlock()
			return errors.New("external run no longer matches the active process plan")
		}
		run := cloneProcessRun(s.processRun)
		run.Checkpoint = append(json.RawMessage(nil), checkpoint.State...)
		external := checkpoint.External
		run.External = &external
		if err := executor.Validate(run); err != nil {
			s.mu.Unlock()
			return err
		}
		if err := s.processRunStore.Save(ctx, run); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("persist external run checkpoint: %w", err)
		}
		s.processRun = run
		s.mu.Unlock()
		coordinator.ReportProgress(ctx, coordinator.StepProgress{
			Summary: checkpoint.Progress.Summary, Detail: checkpoint.Progress.Detail,
			Completed: checkpoint.Progress.Completed, Total: checkpoint.Progress.Total,
		})
		return nil
	}
}

func (s *Server) executeProcessRun(digest string, runner *coordinator.StepRunner, step *coordinator.Step, executor ProcessExecutor) {
	err := runner.Execute(s.ctx, []*coordinator.Step{step})
	var resumeRunner *coordinator.StepRunner
	var resumeStep *coordinator.Step
	s.mu.Lock()
	if s.processRun != nil && secureEqual(s.processRun.Digest, digest) {
		run := cloneProcessRun(s.processRun)
		resumable := run.External != nil && !run.External.Terminal &&
			(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
		switch {
		case err == nil:
			run.Complete = true
			run.Result = "succeeded"
		case resumable:
			run.Complete = false
			run.Result = ""
		default:
			run.Complete = true
			if run.External != nil && run.External.Terminal {
				run.Result = "failed"
			} else {
				run.Result = "uncertain"
			}
		}
		valid := true
		if validateErr := executor.Validate(run); validateErr != nil {
			run.Complete = true
			run.Result = "uncertain"
			valid = false
		}
		if saveErr := s.processRunStore.Save(context.Background(), run); saveErr != nil {
			run.Complete = true
			run.Result = "uncertain"
			valid = false
		}
		s.processRun = run
		if valid && resumable && errors.Is(err, context.DeadlineExceeded) && s.ctx.Err() == nil {
			resumeStep = processExecutionStep(run, func(ctx context.Context) error {
				return executor.Resume(ctx, run.Payload, run.Checkpoint, s.processCheckpoint(run.Digest, executor))
			})
			s.steps = []*coordinator.Step{resumeStep}
			s.runner = &coordinator.StepRunner{}
			resumeRunner = s.runner
			s.processRunning = true
		} else {
			s.processRunning = false
		}
	} else {
		s.processRunning = false
	}
	s.mu.Unlock()
	if resumeRunner != nil {
		go s.executeProcessRun(digest, resumeRunner, resumeStep, executor)
	}
}

func (s *Server) restoreProcessRun() error {
	if s.processRunStore == nil {
		return nil
	}
	run, err := s.processRunStore.Load(s.ctx)
	if errors.Is(err, ErrProcessRunNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load process run journal: %w", err)
	}
	executor, ok := s.processExecutors[run.ProcessID]
	if !ok {
		return fmt.Errorf("stored process %q has no configured executor", run.ProcessID)
	}
	if err := executor.Validate(run); err != nil {
		return fmt.Errorf("validate stored process run: %w", err)
	}
	if run.Started && !run.Complete && len(run.Checkpoint) == 0 {
		run.Complete = true
		run.Result = "uncertain"
		if err := s.processRunStore.Save(s.ctx, run); err != nil {
			return fmt.Errorf("mark interrupted process run uncertain: %w", err)
		}
	}
	s.processRun = run
	if s.goImages.document != nil {
		if run.Result == "uncertain" || run.Started && !run.Complete {
			return errors.New("both a go-images session and an active or uncertain process run exist; inspect the process run journal before restarting")
		}
		return nil
	}
	step := processExecutionStep(run, func(context.Context) error { return nil })
	if run.Started && !run.Complete && len(run.Checkpoint) > 0 {
		step = processExecutionStep(run, func(ctx context.Context) error {
			return executor.Resume(ctx, run.Payload, run.Checkpoint, s.processCheckpoint(run.Digest, executor))
		})
		s.processRunning = true
	}
	s.steps = []*coordinator.Step{step}
	s.runner = &coordinator.StepRunner{}
	s.activeProcessID = run.ProcessID
	if s.processRunning {
		go s.executeProcessRun(run.Digest, s.runner, step, executor)
	}
	return nil
}

func (s *Server) processRunResponseLocked() processRunResponse {
	run := s.processRun
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
		Enabled: true, Eligible: run.Digest != "", PlanDigest: run.Digest,
	}
	if run.Started {
		reference := run.Target
		if run.External != nil {
			reference = *run.External
		}
		execution.Run = pipelineRun{
			BuildID: reference.ID, URL: reference.URL, LinkLabel: reference.LinkLabel,
			Complete: run.Complete, Result: run.Result,
		}
	}
	return processRunResponse{
		Input: append(json.RawMessage(nil), run.Input...), Steps: steps, SessionID: run.Digest,
		Execution: execution, View: run.View,
	}
}

func processExecutionStep(run *ProcessRun, action coordinator.StepFunc) *coordinator.Step {
	return coordinator.NewRootStep(run.Step.Name, run.Step.Timeout, action)
}
