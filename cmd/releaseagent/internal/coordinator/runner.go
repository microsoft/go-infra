// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"golang.org/x/sync/errgroup"
)

type StepStatus int

const (
	StepStatusWaiting StepStatus = iota
	StepStatusRunning
	StepStatusSucceeded
	StepStatusFailed
)

var errStepPanic = errors.New("panic while executing step")

type StepRunner struct {
	states map[*Step]*stepState
}

// Execute runs a group of steps, blocking until all are complete.
//
// If any step fails, returns the first error that occurred. If a step panics, it is recovered,
// wrapped as a errStepPanic, and treated as an error.
//
// If any step depends on a step that doesn't exist in steps, returns an error without executing.
func (r *StepRunner) Execute(ctx context.Context, steps []*Step) error {
	// Create the run state for each step.
	r.states = make(map[*Step]*stepState, len(steps))
	for _, step := range steps {
		if _, ok := r.states[step]; ok {
			return fmt.Errorf("step %q in provided steps is a duplicate", step.Name)
		}
		r.states[step] = &stepState{
			step:     step,
			status:   StepStatusWaiting,
			complete: make(chan struct{}),
		}
	}

	// Check that all dependencies can be resolved properly before letting anything start (even for
	// an instant.)
	for _, state := range r.states {
		_, err := state.allDependencyStepStates(r.states)
		if err != nil {
			return err
		}
	}

	// Wait for all steps to complete. Use an ErrGroup to attempt to cancel, but note that it's
	// cooperative, and a step may not cancel immediately e.g. if it's in the middle of an
	// operation that can't easily be resumed.
	eg, egCtx := errgroup.WithContext(ctx)
	for _, state := range r.states {
		eg.Go(func() error {
			return state.run(egCtx, r.states)
		})
	}
	return eg.Wait()
}

type stepState struct {
	step *Step

	err    error
	status StepStatus
	// complete is closed when the step is done after err and status are updated.
	complete chan struct{}
}

func (s *stepState) run(ctx context.Context, states map[*Step]*stepState) (err error) {
	defer func() {
		// Capture a panic and return it as an error. The caller wants other steps to have a chance
		// to clean up via context cancellation rather than terminating immediately.
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v; stack:\n%v", errStepPanic, r, string(debug.Stack()))
		}

		// Update status on the way out, for reporting to the release runner.
		if err != nil {
			// Wrap error with the step name for context.
			err = fmt.Errorf("step %q failed: %w", s.step.Name, err)
			s.err = err
			s.status = StepStatusFailed
		} else {
			s.status = StepStatusSucceeded
		}

		// Signal step completion to any steps waiting on this one.
		close(s.complete)
	}()

	if err := s.waitForDependencies(ctx, states); err != nil {
		return err
	}
	s.status = StepStatusRunning

	if s.step.Timeout == NoTimeout {
		return s.step.Func(ctx)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, s.step.Timeout)
	defer cancel()
	return s.step.Func(deadlineCtx)
}

func (s *stepState) allDependencyStepStates(states map[*Step]*stepState) ([]*stepState, error) {
	deps := make([]*stepState, 0, len(s.step.DependsOn))
	for _, dStep := range s.step.DependsOn {
		d, ok := states[dStep]
		if !ok {
			return nil, fmt.Errorf("step %q depends on unknown step %q", s.step.Name, dStep.Name)
		}

		deps = append(deps, d)
	}
	return deps, nil
}

// waitForDependencies waits for all dependencies of the step to complete, or for any dependency to
// return an error.
func (s *stepState) waitForDependencies(ctx context.Context, states map[*Step]*stepState) error {
	// eg with a context has the desired all-success or first-failure behavior.
	eg, egCtx := errgroup.WithContext(ctx)

	dependencies, err := s.allDependencyStepStates(states)
	if err != nil {
		return err
	}

	for _, d := range dependencies {
		eg.Go(func() error {
			// Wait for the dependency to complete, but stop waiting if another dependency fails
			// and therefore the context is canceled.
			return d.done(egCtx)
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
			return fmt.Errorf("step %q completed with error: %w", s.step.Name, s.err)
		}
		return nil
	}
}
