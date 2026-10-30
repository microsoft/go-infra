// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package processrun manages the lifecycle and durable state of configured release runs.
package processrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/localserver"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

const (
	resultRunning   = string(runstore.StatusRunning)
	resultSucceeded = string(runstore.StatusSucceeded)
	resultFailed    = string(runstore.StatusFailed)
	resultCanceled  = string(runstore.StatusCanceled)
	resultUncertain = string(runstore.StatusUncertain)
)

// processSession owns the mutable state for one configured release process.
// Process variants have distinct IDs, so each variant can be planned and run independently.
type processSession struct {
	selectionMu sync.Mutex
	mu          sync.Mutex

	steps        []*coordinator.Step
	runner       *coordinator.StepRunner
	run          contract.Run
	state        *runstore.State
	plan         *contract.Plan
	record       *runstore.Record
	running      bool
	checkpointer *processCheckpointer
}

type processRunUpdate struct {
	state          *runstore.State
	expectedRecord *runstore.Record
	plan           *contract.Plan
}

type processCheckpointer struct {
	manager   *Manager
	session   *processSession
	digest    string
	run       contract.Run
	cancel    context.CancelFunc
	failed    atomic.Bool
	persisted atomic.Bool
	requests  chan chan struct{}
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	failureMu sync.Mutex
	failure   error
}

// ErrorKind identifies the operation category of a process-run failure.
type ErrorKind int

const (
	// ErrorNotFound means the requested process is not configured.
	ErrorNotFound ErrorKind = iota + 1
	// ErrorInvalid means the caller supplied invalid input.
	ErrorInvalid
	// ErrorConflict means the requested operation conflicts with the current run state.
	ErrorConflict
	// ErrorPrecondition means the process is not ready for the requested operation.
	ErrorPrecondition
	// ErrorInternal means the process implementation returned inconsistent state.
	ErrorInternal
	// ErrorStore means durable release state could not be read or written.
	ErrorStore
)

// Error describes a process-run failure that a transport can map to its own error response.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

// Manager owns the independent runtime session for each configured release process.
type Manager struct {
	ctx       context.Context
	processes *processregistry.Registry
	store     runstore.Store
	sessions  map[string]*processSession
}

// New creates a process-run manager.
func New(ctx context.Context, processes *processregistry.Registry, store runstore.Store) *Manager {
	return &Manager{
		ctx: ctx, processes: processes, store: store, sessions: newProcessSessions(processes),
	}
}

// History returns the durable release history associated with the manager.
func (s *Manager) History() *History {
	return &History{manager: s}
}

func (s *Manager) processSession(processID string) (*processSession, bool) {
	session, ok := s.sessions[processID]
	return session, ok
}

