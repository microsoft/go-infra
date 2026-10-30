// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package processregistry validates and indexes configured release processes.
package processregistry

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

var (
	processIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	inputIDPattern   = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// Process is one validated process registration.
type Process struct {
	Implementation contract.Process
	Definition     contract.ProcessDefinition
	Inputs         []webview.Input
	Group          *Group
}

// DisplayName combines the registered group and process names for release records.
func (p *Process) DisplayName() string {
	return p.Group.Identity.Name + " \u00b7 " + p.Definition.Name
}

// Group is one validated process group.
type Group struct {
	Identity  contract.Identity
	Processes []*Process
}

// Registry indexes validated release processes and groups.
type Registry struct {
	groups  []*Group
	ordered []*Process
	byID    map[string]*Process
}

// New validates and indexes groups.
func New(groups ...contract.ProcessGroup) (*Registry, error) {
	if len(groups) == 0 {
		return nil, errors.New("release process registry is empty")
	}
	registry := &Registry{byID: make(map[string]*Process)}
	for _, group := range groups {
		if isNil(group) {
			return nil, errors.New("release process group is nil")
		}
		processes := group.Processes()
		if len(processes) == 0 {
			return nil, errors.New("release process group is empty")
		}
		identity := group.Identity()
		if identity == nil {
			return nil, errors.New("release process group returned a nil identity")
		}
		registeredGroup := &Group{Identity: *identity}
		for _, process := range processes {
			entry, err := registry.register(process)
			if err != nil {
				return nil, err
			}
			entry.Group = registeredGroup
			registeredGroup.Processes = append(registeredGroup.Processes, entry)
		}
		if err := validateIdentity("release process group", registeredGroup.Identity); err != nil {
			return nil, err
		}
		registry.groups = append(registry.groups, registeredGroup)
	}
	return registry, nil
}

func (r *Registry) register(process contract.Process) (*Process, error) {
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
	inputs := releaseinput.Set{}
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
	entry := &Process{Implementation: process, Definition: definition, Inputs: definitions}
	r.byID[definition.ID] = entry
	r.ordered = append(r.ordered, entry)
	return entry, nil
}

// Page returns the embedded page name for a process route.
func (r *Registry) Page(path string) (string, bool) {
	id := strings.TrimPrefix(path, "/")
	_, ok := r.byID[id]
	return "process.html", ok && path == Path(id)
}

// Lookup returns a process by ID.
func (r *Registry) Lookup(id string) (*Process, bool) {
	process, ok := r.byID[id]
	return process, ok
}

// Groups returns the registered groups in display order.
func (r *Registry) Groups() []*Group {
	return append([]*Group(nil), r.groups...)
}

// Ordered returns all registered processes in display order.
func (r *Registry) Ordered() []*Process {
	return append([]*Process(nil), r.ordered...)
}

// Path returns the browser path for a process.
func Path(id string) string {
	return "/" + id
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
