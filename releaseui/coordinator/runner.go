// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// StepStatus describes the current execution state of a release step.
type StepStatus string

const (
	StepStatusWaiting   StepStatus = "waiting"
	StepStatusRunning   StepStatus = "running"
	StepStatusSucceeded StepStatus = "succeeded"
	StepStatusFailed    StepStatus = "failed"
	StepStatusBlocked   StepStatus = "blocked"
	StepStatusCanceled  StepStatus = "canceled"
)

var (
	// ErrCanceled reports a confirmed cancellation, not an interrupted local monitor.
	ErrCanceled = errors.New("external operation was canceled")

	errStepPanic     = errors.New("panic while executing step")
	errAlreadyActive = errors.New("step runner is already active")
)

// StepSnapshot is an immutable view of a step's state at a point in time.
type StepSnapshot struct {
	Name      string        `json:"name"`
	Status    StepStatus    `json:"status"`
	DependsOn []string      `json:"dependsOn,omitempty"`
	Progress  *StepProgress `json:"progress,omitempty"`
	Error     string        `json:"error,omitempty"`
	// ObservedAt identifies a fresh read in this viewer, not a saved execution transition.
	ObservedAt       time.Time `json:"observedAt,omitzero"`
	ObservationError string    `json:"observationError,omitempty"`
}

