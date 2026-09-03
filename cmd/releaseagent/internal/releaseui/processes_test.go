// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"net/http"
	"testing"
)

func testDurableWorkflow(heading string) ProcessWorkflow {
	return ProcessWorkflow{Heading: heading, SubmitLabel: "Review", DurableAction: true}
}

func TestProcessRegistry(t *testing.T) {
	registry, err := newProcessRegistry(
		ProcessDefinition{
			ID: "one", Name: "One", Mark: "O", Description: "First process",
			DocumentationURL: "https://example.com/docs",
			Workflow:         testDurableWorkflow("Configure one"),
		},
		ProcessDefinition{
			ID: "two", Name: "Two", Mark: "T", Description: "Second process",
			Workflow: testDurableWorkflow("Configure two"),
		},
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
	if process, ok := registry.process("one"); !ok || process.Name != "One" {
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
		name        string
		definitions []ProcessDefinition
	}{
		{name: "empty"},
		{name: "duplicate ID", definitions: []ProcessDefinition{valid, valid}},
		{name: "invalid ID", definitions: []ProcessDefinition{{
			ID: "One", Name: "One", Mark: "O", Description: "First", Workflow: testDurableWorkflow("Configure"),
		}}},
		{name: "missing workflow", definitions: []ProcessDefinition{{
			ID: "one", Name: "One", Mark: "O", Description: "First",
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProcessRegistry(test.definitions...); err == nil {
				t.Fatal("invalid process registry was accepted")
			}
		})
	}
}

func TestProcessRegistryValidatesWorkflowInputs(t *testing.T) {
	handler := func(*Server, http.ResponseWriter, *http.Request) {}
	definition := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Prepare",
			Preflight: handler, GetPlan: handler, Prepare: handler, Start: handler,
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode", Default: "normal",
				Options: []ProcessInputOption{{Value: "normal", Name: "Normal", Description: "Run normally"}},
			}},
		},
	}
	if _, err := newProcessRegistry(definition); err != nil {
		t.Fatal(err)
	}
	definition.Workflow.Inputs = []ProcessInput{{ID: "count", Type: "number", Label: "Count", Default: "many"}}
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("invalid numeric default was accepted")
	}
	definition.Workflow.Inputs = nil
	if _, err := newProcessRegistry(definition); err != nil {
		t.Fatalf("direct confirmed workflow was rejected: %v", err)
	}
	definition.Workflow.Start = nil
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("incomplete custom lifecycle was accepted")
	}
}

func TestProcessRegistryValidatesDurableAction(t *testing.T) {
	handler := func(*Server, http.ResponseWriter, *http.Request) {}
	definition := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process",
		Workflow: ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Review", DurableAction: true,
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode",
				Options: []ProcessInputOption{{Value: "run", Name: "Run", Description: "Run now"}},
			}},
		},
	}
	if _, err := newProcessRegistry(definition); err != nil {
		t.Fatalf("durable action without handlers was rejected: %v", err)
	}
	definition.Workflow.Prepare = handler
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("durable action with a custom lifecycle handler was accepted")
	}
	definition.Workflow.Prepare = nil
	definition.Workflow.SubmitLabel = ""
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("durable action without a submit label was accepted")
	}
}
