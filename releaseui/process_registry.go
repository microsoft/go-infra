// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

var (
	processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	inputIDPattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// ReleaseProcess describes one release type and creates or restores its runs.
type ReleaseProcess interface {
	Definition() ProcessDefinition
	Preflight(context.Context) (ProcessReadiness, error)
	Prepare(context.Context, json.RawMessage) (ReleaseRun, error)
	Restore(*ReleaseRunState) (ReleaseRun, error)
}

// ReleaseRun owns one release's validated state and executable step graph.
type ReleaseRun interface {
	// Snapshot returns an independent copy suitable for persistence or shared lifecycle updates.
	Snapshot() *ReleaseRunState
	Steps(context.Context, CheckpointFunc) ([]*coordinator.Step, error)
}

// ProcessReadiness reports which non-mutating process operations are currently available.
type ProcessReadiness struct {
	PlanningEnabled  bool
	ExecutionEnabled bool
	Details          string
}

// ProcessDefinition describes one release process served at /{ID} through the shared process page.
type ProcessDefinition struct {
	ID               string
	Name             string
	Mark             string
	Description      string
	DocumentationURL string
	Workflow         ProcessWorkflow
}

// ProcessWorkflow describes the form and capabilities rendered by the shared process page.
type ProcessWorkflow struct {
	Heading     string         `json:"heading"`
	Description string         `json:"description,omitempty"`
	SubmitLabel string         `json:"submitLabel,omitempty"`
	Inputs      []ProcessInput `json:"inputs,omitempty"`
	CanSimulate bool           `json:"canSimulate"`
}

// ProcessInput describes one control rendered by the shared process page.
type ProcessInput struct {
	ID          string               `json:"id"`
	Type        string               `json:"type"`
	Label       string               `json:"label"`
	Description string               `json:"description,omitempty"`
	Default     string               `json:"default,omitempty"`
	Placeholder string               `json:"placeholder,omitempty"`
	Options     []ProcessInputOption `json:"options,omitempty"`
	VisibleWhen *ProcessCondition    `json:"visibleWhen,omitempty"`
}

// ProcessInputOption describes one choice in a choice input.
type ProcessInputOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Mark        string `json:"mark,omitempty"`
	Description string `json:"description"`
	NoticeTitle string `json:"noticeTitle,omitempty"`
	Notice      string `json:"notice,omitempty"`
}

// ProcessCondition conditionally displays an input based on another input's value.
type ProcessCondition struct {
	InputID string `json:"inputId"`
	Equals  string `json:"equals"`
}

type registeredProcess struct {
	process    ReleaseProcess
	definition ProcessDefinition
}

type processRegistry struct {
	ordered []registeredProcess
	byID    map[string]registeredProcess
}

func newProcessRegistry(processes ...ReleaseProcess) (*processRegistry, error) {
	if len(processes) == 0 {
		return nil, errors.New("release process registry is empty")
	}
	registry := &processRegistry{
		ordered: make([]registeredProcess, 0, len(processes)),
		byID:    make(map[string]registeredProcess, len(processes)),
	}
	for _, process := range processes {
		if process == nil || reflect.ValueOf(process).Kind() == reflect.Pointer && reflect.ValueOf(process).IsNil() {
			return nil, errors.New("release process implementation is nil")
		}
		definition := process.Definition()
		if !processIDPattern.MatchString(definition.ID) {
			return nil, fmt.Errorf("invalid release process ID %q", definition.ID)
		}
		if _, exists := registry.byID[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate release process ID %q", definition.ID)
		}
		if strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Mark) == "" ||
			strings.TrimSpace(definition.Description) == "" {

			return nil, fmt.Errorf("release process %q has incomplete catalog metadata", definition.ID)
		}
		if definition.DocumentationURL != "" && !strings.HasPrefix(definition.DocumentationURL, "https://") {
			return nil, fmt.Errorf("release process %q has an invalid documentation URL", definition.ID)
		}
		if err := validateProcessWorkflow(definition.ID, definition.Workflow); err != nil {
			return nil, err
		}
		entry := registeredProcess{process: process, definition: definition}
		registry.byID[definition.ID] = entry
		registry.ordered = append(registry.ordered, entry)
	}
	return registry, nil
}