// Active returns the runner currently associated with processID and whether it has a prepared plan.
func (s *Manager) Active(processID string) (*coordinator.StepRunner, bool, bool) {
	session, ok := s.processSession(processID)
	if !ok {
		return nil, false, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.runner, len(session.steps) != 0, true
}

// Running reports whether processID is currently executing a release.
func (s *Manager) Running(processID string) bool {
	session, ok := s.processSession(processID)
	if !ok {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.running
}

// Preflight checks whether processID is ready to plan and execute.
func (s *Manager) Preflight(ctx context.Context, processID string) (webview.PreflightReport, bool) {
	report := webview.PreflightReport{PlanningEnabled: true, Checks: []webview.PreflightCheck{{
		ID: "loopback-server", Name: "Loopback-only HTTP server", Status: webview.CheckStatusPassed,
		Details: "The release UI accepts requests only through a local loopback address.",
	}}}
	registered, ok := s.processes.Lookup(processID)
	if !ok {
		return webview.PreflightReport{}, false
	}
	report.Checks = append(report.Checks, webview.PreflightCheck{
		ID: "release-tracking", Name: "Release tracking", Status: webview.CheckStatusPassed,
		Details: "Confirmed release state is stored in a revisioned record; unconfirmed plans remain in memory.",
	})
	warning, blocking := registered.Implementation.Preflight(ctx)
	report.ExternalExecutionEnabled = blocking == nil
	switch {
	case blocking != nil:
		report.PlanningEnabled = false
		report.Checks = append(report.Checks, webview.PreflightCheck{
			ID: processID + "-readiness", Name: registered.Definition.Name + " readiness",
			Status: webview.CheckStatusUnavailable, Details: blocking.Error(),
		})
	case warning != nil:
		report.Checks = append(report.Checks, webview.PreflightCheck{
			ID: processID + "-readiness", Name: registered.Definition.Name + " readiness",
			Status: webview.CheckStatusWarning, Details: warning.Error(),
		})
	default:
		report.Checks = append(report.Checks, webview.PreflightCheck{
			ID: processID + "-readiness", Name: registered.Definition.Name + " readiness",
			Status: webview.CheckStatusPassed, Details: "Ready.",
		})
	}
	return report, true
}

// Current returns the prepared or running release for processID.
func (s *Manager) Current(processID string) (webview.ProcessRunResponse[*contract.Plan], bool) {
	session, ok := s.processSession(processID)
	if !ok {
		return webview.ProcessRunResponse[*contract.Plan]{}, false
	}
	response, run, found := session.captureCurrent()
	if !found {
		return webview.ProcessRunResponse[*contract.Plan]{}, false
	}
	if view := run.TakeView(); view != nil {
		response.Execution.RunURL = view.RunURL
	}
	return response, true
}

// Prepare validates input and builds the release plan for processID.
func (s *Manager) Prepare(
	ctx context.Context,
	processID string,
	encoded json.RawMessage,
) (webview.ProcessRunResponse[*contract.Plan], error) {
	registered, ok := s.processes.Lookup(processID)
	if !ok {
		return webview.ProcessRunResponse[*contract.Plan]{},
			newError(ErrorNotFound, errors.New("release process is not configured"))
	}
	if _, blocking := registered.Implementation.Preflight(ctx); blocking != nil {
		return webview.ProcessRunResponse[*contract.Plan]{}, newError(ErrorPrecondition, blocking)
	}
	inputs := &releaseinput.Set{}
	input := registered.Implementation.InputForm(inputs)
	if err := inputs.Validate(); err != nil {
		panic(fmt.Sprintf("release process %q has invalid input declarations: %v", processID, err))
	}
	if err := inputs.Parse(encoded); err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{}, newError(ErrorInvalid, err)
	}

	if s.Running(processID) {
		return webview.ProcessRunResponse[*contract.Plan]{},
			newError(ErrorConflict, errors.New("cannot replace the plan while a workflow is running"))
	}

	snapshot, err := registered.Implementation.Prepare(ctx, input)
	if err != nil {
		kind := ErrorConflict
		if errors.Is(err, contract.ErrInvalidInput) {
			kind = ErrorInvalid
		}
		return webview.ProcessRunResponse[*contract.Plan]{}, newError(kind, err)
	}
	run, err := loadReleaseRun(registered.Implementation, snapshot)
	if err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{},
			errorf(ErrorInternal, "load prepared release run: %v", err)
	}
	preparedSnapshot := runstore.CloneSnapshot(snapshot)
	plan := run.Plan()
	steps, err := run.Build(ctx, nil)
	if err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{},
			errorf(ErrorInternal, "build process graph: %v", err)
	}
	if err := validateProcessRunGraph(steps); err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{},
			errorf(ErrorInternal, "validate process graph: %v", err)
	}
	snapshot, err = snapshotReleaseRun(run)
	if err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{}, newError(ErrorInternal, err)
	}
	if !reflect.DeepEqual(snapshot, preparedSnapshot) {
		return webview.ProcessRunResponse[*contract.Plan]{},
			newError(ErrorInternal, errors.New("Plan or Build changed the prepared release state"))
	}
	state, err := runstore.NewState(processID, snapshot, time.Now().UTC())
	if err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{},
			errorf(ErrorInternal, "create release run state: %v", err)
	}

	session, _ := s.processSession(processID)
	result, err := session.publishPreparedRun(run, state, plan, steps)
	if err != nil {
		return webview.ProcessRunResponse[*contract.Plan]{}, newError(ErrorConflict, err)
	}
	return result, nil
}

