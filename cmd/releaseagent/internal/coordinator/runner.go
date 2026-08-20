// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
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
	errStepPanic     = errors.New("panic while executing step")
	errAlreadyActive = errors.New("step runner is already active")
)

// StepSnapshot is an immutable view of a step's state at a point in time.
type StepSnapshot struct {
	Name       string        `json:"name"`
	Status     StepStatus    `json:"status"`
	DependsOn  []string      `json:"dependsOn,omitempty"`
	Progress   *StepProgress `json:"progress,omitempty"`
	Error      string        `json:"error,omitempty"`
	StartedAt  *time.Time    `json:"startedAt,omitempty"`
	FinishedAt *time.Time    `json:"finishedAt,omitempty"`
}

// StepProgress is optional live detail reported by a running step. Summary and Detail are short
// human-readable strings. Items contains active sub-operations, while Completed and Total can be
// used to render a determinate progress indicator when Total is positive.
type StepProgress struct {
	Summary   string   `json:"summary,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Items     []string `json:"items,omitempty"`
	Completed int      `json:"completed,omitempty"`
	Total     int      `json:"total,omitempty"`
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
	mu sync.RWMutex

	states   map[*Step]*stepState
	ordered  []*stepState
	active   bool
	runError string
	sequence uint64

	nextSubscriberID uint64
	subscribers      map[uint64]chan Snapshot
}

// Execute runs a group of steps, blocking until all are complete.
//
// If any step fails, Execute returns the first error that occurred and cooperatively cancels other
// running steps. A panic is recovered, wrapped with errStepPanic, and treated as an error. The
// steps must be the validated graph returned by Step.TransitiveDependencies.
func (r *StepRunner) Execute(ctx context.Context, steps []*Step) error {
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		return errAlreadyActive
	}
	r.states = make(map[*Step]*stepState, len(steps))
	r.ordered = make([]*stepState, 0, len(steps))
	r.active = true
	r.runError = ""
	for _, step := range steps {
		state := &stepState{
			step:     step,
			status:   StepStatusWaiting,
			complete: make(chan struct{}),
		}
		r.states[step] = state
		r.ordered = append(r.ordered, state)
	}
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
	return r.snapshotLocked()
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
	initial := r.snapshotLocked()
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

func (r *StepRunner) snapshotLocked() Snapshot {
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
			progress := cloneStepProgress(*state.progress)
			stepSnapshot.Progress = &progress
		}
		for i, dependency := range state.step.DependsOn {
			stepSnapshot.DependsOn[i] = dependency.Name
		}
		if state.err != nil {
			stepSnapshot.Error = state.err.Error()
		}
		if !state.startedAt.IsZero() {
			startedAt := state.startedAt
			stepSnapshot.StartedAt = &startedAt
		}
		if !state.finishedAt.IsZero() {
			finishedAt := state.finishedAt
			stepSnapshot.FinishedAt = &finishedAt
		}
		snapshot.Steps = append(snapshot.Steps, stepSnapshot)
	}
	return snapshot
}

func (r *StepRunner) transition(state *stepState, status StepStatus, err error) {
	r.mu.Lock()
	state.status = status
	state.err = err
	now := time.Now().UTC()
	if status == StepStatusRunning {
		state.startedAt = now
	}
	if isTerminalStatus(status) {
		state.finishedAt = now
	}
	r.publishLocked()
	r.mu.Unlock()
}

func (r *StepRunner) reportProgress(state *stepState, progress StepProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state.status != StepStatusRunning || stepProgressEqual(state.progress, progress) {
		return
	}
	cloned := cloneStepProgress(progress)
	state.progress = &cloned
	r.publishLocked()
}

func cloneStepProgress(progress StepProgress) StepProgress {
	progress.Items = append([]string(nil), progress.Items...)
	return progress
}

func stepProgressEqual(current *StepProgress, next StepProgress) bool {
	if current == nil || current.Summary != next.Summary || current.Detail != next.Detail ||
		current.Completed != next.Completed || current.Total != next.Total || len(current.Items) != len(next.Items) {

		return false
	}
	for index := range current.Items {
		if current.Items[index] != next.Items[index] {
			return false
		}
	}
	return true
}

func (r *StepRunner) publishLocked() {
	r.sequence++
	snapshot := r.snapshotLocked()
	for id, updates := range r.subscribers {
		select {
		case updates <- snapshot:
		default:
			close(updates)
			delete(r.subscribers, id)
		}
	}
}

func isTerminalStatus(status StepStatus) bool {
	return status == StepStatusSucceeded || status == StepStatusFailed ||
		status == StepStatusBlocked || status == StepStatusCanceled
}

type stepState struct {
	step *Step

	err        error
	status     StepStatus
	progress   *StepProgress
	startedAt  time.Time
	finishedAt time.Time
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
