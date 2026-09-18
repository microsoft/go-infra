// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package contract

import (
	"encoding/json"
	"testing"
)

func TestInputSetBindsPositiveInteger(t *testing.T) {
	var buildID int
	inputs := NewInputSet()
	inputs.PositiveIntVar(&buildID, "sourceBuildId", FieldOptions{
		Label: "Source build ID", Description: "A successful build.", Placeholder: "123",
	})
	definitions := inputs.Inputs()
	if len(definitions) != 1 || definitions[0].ID != "sourceBuildId" ||
		definitions[0].Type != "number" || definitions[0].Label != "Source build ID" {

		t.Fatalf("definitions = %#v", definitions)
	}
	if err := inputs.Parse(json.RawMessage(`{"sourceBuildId":"42"}`)); err != nil {
		t.Fatal(err)
	}
	if buildID != 42 {
		t.Fatalf("build ID = %d, want 42", buildID)
	}
}

func TestInputSetRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "missing", data: `{}`},
		{name: "unknown", data: `{"sourceBuildId":"42","other":"1"}`},
		{name: "wrong type", data: `{"sourceBuildId":42}`},
		{name: "zero", data: `{"sourceBuildId":"0"}`},
		{name: "negative", data: `{"sourceBuildId":"-1"}`},
		{name: "trailing", data: `{"sourceBuildId":"42"}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buildID int
			inputs := NewInputSet()
			inputs.PositiveIntVar(&buildID, "sourceBuildId", FieldOptions{Label: "Source build ID"})
			if err := inputs.Parse(json.RawMessage(test.data)); err == nil {
				t.Fatalf("invalid input %s was accepted", test.data)
			}
		})
	}
}

func TestInputSetAcceptsEmptyObjectWithoutFields(t *testing.T) {
	if err := NewInputSet().Parse(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestNewStateCopiesPlanAndBindsDigest(t *testing.T) {
	plan := Plan{
		VariantID: "test",
		Input:     json.RawMessage(`{"mode":"test"}`),
		Payload:   json.RawMessage(`{"value":"fixed"}`),
		View: PlanView{
			IntentTitle: "Run example", ExecutionTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
			Facts:                []PlanFact{{Label: "Version", Value: "1.0"}},
			Request:              &RequestPreview{Title: "Request", Fields: []RequestField{{Name: "mode", Value: "test"}}},
		},
		Target: Reference{ID: "example", URL: "https://example.com/runs", LinkLabel: "Open example runs"},
	}
	state, err := NewState("example", plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Input[2] = 'x'
	plan.View.Facts[0].Value = "changed"
	plan.View.Request.Fields[0].Value = "changed"
	if string(state.Input) != `{"mode":"test"}` || state.View.Facts[0].Value != "1.0" ||
		state.View.Request.Fields[0].Value != "test" {

		t.Fatalf("state changed with source plan: %#v", state)
	}
	clone := state.Clone()
	clone.Payload[2] = 'x'
	clone.View.Facts[0].Value = "changed"
	clone.View.Request.Fields[0].Value = "changed"
	if string(state.Payload) != `{"value":"fixed"}` || state.View.Facts[0].Value != "1.0" ||
		state.View.Request.Fields[0].Value != "test" {

		t.Fatalf("state changed with clone: %#v", state)
	}
	state.VariantID = "changed"
	if err := state.Validate(); err == nil {
		t.Fatal("state with a modified variant unexpectedly passed validation")
	}
}

func TestNewStateRejectsIncompletePlan(t *testing.T) {
	if _, err := NewState("example", Plan{}); err == nil {
		t.Fatal("incomplete plan unexpectedly produced state")
	}
}

func TestNewStateRequiresVariant(t *testing.T) {
	plan := validPlan()
	plan.VariantID = ""
	if _, err := NewState("example", plan); err == nil {
		t.Fatal("plan without a variant unexpectedly produced state")
	}
}

func TestNewStateRequiresExecutionTitle(t *testing.T) {
	plan := validPlan()
	plan.View.ExecutionTitle = ""
	if _, err := NewState("example", plan); err == nil {
		t.Fatal("plan without an execution title unexpectedly produced state")
	}
}

func validPlan() Plan {
	return Plan{
		VariantID: "test",
		Input:     json.RawMessage(`{}`),
		Payload:   json.RawMessage(`{}`),
		View: PlanView{
			IntentTitle: "Run example", ExecutionTitle: "Run example", ExecutionConfirmation: "Confirm example.",
			ExecutionButtonLabel: "Run example",
		},
		Target: Reference{ID: "example", URL: "https://example.com", LinkLabel: "Open example"},
	}
}