func (s *Manager) Start(ctx context.Context, processID string, start webview.ReleaseStartRequest) error {
	registered, ok := s.processes.Lookup(processID)
	if !ok {
		return newError(ErrorNotFound, errors.New("release process is not configured"))
	}
	session, _ := s.processSession(processID)
	state, err := session.reserveStart(start)
	if err != nil {
		return err
	}

	_, blocking := registered.Implementation.Preflight(ctx)
	if blocking != nil {
		session.stopStart(state.Digest)
		return errorf(ErrorPrecondition, "external action preflight failed before mutation: %v", blocking)
	}
	run, err := loadReleaseRun(registered.Implementation, state.Snapshot)
	if err != nil {
		session.stopStart(state.Digest)
		return errorf(ErrorInternal, "load process run for execution: %v", err)
	}
	plan := run.Plan()
	executionContext, cancel := context.WithCancel(s.ctx)
	checkpointer := s.newProcessCheckpointer(session, state.Digest, run, cancel)
	steps, err := run.Build(executionContext, checkpointer.Checkpoint)
	if err != nil {
		checkpointer.Close()
		cancel()
		session.stopStart(state.Digest)
		return errorf(ErrorConflict, "build process graph: %v", err)
	}
	builtSnapshot, err := snapshotReleaseRun(run)
	if err != nil || !reflect.DeepEqual(builtSnapshot, state.Snapshot) {
		checkpointer.Close()
		cancel()
		session.stopStart(state.Digest)
		return newError(ErrorConflict, errors.New("building the execution graph changed the reviewed release state"))
	}
	if err := validateProcessRunGraph(steps); err != nil {
		checkpointer.Close()
		cancel()
		session.stopStart(state.Digest)
		return newError(ErrorConflict, errors.New("execution graph no longer matches the reviewed plan"))
	}
	state.Started = true
	state.UpdatedAt = run.TakeView().UpdatedAt
	record, err := s.store.Create(ctx, registered.DisplayName(), state, run.TakeView(), plan)
	if err != nil {
		checkpointer.Close()
		cancel()
		session.stopStart(state.Digest)
		return errorf(ErrorInternal, "create release record before mutation: %v", err)
	}
	runner, err := session.publishStartedRun(run, state, record, plan, steps, checkpointer)
	if err != nil {
		checkpointer.Close()
		cancel()
		session.stopStart(state.Digest)
		return newError(ErrorConflict, err)
	}

	go s.executeProcessRun(session, state.Digest, executionContext, runner, steps, run, checkpointer)
	return nil
}

