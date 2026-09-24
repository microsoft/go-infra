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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

func snapshotReleaseRun(run contract.Run) (*contract.StateSnapshot, error) {
	if run == nil {
		return nil, errors.New("release run is nil")
	}
	snapshot := run.TakeSnapshot()
	if err := validateStateSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("validate release run snapshot: %w", err)
	}
	return cloneStateSnapshot(snapshot), nil
}

func loadReleaseRun(process contract.Process, snapshot *contract.StateSnapshot) (contract.Run, error) {
	if err := validateStateSnapshot(snapshot); err != nil {
		return nil, err
	}
	run, err := process.Load(cloneStateSnapshot(snapshot))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("process loaded a nil run")
	}
	return run, nil
}

type processRunResponse struct {
	VariantID string            `json:"variantId"`
	Input     json.RawMessage   `json:"input"`
	Steps     []planStep        `json:"steps"`
	Execution executionResponse `json:"execution"`
	Plan      *contract.Plan    `json:"plan"`
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

func (s *Server) handleProcessRunPreflight(processID string, response http.ResponseWriter, request *http.Request) {
	report := PreflightReport{PlanningEnabled: true, Checks: []PreflightCheck{{
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
	warning, blocking := registered.process.Preflight(request.Context())
	report.ExternalExecutionEnabled = blocking == nil && s.processRunStore != nil
	switch {
	case blocking != nil:
		report.PlanningEnabled = false
		report.Checks = append(report.Checks, PreflightCheck{
			ID: processID + "-readiness", Name: registered.definition.Name + " readiness",
			Status: CheckStatusUnavailable, Details: blocking.Error(),
		})
	case warning != nil:
		report.Checks = append(report.Checks, PreflightCheck{
			ID: processID + "-readiness", Name: registered.definition.Name + " readiness",
			Status: CheckStatusWarning, Details: warning.Error(),
		})
	default:
		report.Checks = append(report.Checks, PreflightCheck{
			ID: processID + "-readiness", Name: registered.definition.Name + " readiness",
			Status: CheckStatusPassed, Details: "Ready.",
		})
	}
	writeJSON(response, http.StatusOK, report)
}

func (s *Server) handleGetProcessRun(processID string, response http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processRun == nil || s.processRunState == nil || s.processRunState.ProcessID != processID {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(response, http.StatusOK, s.processRunResponseLocked())
}

func (s *Server) handlePrepareProcessRun(processID string, response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	registered, ok := s.processes.process(processID)
	if !ok {
		http.NotFound(response, request)
		return
	}
	if _, blocking := registered.process.Preflight(request.Context()); blocking != nil {
		writeError(response, http.StatusPreconditionFailed, blocking.Error())
		return
	}
	var encoded json.RawMessage
	if err := decodeJSON(response, request, &encoded); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	inputs := &releaseflag.InputSet{}
	input := registered.process.InputForm(inputs)
	if err := inputs.Validate(); err != nil {
		panic(fmt.Sprintf("release process %q has invalid input declarations: %v", processID, err))
	}
	if err := inputs.Parse(encoded); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	s.mu.Unlock()

	snapshot, err := registered.process.Prepare(request.Context(), input)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, contract.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeError(response, status, err.Error())
		return
	}
	run, err := loadReleaseRun(registered.process, snapshot)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("load prepared release run: %v", err))
		return
	}
	preparedSnapshot := cloneStateSnapshot(snapshot)
	plan := run.Plan()
	steps, err := run.Build(request.Context(), nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("build process graph: %v", err))
		return
	}
	if err := validateProcessRunGraph(steps); err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("validate process graph: %v", err))
		return
	}
	snapshot, err = snapshotReleaseRun(run)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if !reflect.DeepEqual(snapshot, preparedSnapshot) {
		writeError(response, http.StatusInternalServerError, "Plan or Build changed the prepared release state")
		return
	}
	state, err := newProcessRunState(processID, snapshot)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create release run state: %v", err))
		return
	}

	s.mu.Lock()
	if s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	s.processRun = run
	s.processRunState = state
	s.processPlan = plan
	s.processRunRecord = nil
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	result := s.processRunResponseLockedWithPlan(plan)
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
	if s.processRun == nil || s.processRunState == nil || s.processRunState.ProcessID != processID {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "review this external action first")
		return
	}
	if !secureEqual(start.PlanDigest, s.processRunState.Digest) {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "run request does not match the reviewed external action")
		return
	}
	if !start.Confirmed {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirm the external action before starting it")
		return
	}
	if s.processRunState.Started || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the reviewed external action has already started")
		return
	}
	if s.processRunStore == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "release tracking is required before starting an external action")
		return
	}
	state := s.processRunState.Clone()
	s.processRunning = true
	s.mu.Unlock()

	_, blocking := registered.process.Preflight(request.Context())
	if blocking != nil {
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("external action preflight failed before mutation: %v", blocking))
		return
	}
	run, err := loadReleaseRun(registered.process, state.Snapshot)
	if err != nil {
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("load process run for execution: %v", err))
		return
	}
	plan := run.Plan()
	executionContext, cancel := context.WithCancel(s.ctx)
	checkpointer := s.newProcessCheckpointer(state.Digest, run, cancel)
	steps, err := run.Build(executionContext, checkpointer.Checkpoint)
	if err != nil {
		checkpointer.Close()
		cancel()
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusConflict, fmt.Sprintf("build process graph: %v", err))
		return
	}
	builtSnapshot, err := snapshotReleaseRun(run)
	if err != nil || !reflect.DeepEqual(builtSnapshot, state.Snapshot) {
		checkpointer.Close()
		cancel()
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusConflict, "building the execution graph changed the reviewed release state")
		return
	}
	if err := validateProcessRunGraph(steps); err != nil {
		checkpointer.Close()
		cancel()
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusConflict, "execution graph no longer matches the reviewed plan")
		return
	}
	state.Started = true
	state.UpdatedAt = run.TakeView().UpdatedAt
	record, err := s.processRunStore.Create(request.Context(), state, run.TakeView(), plan)
	if err != nil {
		checkpointer.Close()
		cancel()
		s.stopProcessStart(state.Digest)
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create release work item before mutation: %v", err))
		return
	}
	s.mu.Lock()
	if s.processRunState == nil || s.processRunState.Started || !secureEqual(s.processRunState.Digest, state.Digest) {
		s.processRunning = false
		s.mu.Unlock()
		checkpointer.Close()
		cancel()
		writeError(response, http.StatusConflict, "reviewed external action changed before start")
		return
	}
	s.processRun = run
	s.processRunState = record.Run.Clone()
	s.processPlan = plan
	s.processRunRecord = record
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	s.processCheckpointer = checkpointer
	runner := s.runner
	s.mu.Unlock()

	go s.executeProcessRun(state.Digest, executionContext, runner, steps, run, checkpointer)
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "external action started"})
}

