// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func testDurableWorkflow(heading string) contract.Workflow {
	return contract.Workflow{
		Heading: heading, SubmitLabel: "Review",
		Variants: []contract.Variant{{ID: "default", Name: "Default", Description: "Default process"}},
	}
}

type fakeProcess struct {
	definition contract.Definition
	preflight  func(context.Context) (contract.Readiness, error)
	prepare    func(context.Context, json.RawMessage) (contract.Plan, error)
	build      func(context.Context, *contract.State, contract.CheckpointFunc) ([]*coordinator.Step, error)
	validate   func(*contract.State) error
}

type fakeReleaseRun struct {
	process *fakeProcess
	state   *contract.State
}

func (p *fakeProcess) Definition() contract.Definition {
	return p.definition
}

func (p *fakeProcess) Preflight(ctx context.Context) (contract.Readiness, error) {
	if p.preflight == nil {
		return contract.Readiness{}, nil
	}
	return p.preflight(ctx)
}

func (p *fakeProcess) Prepare(ctx context.Context, selection contract.Selection) (contract.Run, error) {
	if p.prepare == nil {
		return nil, errors.New("fake process preparation is not configured")
	}
	prepared, err := p.prepare(ctx, selection.Input)
	if err != nil {
		return nil, err
	}
	prepared.VariantID = selection.VariantID
	state, err := contract.NewState(p.definition.ID, prepared)
	if err != nil {
		return nil, err
	}
	return p.Restore(state)
}

func (p *fakeProcess) Restore(state *contract.State) (contract.Run, error) {
	if state == nil || state.ProcessID != p.definition.ID {
		return nil, errors.New("fake release run has the wrong process ID")
	}
	if p.validate != nil {
		if err := p.validate(state); err != nil {
			return nil, err
		}
	} else if err := state.Validate(); err != nil {
		return nil, err
	}
	return &fakeReleaseRun{process: p, state: state.Clone()}, nil
}

func (r *fakeReleaseRun) Snapshot() *contract.State {
	return r.state.Clone()
}

func (r *fakeReleaseRun) Steps(
	ctx context.Context,
	checkpoint contract.CheckpointFunc,
) ([]*coordinator.Step, error) {
	if r.process.build == nil {
		return nil, errors.New("fake process graph is not configured")
	}
	return r.process.build(ctx, r.Snapshot(), checkpoint)
}

func TestProcessRegistry(t *testing.T) {
	registry, err := newProcessRegistry(
		&fakeProcess{definition: contract.Definition{
			ID: "one", Name: "One", Mark: "O", Description: "First process",
			DocumentationURL: "https://example.com/docs",
			Workflow:         testDurableWorkflow("Configure one"),
		}},
		&fakeProcess{definition: contract.Definition{
			ID: "two", Name: "Two", Mark: "T", Description: "Second process",
			Workflow: testDurableWorkflow("Configure two"),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page, ok := registry.page("/one"); !ok || page != "process.html" {
		t.Fatalf("page = %q, ok = %v", page, ok)
	}
	if page, ok := registry.page("/two"); !ok || page != "process.html" {
		t.Fatalf("page = %q, ok = %v", page, ok)
	}
	if process, ok := registry.process("one"); !ok || process.definition.Name != "One" {
		t.Fatalf("process = %#v, ok = %v", process, ok)
	}
	summaries := registry.summaries()
	if len(summaries) != 2 || summaries[0].Mark != "O" || summaries[1].Href != "/two" {
		t.Fatalf("summaries = %#v", summaries)
	}
}

func TestProcessRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := contract.Definition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: testDurableWorkflow("Configure"),
	}
	for _, test := range []struct {
		name      string
		processes []contract.Process
	}{
		{name: "empty"},
		{name: "nil process", processes: []contract.Process{nil}},
		{name: "duplicate ID", processes: []contract.Process{
			&fakeProcess{definition: valid}, &fakeProcess{definition: valid},
		}},
		{name: "invalid ID", processes: []contract.Process{&fakeProcess{definition: contract.Definition{
			ID: "One", Name: "One", Mark: "O", Description: "First", Workflow: testDurableWorkflow("Configure"),
		}}}},
		{name: "missing workflow", processes: []contract.Process{&fakeProcess{definition: contract.Definition{
			ID: "one", Name: "One", Mark: "O", Description: "First",
		}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProcessRegistry(test.processes...); err == nil {
				t.Fatal("invalid process registry was accepted")
			}
		})
	}
}

func TestProcessRegistryValidatesWorkflowVariants(t *testing.T) {
	definition := contract.Definition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: contract.Workflow{
			Heading: "Configure", SubmitLabel: "Prepare",
			Variants: []contract.Variant{{
				ID: "normal", Name: "Normal", Description: "Run normally",
				Inputs: []contract.Input{{ID: "count", Type: "number", Label: "Count"}},
			}},
		},
	}
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err != nil {
		t.Fatal(err)
	}
	definition.Workflow.Variants[0].Inputs[0].Type = "text"
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("invalid input type was accepted")
	}
	definition.Workflow.Variants[0].Inputs[0].Type = "number"
	definition.Workflow.Variants = append(definition.Workflow.Variants, definition.Workflow.Variants[0])
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("duplicate variant was accepted")
	}
	definition.Workflow.Variants = nil
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("workflow without variants was accepted")
	}
	definition.Workflow.Variants = []contract.Variant{{ID: "normal", Name: "Normal", Description: "Run normally"}}
	definition.Workflow.SubmitLabel = ""
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("workflow without a submit label was accepted")
	}
}

func TestProcessRegistryValidatesDefinitionMetadata(t *testing.T) {
	definition := contract.Definition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		DocumentationURL: "https://example.com/docs",
		Workflow:         testDurableWorkflow("Configure"),
	}
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err != nil {
		t.Fatalf("valid process was rejected: %v", err)
	}
	definition.DocumentationURL = "http://example.com/docs"
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("insecure documentation URL was accepted")
	}
	definition.DocumentationURL = "https://example.com/docs"
	definition.Description = ""
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("incomplete catalog metadata was accepted")
	}
}

var (
	_ contract.Process = (*fakeProcess)(nil)
	_ contract.Run     = (*fakeReleaseRun)(nil)
)
