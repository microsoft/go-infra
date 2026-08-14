// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const NoTimeout = time.Duration(0)

type StepFunc func(ctx context.Context) error

// Step represents a step in the release. Just enough information is represented in Step to allow
// status to be reported, otherwise state is internal to Func.
type Step struct {
	// ID is serialized into durable plans and browser snapshots. It must be unique and stable so
	// changing the user-facing Name doesn't invalidate a resumable release or break UI correlation.
	ID string
	// Name is the human-readable name of the step.
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
func NewRootStep(id, name string, timeout time.Duration, f StepFunc) *Step {
	return &Step{
		ID:      id,
		Name:    name,
		Timeout: timeout,
		Func:    f,
	}
}

// NewStep creates a new step with the given name, implementation, and dependencies. dependsOn must
// contain at least one step or NewStep will panic.
//
// If there are no dependencies, use NewRootStep instead. These funcs are separate to prevent
// accidentally creating a root step by omitting dependencies.
func NewStep(id, name string, timeout time.Duration, f StepFunc, dependsOn ...*Step) *Step {
	if len(dependsOn) < 1 {
		panic("at least one dependency required to create " + name)
	}
	return &Step{
		ID:        id,
		Name:      name,
		Timeout:   timeout,
		Func:      f,
		DependsOn: dependsOn,
	}
}

// NewIndicatorStep creates a new step with the given name and no implementation. An indicator step
// generally helps a release runner understand the step graph more easily by indicating what it
// means for a set of steps to complete, and clarify what other steps take a dependency on.
func NewIndicatorStep(id, name string, dependsOnAdditional ...*Step) *Step {
	return NewStep(
		id,
		name,
		NoTimeout,
		func(context.Context) error { return nil },
		dependsOnAdditional...,
	)
}

// Then creates a new step that depends on s and returns the new step. This can be used when
// defining a step graph to chain a sequence of steps together without as much syntactic clutter.
func (s *Step) Then(id, name string, timeout time.Duration, f StepFunc, dependsOnAdditional ...*Step) *Step {
	return &Step{
		ID:        id,
		Name:      name,
		Timeout:   timeout,
		Func:      f,
		DependsOn: append(dependsOnAdditional, s),
	}
}

// ValidateSteps checks an arbitrary step slice before execution or persistence. Unlike
// TransitiveDependencies, it verifies that every dependency is present in the supplied slice.
func ValidateSteps(steps []*Step) error {
	if len(steps) == 0 {
		return errors.New("step graph is empty")
	}

	stepSet := make(map[*Step]struct{}, len(steps))
	ids := make(map[string]*Step, len(steps))
	for i, step := range steps {
		if step == nil {
			return fmt.Errorf("step at index %d is nil", i)
		}
		if _, ok := stepSet[step]; ok {
			return fmt.Errorf("step %q (%q) appears more than once", step.ID, step.Name)
		}
		stepSet[step] = struct{}{}
		if step.ID == "" {
			return fmt.Errorf("step %q has an empty ID", step.Name)
		}
		if existing, ok := ids[step.ID]; ok {
			return fmt.Errorf("steps %q and %q use duplicate ID %q", existing.Name, step.Name, step.ID)
		}
		ids[step.ID] = step
		if step.Name == "" {
			return fmt.Errorf("step %q has an empty name", step.ID)
		}
		if step.Func == nil {
			return fmt.Errorf("step %q (%q) has no implementation", step.ID, step.Name)
		}
	}

	for _, step := range steps {
		dependencies := make(map[*Step]struct{}, len(step.DependsOn))
		for i, dependency := range step.DependsOn {
			if dependency == nil {
				return fmt.Errorf("step %q has a nil dependency at index %d", step.ID, i)
			}
			if _, ok := stepSet[dependency]; !ok {
				return fmt.Errorf("step %q depends on unknown step %q", step.ID, dependency.ID)
			}
			if _, ok := dependencies[dependency]; ok {
				return fmt.Errorf("step %q depends on step %q more than once", step.ID, dependency.ID)
			}
			dependencies[dependency] = struct{}{}
		}
	}

	type visitState int
	const (
		unvisited visitState = iota
		visiting
		visited
	)
	visits := make(map[*Step]visitState, len(steps))
	var path []*Step
	var visit func(*Step) error
	visit = func(step *Step) error {
		switch visits[step] {
		case visiting:
			cycleStart := 0
			for i, candidate := range path {
				if candidate == step {
					cycleStart = i
					break
				}
			}
			cycle := append(append([]*Step(nil), path[cycleStart:]...), step)
			ids := make([]string, len(cycle))
			for i, cycleStep := range cycle {
				ids[i] = cycleStep.ID
			}
			return fmt.Errorf("encountered cycle: %s", strings.Join(ids, " <- "))
		case visited:
			return nil
		}

		visits[step] = visiting
		path = append(path, step)
		for _, dependency := range step.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		visits[step] = visited
		return nil
	}
	for _, step := range steps {
		if visits[step] == unvisited {
			if err := visit(step); err != nil {
				return err
			}
		}
	}
	return nil
}

// TransitiveDependencies returns all the steps s transitively depends on. Returns an error if a
// cycle is detected.
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

	// visit is a recursive function that explores the transitive dependency graph of a given step.
	// It updates v during the exploration.
	// If there is no cycle, returns nil. If there is a cycle, returns the step names in the cycle.
	var visit func(s *Step) []string
	visit = func(s *Step) []string {
		if s == nil {
			return []string{"<nil>"}
		}
		switch v[s] {
		case visiting:
			// Cycle.
			return []string{s.Name}
		case visited:
			// Already know there's no cycle here, and nothing to visit.
			return nil
		}

		// Mark current step as visiting and check its dependencies.
		v[s] = visiting
		for _, d := range s.DependsOn {
			if cycle := visit(d); cycle != nil {
				return append(cycle, s.Name)
			}
		}
		// Mark current step as visited: we have ruled out cycles.
		v[s] = visited

		// We now know all steps that s transitively depends on are in sortedSteps. So, we can now
		// add s to the end of sortedSteps.
		sortedSteps = append(sortedSteps, s)

		return nil
	}
	if cycle := visit(s); cycle != nil {
		return nil, fmt.Errorf("encountered cycle: %v", strings.Join(cycle, " <- "))
	}
	if err := ValidateSteps(sortedSteps); err != nil {
		return nil, err
	}
	return sortedSteps, nil
}

