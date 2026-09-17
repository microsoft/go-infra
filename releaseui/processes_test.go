// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func testDurableWorkflow(heading string) ProcessWorkflow {
	return ProcessWorkflow{Heading: heading, SubmitLabel: "Review"}
}

type fakeProcess struct {
	definition ProcessDefinition
	preflight  func(context.Context) (ProcessReadiness, error)
	prepare    func(context.Context, json.RawMessage) (ReleasePlan, error)
	build      func(context.Context, *ReleaseRunState, CheckpointFunc) ([]*coordinator.Step, error)
	validate   func(*ReleaseRunState) error
}

type fakeReleaseRun struct {
	process *fakeProcess
	state   *ReleaseRunState
}

func (p *fakeProcess) Definition() ProcessDefinition {
	return p.definition
}

func (p *fakeProcess) Preflight(ctx context.Context) (ProcessReadiness, error) {
	if p.preflight == nil {
		return ProcessReadiness{}, nil
	}
	return p.preflight(ctx)
}

func (p *fakeProcess) Prepare(ctx context.Context, input json.RawMessage) (ReleaseRun, error) {
	if p.prepare == nil {
		return nil, errors.New("fake process preparation is not configured")
	}
	prepared, err := p.prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	state, err := NewReleaseRunState(p.definition.ID, prepared)
	if err != nil {
		return nil, err
	}
	return p.Restore(state)
}

func (p *fakeProcess) Restore(state *ReleaseRunState) (ReleaseRun, error) {
	if state == nil || state.ProcessID != p.definition.ID {
		return nil, errors.New("fake release run has the wrong process ID")
	}
	if p.validate != nil {
		if err := p.validate(state); err != nil {
			return nil, err
		}
	} else if err := validateProcessRun(state); err != nil {
		return nil, err
	}
	return &fakeReleaseRun{process: p, state: CloneReleaseRunState(state)}, nil
}

func (r *fakeReleaseRun) Snapshot() *ReleaseRunState {
	return CloneReleaseRunState(r.state)
}

func (r *fakeReleaseRun) Steps(
	ctx context.Context,
	checkpoint CheckpointFunc,
) ([]*coordinator.Step, error) {
	if r.process.build == nil {
		return nil, errors.New("fake process graph is not configured")
	}
	return r.process.build(ctx, r.Snapshot(), checkpoint)
}

func TestProcessRegistry(t *testing.T) {
	registry, err := newProcessRegistry(
		&fakeProcess{definition: ProcessDefinition{
			ID: "one", Name: "One", Mark: "O", Description: "First process",
			DocumentationURL: "https://example.com/docs",
			Workflow:         testDurableWorkflow("Configure one"),
		}},
		&fakeProcess{definition: ProcessDefinition{
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
	valid := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: testDurableWorkflow("Configure"),
	}
	for _, test := range []struct {
		name      string
		processes []ReleaseProcess
	}{
		{name: "empty"},
		{name: "nil process", processes: []ReleaseProcess{nil}},
		{name: "duplicate ID", processes: []ReleaseProcess{
			&fakeProcess{definition: valid}, &fakeProcess{definition: valid},
		}},
		{name: "invalid ID", processes: []ReleaseProcess{&fakeProcess{definition: ProcessDefinition{
			ID: "One", Name: "One", Mark: "O", Description: "First", Workflow: testDurableWorkflow("Configure"),
		}}}},
		{name: "missing workflow", processes: []ReleaseProcess{&fakeProcess{definition: ProcessDefinition{
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

func TestProcessRegistryValidatesWorkflowInputs(t *testing.T) {
	definition := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Prepare",
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode", Default: "normal",
				Options: []ProcessInputOption{{Value: "normal", Name: "Normal", Description: "Run normally"}},
			}},
		},
	}
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err != nil {
		t.Fatal(err)
	}
	definition.Workflow.Inputs = []ProcessInput{{ID: "count", Type: "number", Label: "Count", Default: "many"}}
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("invalid numeric default was accepted")
	}
	definition.Workflow.Inputs = nil
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err != nil {
		t.Fatalf("direct confirmed workflow was rejected: %v", err)
	}
	definition.Workflow.SubmitLabel = ""
	if _, err := newProcessRegistry(&fakeProcess{definition: definition}); err == nil {
		t.Fatal("workflow without a submit label was accepted")
	}
}

func TestProcessRegistryValidatesDefinitionMetadata(t *testing.T) {
	definition := ProcessDefinition{
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
	_ ReleaseProcess = (*fakeProcess)(nil)
	_ ReleaseRun     = (*fakeReleaseRun)(nil)
)
