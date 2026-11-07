// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processregistry

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

type testProcess struct {
	definition contract.ProcessDefinition
	inputForm  func(*releaseinput.Set) any
}

func (p *testProcess) Definition() contract.ProcessDefinition {
	return p.definition
}

func (*testProcess) Preflight(context.Context) (error, error) {
	return nil, nil
}

func (p *testProcess) InputForm(inputs *releaseinput.Set) any {
	if p.inputForm != nil {
		return p.inputForm(inputs)
	}
	return nil
}

func (*testProcess) Prepare(context.Context, any) (*contract.StateSnapshot, error) {
	return nil, nil
}

func (*testProcess) Load(*contract.StateSnapshot) (contract.Run, error) {
	return nil, nil
}

func (*testProcess) Validate(*contract.StateSnapshot) error { return nil }

type testGroup struct {
	identity  *contract.Identity
	processes []contract.Process
}

func (g *testGroup) Identity() *contract.Identity {
	return g.identity
}

func (g *testGroup) Processes() []contract.Process {
	return append([]contract.Process(nil), g.processes...)
}

func newTestProcess(id string) *testProcess {
	return &testProcess{definition: contract.ProcessDefinition{
		ID: id, Identity: contract.Identity{Name: id},
	}}
}

func newTestGroup(processes ...contract.Process) *testGroup {
	return &testGroup{
		identity:  &contract.Identity{Name: "Examples"},
		processes: append([]contract.Process(nil), processes...),
	}
}

func TestRegistryIndexesProcessesAndGroups(t *testing.T) {
	one := newTestProcess("one")
	two := newTestProcess("two")
	registry, err := New(newTestGroup(one, two))
	if err != nil {
		t.Fatal(err)
	}
	if page, ok := registry.Page("/one"); !ok || page != "process.html" {
		t.Fatalf("page = %q, ok = %v", page, ok)
	}
	process, ok := registry.Lookup("two")
	if !ok || process.Definition.Name != "two" {
		t.Fatalf("process = %#v, ok = %v", process, ok)
	}
	if name := process.DisplayName(); name != "Examples \u00b7 two" {
		t.Fatalf("display name = %q", name)
	}
	groups := registry.Groups()
	if len(groups) != 1 || process.Group != groups[0] || groups[0].Processes[0].Definition.ID != "one" {
		t.Fatalf("groups = %#v", groups)
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := newTestProcess("one")
	for _, test := range []struct {
		name   string
		groups []contract.ProcessGroup
	}{
		{name: "empty"},
		{name: "nil group", groups: []contract.ProcessGroup{nil}},
		{name: "empty group", groups: []contract.ProcessGroup{newTestGroup()}},
		{name: "empty group identity", groups: []contract.ProcessGroup{
			&testGroup{processes: []contract.Process{valid}},
		}},
		{name: "duplicate ID", groups: []contract.ProcessGroup{
			newTestGroup(valid), newTestGroup(valid),
		}},
		{name: "invalid ID", groups: []contract.ProcessGroup{
			newTestGroup(newTestProcess("One")),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.groups...); err == nil {
				t.Fatal("invalid process registry was accepted")
			}

		})
	}
}

func TestRegistryExplainsInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name      string
		label     string
		nilTarget bool
		cause     string
	}{
		{
			name: "nil target", label: "Dimensional order", nilTarget: true,
			cause: `input "Dimensional order" has a nil target`,
		},
		{
			name: "empty label", label: " ",
			cause: `input 1 has an empty label`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := newTestProcess("example")
			process.inputForm = func(inputs *releaseinput.Set) any {
				value := new(int)
				if test.nilTarget {
					value = nil
				}
				inputs.PositiveIntVar(value, releaseinput.FieldOptions{Label: test.label})
				return value
			}
			defer func() {
				message, ok := recover().(string)
				if !ok || !strings.Contains(message, `release process "example"`) ||
					!strings.Contains(message, test.cause) {
					t.Fatalf("declaration panic = %q, want process and cause %q", message, test.cause)
				}
			}()
			_, _ = New(newTestGroup(process))
		})
	}
}