// StepProgress is optional live detail reported by a running step. Completed and Total can render
// a determinate progress indicator when Total is positive. RunURL identifies this step's external
// operation in both saved progress and read-only observations.
type StepProgress struct {
	Summary   string `json:"summary,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Completed int    `json:"completed,omitempty"`
	Total     int    `json:"total,omitempty"`
	RunURL    string `json:"runUrl,omitempty"`
}

type progressReporterKey struct{}

// ReportProgress publishes live detail for the step currently executing with ctx. It is a no-op
// when ctx did not come from a StepRunner, allowing services to report progress without coupling
// their behavior to whether a UI is attached.
func ReportProgress(ctx context.Context, progress StepProgress) {
	if ctx == nil {
		return
	}
	reporter, ok := ctx.Value(progressReporterKey{}).(func(StepProgress))
	if ok {
		reporter(progress)
	}
}

// Snapshot is an immutable view of an entire execution. Sequence increases on every change.
type Snapshot struct {
	Sequence uint64         `json:"sequence"`
	Active   bool           `json:"active"`
	Error    string         `json:"error,omitempty"`
	Steps    []StepSnapshot `json:"steps"`
}

// StepRunner executes a release step graph and publishes race-free snapshots of its state.
type StepRunner struct {
	mu        sync.RWMutex
	observeMu sync.Mutex

	states     map[*Step]*stepState
	ordered    []*stepState
	active     bool
	runError   string
	sequence   uint64
	generation uint64

	nextSubscriberID uint64
	subscribers      map[uint64]chan Snapshot
}

// NewStepRunner constructs a graph without executing it. saved may be empty for a new plan.
// A restored snapshot describes last-saved progress, never an active local executor.
func NewStepRunner(steps []*Step, saved Snapshot) (*StepRunner, error) {
	initial := Snapshot{Steps: make([]StepSnapshot, len(steps))}
	for index, step := range steps {
		if step == nil {
			return nil, errors.New("graph contains a nil step")
		}
		initial.Steps[index] = StepSnapshot{Name: step.Name, Status: StepStatusWaiting}
		for _, dependency := range step.DependsOn {
			if dependency == nil {
				return nil, errors.New("graph contains a nil dependency")
			}
			initial.Steps[index].DependsOn = append(initial.Steps[index].DependsOn, dependency.Name)
		}
	}
	if err := initial.Validate(); err != nil {
		return nil, err
	}
	if len(saved.Steps) != 0 {
		if err := saved.Validate(); err != nil {
			return nil, err
		}
		if err := validateStepsMatch(steps, saved.Steps); err != nil {
			return nil, err
		}
		initial = saved.Clone()
	}
	runner := &StepRunner{sequence: initial.Sequence, runError: initial.Error}
	runner.initializeLocked(steps)
	for index, savedStep := range initial.Steps {
		state := runner.ordered[index]
		state.status, state.progress = savedStep.Status, savedStep.Progress
		if savedStep.Error != "" {
			state.err = errors.New(savedStep.Error)
		}
	}
	return runner, nil
}

// ValidateSteps checks that execution uses the graph already prepared or opened by this runner.
func (r *StepRunner) ValidateSteps(steps []*Step) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.active {
		return errAlreadyActive
	}
	return validateStepsMatch(steps, r.snapshotLocked(false).Steps)
}

// Execute runs a group of steps, blocking until all are complete.
//
// If any step fails, Execute returns the first error that occurred and cooperatively cancels other
// running steps. A panic is recovered, wrapped with errStepPanic, and treated as an error. The
// steps must be the validated graph returned by Step.TransitiveDependencies.
func (r *StepRunner) Execute(ctx context.Context, steps []*Step) error {
	return r.ExecuteWithFinalizer(ctx, steps, nil)
}

// ExecuteWithFinalizer finishes the run only after finalize has recorded its outcome.
// The finalizer runs outside the runner lock, once all steps have returned.
func (r *StepRunner) ExecuteWithFinalizer(ctx context.Context, steps []*Step, finalize func(error) error) error {
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		return errAlreadyActive
	}
	if len(r.ordered) != 0 {
		if err := validateStepsMatch(steps, r.snapshotLocked(false).Steps); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	r.initializeLocked(steps)
	r.active = true
	r.runError = ""
	r.publishLocked()
	r.mu.Unlock()

	// Wait for all steps to complete. Cancellation is cooperative: a step may not stop immediately
	// if it is in the middle of an operation that cannot be resumed safely.
	eg, egCtx := errgroup.WithContext(ctx)
	for _, state := range r.ordered {
		eg.Go(func() error {
			return state.run(egCtx, r)
		})
	}
	err := eg.Wait()
	if finalize != nil {
		err = errors.Join(err, finalize(err))
	}

	r.mu.Lock()
	r.active = false
	if err != nil {
		r.runError = err.Error()
	}
	r.publishLocked()
	r.mu.Unlock()
	return err
}

// Snapshot returns a race-free copy of the current execution state.
func (r *StepRunner) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked(true)
}

// ExecutionSnapshot returns only execution-owned progress, without read-only observations.
// Use it for durable checkpoints, not the viewer's derived display.
func (r *StepRunner) ExecutionSnapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshotLocked(false)
}

// Observe refreshes only the read-only callbacks of an inactive graph. It never executes steps,
// calls their checkpoint callbacks, or changes their saved execution state.
func (r *StepRunner) Observe(ctx context.Context) error {
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	r.mu.RLock()
	if r.active {
		r.mu.RUnlock()
		return errors.New("cannot observe an executing graph")
	}
	generation := r.generation
	states := append([]*stepState(nil), r.ordered...)
	r.mu.RUnlock()
	var failures []error
	for _, state := range states {
		if state.step.Observe == nil {
			continue
		}
		var observation *Observation
		err := ctx.Err()
		if err == nil {
			observation, err = state.step.Observe(ctx)
		}
		if err == nil && observation != nil {
			err = observation.Validate()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		if err == nil {
			err = ctx.Err()
		}
		r.mu.Lock()
		if r.active || r.generation != generation {
			r.mu.Unlock()
			return context.Canceled
		}
		state.observationErr = err
		if err != nil {
			failures = append(failures, fmt.Errorf("observe %s: %w", state.step.Name, err))
		} else if observation != nil {
			value := *observation
			state.observation = &value
			state.observedAt = time.Now().UTC()
		} else {
			state.observation = nil
			state.observedAt = time.Time{}
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active || r.generation != generation {
		return context.Canceled
	}
	r.publishLocked()
	return errors.Join(failures...)
}

// Subscribe atomically returns the current snapshot and subscribes to future snapshots. The
// returned cancel function is safe to call more than once. If a subscriber fails to keep up and
// fills its buffer, its channel is closed so it can reconnect and obtain a fresh full snapshot.
func (r *StepRunner) Subscribe(buffer int) (Snapshot, <-chan Snapshot, func()) {
	if buffer < 1 {
		buffer = 1
	}

	r.mu.Lock()
	if r.subscribers == nil {
		r.subscribers = make(map[uint64]chan Snapshot)
	}
	r.nextSubscriberID++
	id := r.nextSubscriberID
	updates := make(chan Snapshot, buffer)
	r.subscribers[id] = updates
	initial := r.snapshotLocked(true)
	r.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			r.mu.Lock()
			if active, ok := r.subscribers[id]; ok {
				delete(r.subscribers, id)
				close(active)
			}
			r.mu.Unlock()
		})
	}
	return initial, updates, cancel
}

func (r *StepRunner) snapshotLocked(includeObservations bool) Snapshot {
	snapshot := Snapshot{
		Sequence: r.sequence,
		Active:   r.active,
		Error:    r.runError,
		Steps:    make([]StepSnapshot, 0, len(r.ordered)),
	}
	for _, state := range r.ordered {
		stepSnapshot := StepSnapshot{
			Name:      state.step.Name,
			Status:    state.status,
			DependsOn: make([]string, len(state.step.DependsOn)),
		}
		if state.progress != nil {
			progress := *state.progress
			stepSnapshot.Progress = &progress
		}
		for i, dependency := range state.step.DependsOn {
			stepSnapshot.DependsOn[i] = dependency.Name
		}
		if state.err != nil {
			stepSnapshot.Error = state.err.Error()
		}
		if includeObservations && !r.active {
			if state.observation != nil {
				stepSnapshot.Status = state.observation.Status
				progress := state.observation.Progress
				stepSnapshot.Progress = &progress
				stepSnapshot.Error = state.observation.Error
				stepSnapshot.ObservedAt = state.observedAt
			}
			if state.observationErr != nil {
				stepSnapshot.ObservationError = state.observationErr.Error()
			}
		}
		snapshot.Steps = append(snapshot.Steps, stepSnapshot)
	}
	return snapshot
}

func (r *StepRunner) initializeLocked(steps []*Step) {
	r.generation++
	r.states = make(map[*Step]*stepState, len(steps))
	r.ordered = make([]*stepState, 0, len(steps))
	for _, step := range steps {
		state := &stepState{
			step: step, status: StepStatusWaiting, complete: make(chan struct{}),
		}
		r.states[step] = state
		r.ordered = append(r.ordered, state)
	}
}

func (r *StepRunner) transition(state *stepState, status StepStatus, err error) {
	r.mu.Lock()
	state.status = status
	state.err = err
	r.publishLocked()
	r.mu.Unlock()
}

func (r *StepRunner) reportProgress(state *stepState, progress StepProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state.status != StepStatusRunning || stepProgressEqual(state.progress, progress) {
		return
	}
	state.progress = &progress
	r.publishLocked()
}

func (r *StepRunner) publishLocked() {
	r.sequence++
	snapshot := r.snapshotLocked(true)
	for id, updates := range r.subscribers {
		select {
		case updates <- snapshot:
		default:
			close(updates)
			delete(r.subscribers, id)
		}
	}
}

// Clone returns an independent snapshot.
func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.Steps = make([]StepSnapshot, len(s.Steps))
	for index, step := range s.Steps {
		clone.Steps[index] = step
		clone.Steps[index].DependsOn = slices.Clone(step.DependsOn)
		if step.Progress != nil {
			progress := *step.Progress
			clone.Steps[index].Progress = &progress
		}
	}
	return clone
}

// Validate checks the statuses and dependency order of a saved graph.
func (s Snapshot) Validate() error {
	seen := make(map[string]bool, len(s.Steps))
	for _, step := range s.Steps {
		if step.Name == "" || seen[step.Name] {
			return fmt.Errorf("saved graph has an empty or duplicate step name %q", step.Name)
		}
		for _, dependency := range step.DependsOn {
			if !seen[dependency] {
				return fmt.Errorf("saved graph step %q has an unordered or missing dependency %q", step.Name, dependency)
			}
		}
		switch step.Status {
		case StepStatusWaiting, StepStatusRunning, StepStatusSucceeded, StepStatusFailed, StepStatusBlocked, StepStatusCanceled:
		default:
			return fmt.Errorf("saved graph step %q has invalid status %q", step.Name, step.Status)
		}
		if step.Progress != nil {
			if err := validateRunURL(step.Progress.RunURL); err != nil {
				return fmt.Errorf("saved graph step %q progress: %w", step.Name, err)
			}
		}
		seen[step.Name] = true
	}
	return nil
}

func validateStepsMatch(steps []*Step, saved []StepSnapshot) error {
	if len(steps) != len(saved) {
		return errors.New("execution graph does not match the saved graph")
	}
	for index, step := range steps {
		prior := saved[index]
		if step == nil || step.Name != prior.Name || len(step.DependsOn) != len(prior.DependsOn) {
			return errors.New("execution graph does not match the saved graph")
		}
		for dependencyIndex, dependency := range step.DependsOn {
			if dependency == nil || dependency.Name != prior.DependsOn[dependencyIndex] {
				return errors.New("execution graph does not match the saved graph")
			}
		}
	}
	return nil
}

func stepProgressEqual(current *StepProgress, next StepProgress) bool {
	if current == nil || current.Summary != next.Summary || current.Detail != next.Detail ||
		current.Completed != next.Completed || current.Total != next.Total || current.RunURL != next.RunURL {

		return false
	}
	return true
}

type stepState struct {
	step *Step

	err            error
	status         StepStatus
	progress       *StepProgress
	observation    *Observation
	observedAt     time.Time
	observationErr error
	// complete is closed after err and status are updated.
	complete chan struct{}
}

func (s *stepState) run(ctx context.Context, runner *StepRunner) (err error) {
	defer func() {
		// Capture a panic and return it as an error. The caller wants other steps to have a chance
		// to clean up via context cancellation rather than terminating immediately.
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v; stack:\n%v", errStepPanic, recovered, string(debug.Stack()))
		}

		status := StepStatusSucceeded
		if err != nil {
			status = StepStatusFailed
			var dependencyErr *errDependencyFailed
			switch {
			case errors.As(err, &dependencyErr):
				status = StepStatusBlocked
			case errors.Is(err, ErrCanceled):
				status = StepStatusCanceled
			case errors.Is(err, context.Canceled) && ctx.Err() != nil:
				status = StepStatusCanceled
			}
			err = fmt.Errorf("step %q failed: %w", s.step.Name, err)
		}
		runner.transition(s, status, err)
		close(s.complete)
	}()

	if err := s.waitForDependencies(ctx, runner.states); err != nil {
		return err
	}
	runner.transition(s, StepStatusRunning, nil)
	ctx = context.WithValue(ctx, progressReporterKey{}, func(progress StepProgress) {
		runner.reportProgress(s, progress)
	})

	if s.step.Timeout == NoTimeout {
		return s.step.Func(ctx)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, s.step.Timeout)
	defer cancel()
	return s.step.Func(deadlineCtx)
}

// waitForDependencies waits for all dependencies of the step to complete, or for any dependency
// to return an error.
func (s *stepState) waitForDependencies(ctx context.Context, states map[*Step]*stepState) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for _, dependencyStep := range s.step.DependsOn {
		dependency := states[dependencyStep]
		eg.Go(func() error {
			return dependency.done(egCtx)
		})
	}
	return eg.Wait()
}

func (s *stepState) done(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.complete:
		if s.err != nil {
			return &errDependencyFailed{stepName: s.step.Name, err: s.err}
		}
		return nil
	}
}

type errDependencyFailed struct {
	stepName string
	err      error
}

func (e *errDependencyFailed) Error() string {
	return fmt.Sprintf("dependency %q failed: %v", e.stepName, e.err)
}

func (e *errDependencyFailed) Unwrap() error {
	return e.err
}
