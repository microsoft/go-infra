// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const NoTimeout = time.Duration(0)

// StepControl is a set of utilities that a step implementation can use to interact with the runner.
// StepRunner supplies a non-nil StepControl to every step function.
type StepControl interface {
	// ReportProgress publishes the current progress of the step. It may be called multiple times
	// during the step's execution. The runner propagates this information so releaseui can display
	// live progress and persist a progress view to the tracking workitem backend.
	ReportProgress(StepProgress)

	// Update calls fn synchronously while holding a lock shared by all steps in this run.
	// releaseui snapshots and persists the resulting state before Update returns. If persistence
	// fails, Update returns the error and cancels execution. Return this error before performing
	// any external action that depends on the update.
	//
	// If a step reads another step's completed result, synchronize the read through a direct or
	// transitive dependency on the producing step. Use [Step.DependsOn] or [Step.Then]. Using
	// dependencies rather than read locks lets the race detector expose missing synchronization.
	//
	// The host uses the same lock when reading snapshots and views. The callback must not call
	// Update recursively or wait for another step.
	Update(fn func()) error
}

// StepFunc defines the signature of a function that implements a release step. It receives a
// context for cancellation and a StepControl for reporting progress and other step utilities. It
// should return an error if the step fails.
type StepFunc func(ctx context.Context, ctl StepControl) error

// Step represents a step in the release. Just enough information is represented in Step to allow
// status to be reported, otherwise state is internal to Func.
type Step struct {
	// Name identifies the step and must be unique within the release step graph.
	Name string
	// Timeout defines the deadline that should be set up for the ctx passed to Func.
	// If NoTimeout (zero), no deadline is set.
	Timeout time.Duration
	// Func is the implementation of the step. It is executed when the step is run.
	// It is run in its own goroutine and may block on network calls, retries, etc.
	// It shouldn't wait for another step to complete: this should be done using DependsOn.
	Func StepFunc
	// DependsOn is a list of steps that must all complete before Func is run.
	DependsOn []*Step
}

// NewRootStep creates a new step with the given name, implementation, and no dependencies.
func NewRootStep(name string, timeout time.Duration, f StepFunc) *Step {
	return &Step{
		Name:    name,
		Timeout: timeout,
		Func:    f,
	}
}

// Then creates a new step that depends on s and returns the new step. This can be used when
// defining a step graph to chain a sequence of steps together without as much syntactic clutter.
func (s *Step) Then(name string, timeout time.Duration, f StepFunc, dependsOnAdditional ...*Step) *Step {
	return &Step{
		Name:      name,
		Timeout:   timeout,
		Func:      f,
		DependsOn: append(dependsOnAdditional, s),
	}
}

// TransitiveDependencies returns all the steps s transitively depends on. Returns an error if a
// step is invalid, names are not unique, or a cycle is detected.
//
// The slice is topologically sorted: for each step x in the slice, every step y that x depends on
// precedes x. This means the topologically sorted list would be a valid order to run the steps one
// at a time. However, we expect to run the steps in parallel, so the execution order is not
// predictable in practice. The order may be useful for text representations of the graph, but in
// most use cases it is not relevant.
//
// The result is reproducible for a given slice of steps and their dependency slices.
func (s *Step) TransitiveDependencies() ([]*Step, error) {
	if s == nil {
		return nil, errors.New("cannot traverse dependencies of a nil step")
	}

	type visitState int
	v := make(map[*Step]visitState)
	const (
		// Not yet visited. This func relies on v[s] being 0 when s isn't in the map.
		_ visitState = iota
		// We're visiting this node. If we encounter it again, we found a cycle.
		visiting
		// This node's dependencies have been traversed and it's cycle-free.
		visited
	)

	var sortedSteps []*Step
	stepsByName := make(map[string]*Step)
	var path []*Step

	// visit is a recursive function that explores the transitive dependency graph of a given step.
	// It updates v during the exploration.
	// If there is no cycle, returns nil. If there is a cycle, the error lists the cycle's names.
	var visit func(s *Step) error
	visit = func(s *Step) error {
		if s == nil {
			return errors.New("step graph contains a nil step")
		}
		switch v[s] {
		case visiting:
			cycleStart := 0
			for i, candidate := range path {
				if candidate == s {
					cycleStart = i
					break
				}
			}
			cycle := append(append([]*Step(nil), path[cycleStart:]...), s)
			names := make([]string, len(cycle))
			for i, step := range cycle {
				names[i] = step.Name
			}
			return fmt.Errorf("encountered cycle: %s", strings.Join(names, " <- "))
		case visited:
			return nil
		}
		if s.Name == "" {
			return errors.New("step has an empty name")
		}
		if existing, ok := stepsByName[s.Name]; ok && existing != s {
			return fmt.Errorf("step name %q is not unique", s.Name)
		}
		stepsByName[s.Name] = s
		if s.Func == nil {
			return fmt.Errorf("step %q has no implementation", s.Name)
		}

		v[s] = visiting
		path = append(path, s)
		dependencies := make(map[*Step]struct{}, len(s.DependsOn))
		for i, dependency := range s.DependsOn {
			if dependency == nil {
				return fmt.Errorf("step %q has a nil dependency at index %d", s.Name, i)
			}
			if _, ok := dependencies[dependency]; ok {
				return fmt.Errorf("step %q depends on %q more than once", s.Name, dependency.Name)
			}
			dependencies[dependency] = struct{}{}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		v[s] = visited
		sortedSteps = append(sortedSteps, s)
		return nil
	}
	if err := visit(s); err != nil {
		return nil, err
	}
	return sortedSteps, nil
}

// StepSet lists dependencies for a new step. Order does not constrain execution, but it does affect
// the traversal order returned by [Step.TransitiveDependencies].
type StepSet []*Step

// All returns the given steps as a dependency set. Use All(a, b).Then(...) to create a step that
// depends on both a and b. This is intended to be useful to help with readability alongside chains
// created using c.Then(...).
func All(steps ...*Step) StepSet {
	return steps
}

// Then creates a new step that depends on all steps in s. It copies the dependency list so
// subsequent changes to s do not change the new step's dependencies.
func (s StepSet) Then(name string, timeout time.Duration, f StepFunc) *Step {
	return &Step{
		Name:      name,
		Timeout:   timeout,
		Func:      f,
		DependsOn: slices.Clone(s),
	}
}