func validateProcessWorkflow(processID string, workflow ProcessWorkflow) error {
	if strings.TrimSpace(workflow.Heading) == "" {
		return fmt.Errorf("release process %q has an incomplete workflow", processID)
	}
	if strings.TrimSpace(workflow.SubmitLabel) == "" {
		return fmt.Errorf("release process %q has no workflow submit label", processID)
	}
	inputs := make(map[string]ProcessInput, len(workflow.Inputs))
	for _, input := range workflow.Inputs {
		if !inputIDPattern.MatchString(input.ID) || strings.TrimSpace(input.Label) == "" ||
			input.Type != "choice" && input.Type != "number" {

			return fmt.Errorf("release process %q has an invalid input %q", processID, input.ID)
		}
		if _, exists := inputs[input.ID]; exists {
			return fmt.Errorf("release process %q repeats input %q", processID, input.ID)
		}
		if input.Type == "choice" && len(input.Options) == 0 || input.Type != "choice" && len(input.Options) != 0 {
			return fmt.Errorf("release process %q input %q has invalid options", processID, input.ID)
		}
		optionValues := make(map[string]struct{}, len(input.Options))
		for _, option := range input.Options {
			if strings.TrimSpace(option.Value) == "" || strings.TrimSpace(option.Name) == "" || strings.TrimSpace(option.Description) == "" {
				return fmt.Errorf("release process %q input %q has an invalid option", processID, input.ID)
			}
			if _, exists := optionValues[option.Value]; exists {
				return fmt.Errorf("release process %q input %q repeats option %q", processID, input.ID, option.Value)
			}
			optionValues[option.Value] = struct{}{}
		}
		if input.Default != "" && input.Type == "choice" {
			if _, exists := optionValues[input.Default]; !exists {
				return fmt.Errorf("release process %q input %q has an invalid default", processID, input.ID)
			}
		}
		if input.Default != "" && input.Type == "number" {
			value, err := strconv.ParseUint(input.Default, 10, 64)
			if err != nil || value == 0 {
				return fmt.Errorf("release process %q input %q has an invalid default", processID, input.ID)
			}
		}
		inputs[input.ID] = input
	}
	for _, input := range workflow.Inputs {
		if input.VisibleWhen == nil {
			continue
		}
		controlling, exists := inputs[input.VisibleWhen.InputID]
		if !exists || controlling.Type != "choice" {
			return fmt.Errorf("release process %q input %q has an invalid condition", processID, input.ID)
		}
		matched := false
		for _, option := range controlling.Options {
			matched = matched || option.Value == input.VisibleWhen.Equals
		}
		if !matched {
			return fmt.Errorf("release process %q input %q has an invalid condition value", processID, input.ID)
		}
	}
	return nil
}

func processPath(id string) string {
	return "/" + id
}

func (r *processRegistry) page(path string) (string, bool) {
	id := strings.TrimPrefix(path, "/")
	_, ok := r.byID[id]
	ok = ok && path == processPath(id)
	return "process.html", ok
}

func (r *processRegistry) process(id string) (registeredProcess, bool) {
	process, ok := r.byID[id]
	return process, ok
}

func (r *processRegistry) summaries() []processSummary {
	summaries := make([]processSummary, 0, len(r.ordered))
	for _, process := range r.ordered {
		definition := process.definition
		summaries = append(summaries, processSummary{
			ID: definition.ID, Name: definition.Name, Mark: definition.Mark,
			Description: definition.Description, Href: processPath(definition.ID),
		})
	}
	return summaries
}
