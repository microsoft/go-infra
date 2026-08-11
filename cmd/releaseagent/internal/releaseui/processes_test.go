// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"net/http"
	"testing"
)

func TestProcessRegistry(t *testing.T) {
	registry, err := newProcessRegistry(
		ProcessDefinition{
			ID: "one", Name: "One", Mark: "O", Description: "First process", Status: "Available",
			Available:        true,
			DocumentationURL: "https://example.com/docs",
			Methods: []ProcessMethod{{
				ID: "manual", Name: "Manual", Description: "Run it manually", Steps: []string{"Open it"},
				ActionLabel: "Open", ActionHref: "https://example.com/run",
			}},
		},
		ProcessDefinition{
			ID: "two", Name: "Two", Mark: "T", Description: "Second process", Status: "Future",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page, ok := registry.page("/one"); !ok || page != "process.html" {
		t.Fatalf("page = %q, ok = %v", page, ok)
	}
	if _, ok := registry.page("/two"); ok {
		t.Fatal("unavailable process has a page")
	}
	if process, ok := registry.process("one"); !ok || len(process.Methods) != 1 {
		t.Fatalf("process = %#v, ok = %v", process, ok)
	}
	summaries := registry.summaries()
	if len(summaries) != 2 || summaries[0].Mark != "O" || summaries[1].Available {
		t.Fatalf("summaries = %#v", summaries)
	}
}

func TestProcessRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process", Status: "Available",
		Available: true,
	}
	for _, test := range []struct {
		name        string
		definitions []ProcessDefinition
	}{
		{name: "empty"},
		{name: "duplicate ID", definitions: []ProcessDefinition{valid, valid}},
		{name: "invalid ID", definitions: []ProcessDefinition{{
			ID: "One", Name: "One", Mark: "O", Description: "First", Status: "Future",
		}}},
		{name: "invalid method", definitions: []ProcessDefinition{{
			ID: "one", Name: "One", Mark: "O", Description: "First", Status: "Available",
			Available: true,
			Methods:   []ProcessMethod{{ID: "manual", Name: "Manual"}},
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newProcessRegistry(test.definitions...); err == nil {
				t.Fatal("invalid process registry was accepted")
			}
		})
	}
}

func TestProcessRegistryValidatesWorkflowDependencies(t *testing.T) {
	prepare := func(*Server, http.ResponseWriter, *http.Request) {}
	definition := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process", Status: "Available", Available: true,
		Workflow: &ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Prepare", GetPlan: prepare, Prepare: prepare,
			Inputs: []ProcessInput{{
				ID: "mode", Type: "choice", Label: "Mode", Default: "normal",
				Options: []ProcessInputOption{{Value: "normal", Name: "Normal", Description: "Run normally"}},
			}},
			Steps: []ProcessStep{{Name: "Prepare"}, {Name: "Run", DependsOn: []string{"Prepare"}}},
		},
	}
	if _, err := newProcessRegistry(definition); err != nil {
		t.Fatal(err)
	}
	definition.Workflow.Steps[1].DependsOn = []string{"Missing"}
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("unknown workflow dependency was accepted")
	}
	definition.Workflow.Steps = []ProcessStep{
		{Name: "Prepare", DependsOn: []string{"Run"}},
		{Name: "Run", DependsOn: []string{"Prepare"}},
	}
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("workflow dependency cycle was accepted")
	}
	definition.Workflow.Steps = nil
	definition.Workflow.Inputs = []ProcessInput{{ID: "count", Type: "number", Label: "Count", Default: "many"}}
	if _, err := newProcessRegistry(definition); err == nil {
		t.Fatal("invalid numeric default was accepted")
	}
}

func TestProcessRegistryValidatesDurableAction(t *testing.T) {
	handler := func(*Server, http.ResponseWriter, *http.Request) {}
	definition := ProcessDefinition{
		ID: "one", Name: "One", Mark: "O", Description: "First process", Status: "Available", Available: true,
		Workflow: &ProcessWorkflow{
			Heading: "Configure", SubmitLabel: "Review", DurableAction: true,
			Inputs: []ProcessInput{{ID: "mode", Type: "text", Label: "Mode", Required: true}},
			Steps:  []ProcessStep{{Name: "Run"}},
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
