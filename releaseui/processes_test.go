// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/releaseflag"
)

type fakeProcess struct {
	definition contract.ProcessDefinition
	preflight  func(context.Context) (error, error)
	prepare    func(context.Context, any) (*contract.StateSnapshot, error)
	build      func(context.Context, *fakeReleaseRun, contract.CheckpointFunc) ([]*coordinator.Step, error)
	plan       *contract.Plan
	planForRun func(*contract.StateSnapshot) *contract.Plan
	view       *contract.RunView
}

type fakeReleaseRun struct {
	mu      sync.RWMutex
	process *fakeProcess
	state   *contract.StateSnapshot
}

func (p *fakeProcess) Processes() []contract.Process {
	return []contract.Process{p}
}

func (p *fakeProcess) Definition() contract.ProcessDefinition {
	return p.definition
}

func (p *fakeProcess) Preflight(ctx context.Context) (error, error) {
	if p.preflight == nil {
		return nil, nil
	}
	return p.preflight(ctx)
}

func (p *fakeProcess) InputForm(*releaseflag.InputSet) any {
	return nil
}

func (p *fakeProcess) Prepare(ctx context.Context, input any) (*contract.StateSnapshot, error) {
	if p.prepare == nil {
		return nil, errors.New("fake process preparation is not configured")
	}
	return p.prepare(ctx, input)
}

func (p *fakeProcess) Load(state *contract.StateSnapshot) (contract.Run, error) {
	if err := validateStateSnapshot(state); err != nil {
		return nil, err
	}
	return &fakeReleaseRun{process: p, state: cloneStateSnapshot(state)}, nil
}

func (r *fakeReleaseRun) TakeSnapshot() *contract.StateSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneStateSnapshot(r.state)
}

func (r *fakeReleaseRun) TakeView() *contract.RunView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.process.view == nil {
		return &contract.RunView{Summary: "Ready"}
	}
	view := *r.process.view
	return &view
}

func (r *fakeReleaseRun) Plan() *contract.Plan {
	if r.process.planForRun != nil {
		return r.process.planForRun(r.TakeSnapshot())
	}
	if r.process.plan == nil {
		return nil
	}
	plan := *r.process.plan
	plan.Facts = append([]contract.PlanFact(nil), plan.Facts...)
	return &plan
}

func (r *fakeReleaseRun) Build(
	ctx context.Context,
	checkpoint contract.CheckpointFunc,
) ([]*coordinator.Step, error) {
	if r.process.build == nil {
		return nil, errors.New("fake process graph is not configured")
	}
	return r.process.build(ctx, r, checkpoint)
}

func (r *fakeReleaseRun) setState(state json.RawMessage) {
	r.mu.Lock()
	r.state.State = append(json.RawMessage(nil), state...)
	r.mu.Unlock()
}

func testProcessDefinition(id string) contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{Name: id, Mark: "EX", Description: "Example process"},
		ID:       id, InputPreamble: "Configure", InputSubmitLabel: "Review",
	}
}

type fakeProcessGroup struct {
	identity  *contract.Identity
	processes []contract.Process
}

func (g *fakeProcessGroup) Processes() []contract.Process {
	return append([]contract.Process(nil), g.processes...)
}

func (g *fakeProcessGroup) ProcessGroupIdentity() *contract.Identity {
	return g.identity
}

func TestProcessRegistry(t *testing.T) {
	one := &fakeProcess{definition: testProcessDefinition("one")}
	two := &fakeProcess{definition: testProcessDefinition("two")}
	registry, err := newProcessRegistry(one, two)
	if err != nil {
		t.Fatal(err)
	}
	if page, ok := registry.page("/one"); !ok || page != "process.html" {
		t.Fatalf("page = %q, ok = %v", page, ok)
	}
	if process, ok := registry.process("two"); !ok || process.definition.Name != "two" {
		t.Fatalf("process = %#v, ok = %v", process, ok)
	}
	if summaries := registry.summaries(); len(summaries) != 2 || summaries[1].Href != "/two" {
		t.Fatalf("summaries = %#v", summaries)
	}
}

func TestProcessRegistryUsesGroupIdentity(t *testing.T) {
	one := &fakeProcess{definition: testProcessDefinition("one")}
	two := &fakeProcess{definition: testProcessDefinition("two")}
	group := &fakeProcessGroup{
		identity:  &contract.Identity{Name: "Examples", Mark: "EG", Description: "Example processes"},
		processes: []contract.Process{one, two},
	}
	registry, err := newProcessRegistry(group)
	if err != nil {
		t.Fatal(err)
	}
	if summaries := registry.summaries(); len(summaries) != 1 ||
		summaries[0].Name != "Examples" || summaries[0].Href != "/one" {

		t.Fatalf("summaries = %#v", summaries)
	}
	if process, _ := registry.process("two"); process.group != registry.groups[0] {
		t.Fatal("process was not associated with its group")
	}
}

func TestProcessRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := &fakeProcess{definition: testProcessDefinition("one")}
	for _, test := range []struct {
		name   string
		groups []contract.ProcessGroup
	}{
		{name: "empty"},
		{name: "nil group", groups: []contract.ProcessGroup{nil}},
		{name: "empty group", groups: []contract.ProcessGroup{&fakeProcessGroup{}}},
		{name: "duplicate ID", groups: []contract.ProcessGroup{valid, valid}},
		{name: "invalid ID", groups: []contract.ProcessGroup{
			&fakeProcess{definition: testProcessDefinition("One")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProcessRegistry(test.groups...); err == nil {
				t.Fatal("invalid process registry was accepted")
			}
		})
	}
}