// CreateMermaidStepFlowchart creates a Mermaid flowchart from the given steps' dependencies.
func CreateMermaidStepFlowchart(steps []*Step) string {
	stepIndex := make(map[*Step]int, len(steps))
	for i, step := range steps {
		stepIndex[step] = i
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, "---\nconfig:\n  layout: elk\n---\n")
	fmt.Fprintf(&sb, "flowchart RL\n")
	for i, step := range steps {
		fmt.Fprintf(&sb, "  %v(%v)", i, step.Name)
		if len(step.DependsOn) != 0 {
			fmt.Fprintf(&sb, " --> ")
			for i, dep := range step.DependsOn {
				if i > 0 {
					fmt.Fprintf(&sb, " & ")
				}
				fmt.Fprintf(&sb, "%v", stepIndex[dep])
			}
		}
		fmt.Fprintf(&sb, "\n")
	}

	return sb.String()
}

// MermaidLiveChartURL returns a URL to view or edit the given Mermaid chart on mermaid.live.
//
// The URL is based on observations about Mermaid Live Editor v11.4.0, not a published API.
func MermaidLiveChartURL(chart string, edit bool) (string, error) {
	// Including more data seems to make the diagram more likely to render without a refresh.
	// Browsers that have already loaded a diagram before seem more likely to be able to
	// immediately render a new diagram. Mermaid Live Editor is a client-side app using local
	// storage, which may be related.
	chartObject := struct {
		Code          string `json:"code"`
		Mermaid       string `json:"mermaid"`
		UpdateDiagram bool   `json:"updateDiagram"`
		AutoSync      bool   `json:"autoSync"`
		Zoom          int    `json:"zoom"`
		EditorMode    string `json:"editorMode"`
		PanZoom       bool   `json:"panZoom"`
	}{
		Code:          chart,
		Mermaid:       "{\n  \"theme\": \"dark\"\n}",
		UpdateDiagram: true,
		AutoSync:      true,
		Zoom:          1,
		EditorMode:    "code",
		PanZoom:       true,
	}
	chartJSON, err := json.Marshal(&chartObject)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	b64 := base64.NewEncoder(base64.StdEncoding, &sb)
	zl, err := zlib.NewWriterLevel(b64, zlib.BestCompression)
	if err != nil {
		return "", err
	}

	if _, err := zl.Write(chartJSON); err != nil {
		return "", err
	}

	if err := zl.Close(); err != nil {
		return "", err
	}
	if err := b64.Close(); err != nil {
		return "", err
	}

	mode := "view"
	if edit {
		mode = "edit"
	}

	return "https://mermaid.live/" + mode + "#pako:" + sb.String(), nil
}