func (s *Manager) newProcessCheckpointer(
	session *processSession,
	digest string,
	run contract.Run,
	cancel context.CancelFunc,
) *processCheckpointer {
	checkpointer := &processCheckpointer{
		manager: s, session: session, digest: digest, run: run, cancel: cancel,
		requests: make(chan chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go checkpointer.runLoop()
	return checkpointer
}

func (s *Manager) executeProcessRun(
	session *processSession,
	digest string,
	ctx context.Context,
	runner *coordinator.StepRunner,
	steps []*coordinator.Step,
	run contract.Run,
	checkpointer *processCheckpointer,
) {
	defer checkpointer.cancel()
	if err := runner.ExecuteWithFinalizer(ctx, steps, func(executionErr error) error {
		return s.finishProcessRun(session, digest, run, checkpointer, executionErr)
	}); err != nil {
		log.Printf("Release run finished: %v", err)
	}
}

func (s *Manager) finishProcessRun(
	session *processSession,
	digest string,
	run contract.Run,
	checkpointer *processCheckpointer,
	executionErr error,
) error {
	checkpointer.Close()
	snapshot, snapshotErr := snapshotReleaseRun(run)
	view := run.TakeView()
	update, captureErr := session.captureRunUpdate(digest)
	if captureErr != nil {
		return fmt.Errorf("complete release run: %w", captureErr)
	}
	state := update.state
	if snapshotErr == nil {
		state.Snapshot = snapshot
	}
	state.Complete, state.Result = processRunOutcome(
		executionErr, snapshotErr, checkpointer.failed.Load(), checkpointer.persisted.Load(),
	)
	if view != nil && !view.UpdatedAt.IsZero() {
		state.UpdatedAt = view.UpdatedAt
	}
	record, saveErr := s.store.Update(context.Background(), update.expectedRecord, state, view, update.plan)
	if saveErr != nil {
		saveErr = fmt.Errorf("persist release outcome: %w", saveErr)
	}
	publishErr := session.publishOutcome(update, record, saveErr)
	if publishErr != nil {
		publishErr = fmt.Errorf("publish release outcome: %w", publishErr)
	}
	return errors.Join(checkpointer.failureError(), snapshotErr, saveErr, publishErr)
}

func (s *Manager) restoreProcessRunRecord(record *runstore.Record) error {
	if err := validateRecordForExecution(record); err != nil {
		return err
	}
	state := record.Run.Clone()
	registered, ok := s.processes.Lookup(state.ProcessID)
	if !ok {
		return fmt.Errorf("stored process %q is not configured", state.ProcessID)
	}
	run, err := loadReleaseRun(registered.Implementation, state.Snapshot)
	if err != nil {
		return fmt.Errorf("load stored process run: %w", err)
	}
	plan := run.Plan()
	executionContext, cancel := context.WithCancel(s.ctx)
	session, _ := s.processSession(state.ProcessID)
	checkpointer := s.newProcessCheckpointer(session, state.Digest, run, cancel)
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
	runner, running, err := session.publishRestoredRun(run, state, record, plan, steps, checkpointer)
	if err != nil {
		checkpointer.Close()
		cancel()
		return err
	}
	if running {
		go s.executeProcessRun(session, state.Digest, executionContext, runner, steps, run, checkpointer)
	} else {
		checkpointer.Close()
		cancel()
	}
	return nil
}

func (s *processSession) hasSelectedOrPreparedReleaseLocked() bool {
	return s.running || len(s.steps) != 0 || s.run != nil
}

func (s *processSession) selectedReleaseHrefLocked(id int) (string, bool) {
	if s.record != nil && s.record.ID == id {
		return processregistry.Path(s.record.Run.ProcessID), true
	}
	return "", false
}

func (s *processSession) captureCurrent() (webview.ProcessRunResponse[*contract.Plan], contract.Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.state == nil {
		return webview.ProcessRunResponse[*contract.Plan]{}, nil, false
	}
	return s.responseLocked(), s.run, true
}

func (s *processSession) responseLocked() webview.ProcessRunResponse[*contract.Plan] {
	if s.run == nil || s.state == nil {
		return webview.ProcessRunResponse[*contract.Plan]{}
	}
	state := s.state
	steps := describeSteps(s.steps)
	if state.Complete {
		status := resultSucceeded
		if state.Result != resultSucceeded {
			status = resultFailed
		}
		if state.Result == resultCanceled {
			status = resultCanceled
		}
		for index := range steps {
			steps[index].Status = status
		}
	}
	execution := webview.ExecutionResponse{
		PlanDigest: state.Digest,
		Run:        webview.PipelineRun{Complete: state.Complete, Result: state.Result},
	}
	if s.record != nil {
		execution.Record = &webview.ReleaseRecordReference{ID: s.record.ID, URL: s.record.URL}
	}
	return webview.ProcessRunResponse[*contract.Plan]{
		VariantID: state.ProcessID,
		Input:     append(json.RawMessage(nil), state.Snapshot.Input...),
		Steps:     steps, Execution: execution, Plan: s.plan,
	}
}

func (s *processSession) publishPreparedRun(
	run contract.Run,
	state *runstore.State,
	plan *contract.Plan,
	steps []*coordinator.Step,
) (webview.ProcessRunResponse[*contract.Plan], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return webview.ProcessRunResponse[*contract.Plan]{}, errors.New("cannot replace the plan while a workflow is running")
	}
	s.run = run
	s.state = state
	s.plan = plan
	s.record = nil
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	return s.responseLocked(), nil
}

// Start confirms and starts the prepared release for processID.
func (s *processSession) reserveStart(start webview.ReleaseStartRequest) (*runstore.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.state == nil {
		return nil, newError(ErrorConflict, errors.New("review this external action first"))
	}
	if !localserver.Equal(start.PlanDigest, s.state.Digest) {
		return nil, newError(ErrorConflict, errors.New("run request does not match the reviewed external action"))
	}
	if !start.Confirmed {
		return nil, newError(ErrorInvalid, errors.New("confirm the external action before starting it"))
	}
	if s.state.Started || s.running {
		return nil, newError(ErrorConflict, errors.New("the reviewed external action has already started"))
	}
	state := s.state.Clone()
	s.running = true
	return state, nil
}

func (s *processSession) publishStartedRun(
	run contract.Run,
	state *runstore.State,
	record *runstore.Record,
	plan *contract.Plan,
	steps []*coordinator.Step,
	checkpointer *processCheckpointer,
) (*coordinator.StepRunner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.state.Started || !localserver.Equal(s.state.Digest, state.Digest) {
		return nil, errors.New("reviewed external action changed before start")
	}
	s.run = run
	s.state = record.Run.Clone()
	s.plan = plan
	s.record = record
	s.steps = steps
	s.runner = &coordinator.StepRunner{}
	s.checkpointer = checkpointer
	return s.runner, nil
}

func (s *processSession) stopStart(digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != nil && localserver.Equal(s.state.Digest, digest) && !s.state.Started {
		s.running = false
	}
}

func (s *processSession) captureRunUpdate(digest string) (*processRunUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.record == nil || !localserver.Equal(s.state.Digest, digest) {
		return nil, fmt.Errorf("%w: release run changed before persistence", runstore.ErrConflict)
	}
	return &processRunUpdate{
		state: s.state.Clone(), expectedRecord: s.record, plan: s.plan,
	}, nil
}

func (s *processSession) isRunningRecord(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.record != nil && s.record.ID == id
}

func (s *processSession) publishCheckpoint(update *processRunUpdate, record *runstore.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateProcessRunUpdateLocked(s, update); err != nil {
		return err
	}
	s.state = record.Run.Clone()
	s.record = record
	return nil
}

func (s *processSession) publishOutcome(
	update *processRunUpdate,
	record *runstore.Record,
	saveErr error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateProcessRunUpdateLocked(s, update); err != nil {
		return err
	}
	state := update.state
	if saveErr != nil {
		state.Complete = true
		state.Result = resultUncertain
	} else {
		s.record = record
		state = record.Run
	}
	s.state = state.Clone()
	s.running = false
	s.checkpointer = nil
	return nil
}

func (s *processSession) publishRestoredRun(
	run contract.Run,
	state *runstore.State,
	record *runstore.Record,
	plan *contract.Plan,
	steps []*coordinator.Step,
	checkpointer *processCheckpointer,
) (*coordinator.StepRunner, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasSelectedOrPreparedReleaseLocked() {
		return nil, false, errors.New("a release was prepared while the record was restoring")
	}
	runner := &coordinator.StepRunner{}
	running := state.Started && !state.Complete
	s.run = run
	s.state = state
	s.plan = plan
	s.record = record
	s.steps = steps
	s.runner = runner
	s.running = running
	if running {
		s.checkpointer = checkpointer
	}
	return runner, running, nil
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
		c.fail(snapshotErr)
		return false
	}
	update, err := c.session.captureRunUpdate(c.digest)
	if err != nil {
		c.fail(err)
		return false
	}
	state := update.state
	state.Snapshot = snapshot
	state.Checkpointed = true
	if view != nil && !view.UpdatedAt.IsZero() {
		state.UpdatedAt = view.UpdatedAt
	}
	record, err := c.manager.store.Update(
		c.manager.ctx, update.expectedRecord, state, view, update.plan,
	)
	if err != nil {
		c.fail(err)
		return false
	}
	if err := c.session.publishCheckpoint(update, record); err != nil {
		c.fail(err)
		return false
	}
	c.persisted.Store(true)
	return true
}