func (s *Server) stopProcessStart(digest string) {
	s.mu.Lock()
	if s.processRunState != nil && secureEqual(s.processRunState.Digest, digest) && !s.processRunState.Started {
		s.processRunning = false
	}
	s.mu.Unlock()
}

type processCheckpointer struct {
	server    *Server
	digest    string
	run       contract.Run
	cancel    context.CancelFunc
	failed    atomic.Bool
	persisted atomic.Bool
	requests  chan chan struct{}
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func (s *Server) newProcessCheckpointer(
	digest string,
	run contract.Run,
	cancel context.CancelFunc,
) *processCheckpointer {
	checkpointer := &processCheckpointer{
		server: s, digest: digest, run: run, cancel: cancel,
		requests: make(chan chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go checkpointer.runLoop()
	return checkpointer
}

func (c *processCheckpointer) Checkpoint() {
	requestDone := make(chan struct{})
	select {
	case c.requests <- requestDone:
	case <-c.done:
		return
	}
	select {
	case <-requestDone:
	case <-c.done:
	}
}

func (c *processCheckpointer) Close() {
	c.stopOnce.Do(func() {
		close(c.stop)
	})
	<-c.done
}

func (c *processCheckpointer) runLoop() {
	defer close(c.done)
	for {
		select {
		case requestDone := <-c.requests:
			persisted := c.persist()
			close(requestDone)
			if !persisted {
				return
			}
		case <-c.stop:
			select {
			case requestDone := <-c.requests:
				c.persist()
				close(requestDone)
			default:
			}
			return
		}
	}
}

func (c *processCheckpointer) persist() bool {
	snapshot, snapshotErr := snapshotReleaseRun(c.run)
	view := c.run.TakeView()
	if snapshotErr != nil {
		c.failed.Store(true)
		c.cancel()
		return false
	}
	c.server.mu.Lock()
	defer c.server.mu.Unlock()
	if c.server.processRunState == nil || c.server.processRunRecord == nil ||
		!secureEqual(c.server.processRunState.Digest, c.digest) {

		c.failed.Store(true)
		c.cancel()
		return false
	}
	state := c.server.processRunState.Clone()
	state.Snapshot = snapshot
	state.Checkpointed = true
	if view != nil && !view.UpdatedAt.IsZero() {
		state.UpdatedAt = view.UpdatedAt
	}
	record, err := c.server.processRunStore.Update(
		c.server.ctx, c.server.processRunRecord, state, view, c.server.processPlan,
	)
	if err != nil {
		c.failed.Store(true)
		c.cancel()
		return false
	}
	c.server.processRunState = record.Run.Clone()
	c.server.processRunRecord = record
	c.persisted.Store(true)
	return true
}

func (s *Server) executeProcessRun(
	digest string,
	ctx context.Context,
	runner *coordinator.StepRunner,
	steps []*coordinator.Step,
	run contract.Run,
	checkpointer *processCheckpointer,
) {
	err := runner.Execute(ctx, steps)
	checkpointer.Close()
	snapshot, snapshotErr := snapshotReleaseRun(run)
	view := run.TakeView()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processRunState != nil && secureEqual(s.processRunState.Digest, digest) {
		state := s.processRunState.Clone()
		if snapshotErr == nil {
			state.Snapshot = snapshot
		}
		state.Complete, state.Result = processRunOutcome(
			err, snapshotErr, checkpointer.failed.Load(), checkpointer.persisted.Load(),
		)
		if view != nil && !view.UpdatedAt.IsZero() {
			state.UpdatedAt = view.UpdatedAt
		}
		record, saveErr := s.processRunStore.Update(context.Background(), s.processRunRecord, state, view, s.processPlan)
		if saveErr != nil {
			state.Complete = true
			state.Result = resultUncertain
		} else {
			s.processRunRecord = record
			state = record.Run
		}
		s.processRunState = state.Clone()
	}
	s.processRunning = false
	s.processCheckpointer = nil
}

func processRunOutcome(executionErr, snapshotErr error, checkpointFailed, checkpointPersisted bool) (bool, string) {
	switch {
	case checkpointFailed || snapshotErr != nil:
		return true, resultUncertain
	case executionErr == nil:
		return true, resultSucceeded
	case (errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded)) && checkpointPersisted:
		return false, ""
	case errors.Is(executionErr, context.Canceled) || errors.Is(executionErr, context.DeadlineExceeded):
		return true, resultUncertain
	default:
		return true, resultFailed
	}
}

func (s *Server) restoreProcessRunRecord(record *ReleaseRunRecord) error {
	if record == nil || record.Run == nil {
		return errors.New("release run record is empty")
	}
	state := record.Run.Clone()
	registered, ok := s.processes.process(state.ProcessID)
	if !ok {
		return fmt.Errorf("stored process %q is not configured", state.ProcessID)
	}
	run, err := loadReleaseRun(registered.process, state.Snapshot)
	if err != nil {
		return fmt.Errorf("load stored process run: %w", err)
	}
	plan := run.Plan()
	executionContext, cancel := context.WithCancel(s.ctx)
	checkpointer := s.newProcessCheckpointer(state.Digest, run, cancel)
	if state.Checkpointed {
		checkpointer.persisted.Store(true)
	}
	steps, err := run.Build(executionContext, checkpointer.Checkpoint)
	if err != nil {
		checkpointer.Close()
		cancel()
		return fmt.Errorf("reconstruct process graph: %w", err)
	}
	builtSnapshot, err := snapshotReleaseRun(run)
	if err != nil || !reflect.DeepEqual(builtSnapshot, state.Snapshot) {
		checkpointer.Close()
		cancel()
		return errors.New("reconstructing the process graph changed its state")
	}
	if err := validateProcessRunGraph(steps); err != nil {
		checkpointer.Close()
		cancel()
		return fmt.Errorf("restore process graph: %w", err)
	}
	s.processRun = run
	s.processRunState = state
	s.processPlan = plan
	s.processRunRecord = record
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	s.activeProcessID = state.ProcessID
	if state.Started && !state.Complete {
		s.processRunning = true
		s.processCheckpointer = checkpointer
		go s.executeProcessRun(state.Digest, executionContext, s.runner, steps, run, checkpointer)
	} else {
		checkpointer.Close()
		cancel()
	}
	return nil
}

func (s *Server) processRunResponseLocked() processRunResponse {
	return s.processRunResponseLockedWithPlan(s.processPlan)
}

func (s *Server) processRunResponseLockedWithPlan(plan *contract.Plan) processRunResponse {
	if s.processRun == nil || s.processRunState == nil {
		return processRunResponse{}
	}
	state := s.processRunState
	steps := describeSteps(s.steps)
	if state.Complete {
		status := resultSucceeded
		if state.Result != resultSucceeded {
			status = resultFailed
		}
		for index := range steps {
			steps[index].Status = status
		}
	}
	execution := executionResponse{
		Enabled: s.processRunStore != nil, PlanDigest: state.Digest,
		Run: pipelineRun{Complete: state.Complete},
	}
	if s.processRunRecord != nil {
		execution.WorkItem = &workItemReference{ID: s.processRunRecord.WorkItemID, URL: s.processRunRecord.URL}
	}
	if s.processRunStore == nil {
		execution.UnavailableReason = "Release tracking is unavailable."
	}
	return processRunResponse{
		VariantID: state.ProcessID,
		Input:     append(json.RawMessage(nil), state.Snapshot.Input...),
		Steps:     steps, Execution: execution, Plan: plan,
	}
}

func validateProcessRunGraph(steps []*coordinator.Step) error {
	if len(steps) == 0 {
		return errors.New("process graph has no steps")
	}
	seen := make(map[*coordinator.Step]struct{}, len(steps))
	names := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if step == nil || step.Func == nil || strings.TrimSpace(step.Name) == "" || step.Timeout <= 0 {
			return errors.New("process graph contains an incomplete step")
		}
		if _, exists := names[step.Name]; exists {
			return fmt.Errorf("process graph repeats step %q", step.Name)
		}
		dependencies := make(map[*coordinator.Step]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, ok := seen[dependency]; !ok {
				return fmt.Errorf("process step %q depends on a missing or later step", step.Name)
			}
			if _, exists := dependencies[dependency]; exists {
				return fmt.Errorf("process step %q repeats dependency %q", step.Name, dependency.Name)
			}
			dependencies[dependency] = struct{}{}
		}
		seen[step] = struct{}{}
		names[step.Name] = struct{}{}
	}
	return nil
}
