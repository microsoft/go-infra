// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package processregistry

import (
	"context"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

type testProcess struct {
	definition contract.ProcessDefinition
}

func (p *testProcess) Definition() contract.ProcessDefinition {
	return p.definition
}

func (*testProcess) Preflight(context.Context) (error, error) {
	return nil, nil
}

func (*testProcess) InputForm(*releaseinput.Set) any {
	return nil
}

func (*testProcess) Prepare(context.Context, any) (*contract.StateSnapshot, error) {
	return nil, nil
}

func (*testProcess) Load(*contract.StateSnapshot) (contract.Run, error) {
	return nil, nil
}

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