func (c *processCheckpointer) fail(err error) {
	log.Printf("Release checkpoint failed: %v", err)
	c.failureMu.Lock()
	c.failure = err
	c.failureMu.Unlock()
	c.failed.Store(true)
	c.cancel()
}

func (c *processCheckpointer) failureError() error {
	c.failureMu.Lock()
	defer c.failureMu.Unlock()
	return c.failure
}

func newError(kind ErrorKind, err error) error {
	return &Error{Kind: kind, Err: err}
}

func errorf(kind ErrorKind, format string, args ...any) error {
	return newError(kind, fmt.Errorf(format, args...))
}

func newProcessSessions(processes *processregistry.Registry) map[string]*processSession {
	ordered := processes.Ordered()
	sessions := make(map[string]*processSession, len(ordered))
	for _, process := range ordered {
		sessions[process.Definition.ID] = &processSession{runner: &coordinator.StepRunner{}}
	}
	return sessions
}

func snapshotReleaseRun(run contract.Run) (*contract.StateSnapshot, error) {
	if run == nil {
		return nil, errors.New("release run is nil")
	}
	snapshot := run.TakeSnapshot()
	if err := runstore.ValidateSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("validate release run snapshot: %w", err)
	}
	return runstore.CloneSnapshot(snapshot), nil
}

