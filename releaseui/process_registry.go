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
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

var (
	processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	inputIDPattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

type registeredProcess struct {
	process    contract.Process
	definition contract.ProcessDefinition
	inputs     []webview.Input
	group      *registeredProcessGroup
}

type registeredProcessGroup struct {
	identity  contract.Identity
	processes []*registeredProcess
}

type processRegistry struct {
	groups  []*registeredProcessGroup
	ordered []*registeredProcess
	byID    map[string]*registeredProcess
}

func newProcessRegistry(groups ...contract.ProcessGroup) (*processRegistry, error) {
	if len(groups) == 0 {
		return nil, errors.New("release process registry is empty")
	}
	registry := &processRegistry{byID: make(map[string]*registeredProcess)}
	for _, group := range groups {
		if isNil(group) {
			return nil, errors.New("release process group is nil")
		}
		processes := group.Processes()
		if len(processes) == 0 {
			return nil, errors.New("release process group is empty")
		}
		registeredGroup := &registeredProcessGroup{}
		for _, process := range processes {
			entry, err := registry.register(process)
			if err != nil {
				return nil, err
			}
			entry.group = registeredGroup
			registeredGroup.processes = append(registeredGroup.processes, entry)
		}
		if identityProvider, ok := group.(contract.ProcessGroupIdentity); ok {
			identity := identityProvider.ProcessGroupIdentity()
			if identity == nil {
				return nil, errors.New("release process group returned a nil identity")
			}
			registeredGroup.identity = *identity
		} else {
			registeredGroup.identity = registeredGroup.processes[0].definition.Identity
		}
		if err := validateIdentity("release process group", registeredGroup.identity); err != nil {
			return nil, err
		}
		registry.groups = append(registry.groups, registeredGroup)
	}
	return registry, nil
}

func (r *processRegistry) register(process contract.Process) (*registeredProcess, error) {
	if isNil(process) {
		return nil, errors.New("release process implementation is nil")
	}
	definition := process.Definition()
	if !processIDPattern.MatchString(definition.ID) {
		return nil, fmt.Errorf("invalid release process ID %q", definition.ID)
	}
	if _, exists := r.byID[definition.ID]; exists {
		return nil, fmt.Errorf("duplicate release process ID %q", definition.ID)
	}
	if err := validateIdentity("release process "+definition.ID, definition.Identity); err != nil {
		return nil, err
	}
	if definition.DocumentationURL != "" && !strings.HasPrefix(definition.DocumentationURL, "https://") {
		return nil, fmt.Errorf("release process %q has an invalid documentation URL", definition.ID)
	}
	if definition.Notice != nil && strings.TrimSpace(definition.Notice.Title) == "" {
		return nil, fmt.Errorf("release process %q has a notice without a title", definition.ID)
	}
	inputs := releaseflag.InputSet{}
	result := process.InputForm(&inputs)
	if err := inputs.Validate(); err != nil {
		panic(fmt.Sprintf("release process %q has invalid input declarations: %v", definition.ID, err))
	}
	definitions := inputs.Inputs()
	if result == nil && len(definitions) != 0 {
		panic(fmt.Sprintf("release process %q declares inputs but returns a nil input value", definition.ID))
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, input := range definitions {
		if !inputIDPattern.MatchString(input.ID) || input.Type != "number" || strings.TrimSpace(input.Label) == "" {
			panic(fmt.Sprintf("release process %q has invalid input %q", definition.ID, input.ID))
		}
		if _, exists := seen[input.ID]; exists {
			panic(fmt.Sprintf("release process %q repeats input %q", definition.ID, input.ID))
		}
		seen[input.ID] = struct{}{}
	}
	entry := &registeredProcess{process: process, definition: definition, inputs: definitions}
	r.byID[definition.ID] = entry
	r.ordered = append(r.ordered, entry)
	return entry, nil
}

func validateIdentity(subject string, identity contract.Identity) error {
	if strings.TrimSpace(identity.Name) == "" {
		return fmt.Errorf("%s has no name", subject)
	}
	if identity.DocumentationURL != "" && !strings.HasPrefix(identity.DocumentationURL, "https://") {
		return fmt.Errorf("%s has an invalid documentation URL", subject)
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func processPath(id string) string {
	return "/" + id
}

func (r *processRegistry) page(path string) (string, bool) {
	id := strings.TrimPrefix(path, "/")
	_, ok := r.byID[id]
	return "process.html", ok && path == processPath(id)
}

func (r *processRegistry) process(id string) (*registeredProcess, bool) {
	process, ok := r.byID[id]
	return process, ok
}

func (r *processRegistry) summaries() []processSummary {
	summaries := make([]processSummary, 0, len(r.groups))
	for _, group := range r.groups {
		defaultProcess := group.processes[0]
		summaries = append(summaries, processSummary{
			ID:   defaultProcess.definition.ID,
			Name: group.identity.Name, Mark: group.identity.Mark,
			Description: group.identity.Description, Href: processPath(defaultProcess.definition.ID),
		})
	}
	return summaries
}
