// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/microsoft/go-infra/releaseui/contract"
)

var (
	processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	inputIDPattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

type registeredProcess struct {
	process    contract.Process
	definition contract.Definition
}

type processRegistry struct {
	ordered []registeredProcess
	byID    map[string]registeredProcess
}

func newProcessRegistry(processes ...contract.Process) (*processRegistry, error) {
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

func validateProcessWorkflow(processID string, workflow contract.Workflow) error {
	if strings.TrimSpace(workflow.Heading) == "" {
		return fmt.Errorf("release process %q has an incomplete workflow", processID)
	}
	if strings.TrimSpace(workflow.SubmitLabel) == "" {
		return fmt.Errorf("release process %q has no workflow submit label", processID)
	}
	if len(workflow.Variants) == 0 {
		return fmt.Errorf("release process %q has no variants", processID)
	}
	variants := make(map[string]struct{}, len(workflow.Variants))
	for _, variant := range workflow.Variants {
		if !processIDPattern.MatchString(variant.ID) || strings.TrimSpace(variant.Name) == "" ||
			strings.TrimSpace(variant.Description) == "" {

			return fmt.Errorf("release process %q has an invalid variant %q", processID, variant.ID)
		}
		if _, exists := variants[variant.ID]; exists {
			return fmt.Errorf("release process %q repeats variant %q", processID, variant.ID)
		}
		variants[variant.ID] = struct{}{}
		inputs := make(map[string]struct{}, len(variant.Inputs))
		for _, input := range variant.Inputs {
			if !inputIDPattern.MatchString(input.ID) || input.Type != "number" || strings.TrimSpace(input.Label) == "" {
				return fmt.Errorf("release process %q variant %q has an invalid input %q", processID, variant.ID, input.ID)
			}
			if _, exists := inputs[input.ID]; exists {
				return fmt.Errorf("release process %q variant %q repeats input %q", processID, variant.ID, input.ID)
			}
			inputs[input.ID] = struct{}{}
		}
	}
	return nil
}

func processVariant(definition contract.Definition, variantID string) (contract.Variant, bool) {
	for _, variant := range definition.Workflow.Variants {
		if variant.ID == variantID {
			return variant, true
		}
	}
	return contract.Variant{}, false
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