func loadReleaseRun(process contract.Process, snapshot *contract.StateSnapshot) (contract.Run, error) {
	if err := runstore.ValidateSnapshot(snapshot); err != nil {
		return nil, err
	}
	run, err := process.Load(runstore.CloneSnapshot(snapshot))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("process loaded a nil run")
	}
	return run, nil
}

func validateRecordForExecution(record *runstore.Record) error {
	if err := runstore.ValidateRecord(record); err != nil {
		return err
	}
	if record.Closed && !record.Run.Complete {
		return errors.New("closed release run record cannot be resumed")
	}
	return nil
}

func validateProcessRunUpdateLocked(session *processSession, update *processRunUpdate) error {
	if session.state == nil || session.record == nil ||
		!localserver.Equal(session.state.Digest, update.state.Digest) ||
		session.record.ID != update.expectedRecord.ID || session.record.Revision != update.expectedRecord.Revision {

		return fmt.Errorf("%w: release run changed during persistence", runstore.ErrConflict)
	}
	return nil
}

func processRunOutcome(executionErr, snapshotErr error, checkpointFailed, checkpointPersisted bool) (bool, string) {
	switch {
	case checkpointFailed || snapshotErr != nil:
		return true, resultUncertain
	case errors.Is(executionErr, coordinator.ErrCanceled):
		return true, resultCanceled
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

func describeSteps(steps []*coordinator.Step) []webview.PlanStep {
	descriptions := make([]webview.PlanStep, 0, len(steps))
	for _, step := range steps {
		description := webview.PlanStep{
			Name: step.Name, DependsOn: make([]string, len(step.DependsOn)), Status: "waiting",
		}
		for i, dependency := range step.DependsOn {
			description.DependsOn[i] = dependency.Name
		}
		descriptions = append(descriptions, description)
	}
	return descriptions
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
