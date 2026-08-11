// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeProcessInputs(t *testing.T) {
	inputs := []ProcessInput{
		{
			ID: "mode", Type: "choice", Label: "Mode", Default: "normal", Required: true,
			Options: []ProcessInputOption{
				{Value: "normal", Name: "Normal", Description: "Normal mode"},
				{Value: "rollback", Name: "Rollback", Description: "Rollback mode"},
			},
		},
		{
			ID: "build", Type: "number", Label: "Build", Required: true,
			VisibleWhen: &ProcessCondition{InputID: "mode", Equals: "rollback"},
		},
		{ID: "note", Type: "text", Label: "Note"},
	}
	for _, test := range []struct {
		name  string
		input string
		want  map[string]string
	}{
		{name: "defaults and trims", input: `{"note":"  hello  "}`, want: map[string]string{"mode": "normal", "note": "hello"}},
		{name: "conditional number", input: `{"mode":"rollback","build":"42"}`, want: map[string]string{"mode": "rollback", "build": "42"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotJSON, err := normalizeProcessInputs(inputs, json.RawMessage(test.input))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(gotJSON, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("inputs = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeProcessInputsRejectsSchemaViolations(t *testing.T) {
	inputs := []ProcessInput{
		{
			ID: "mode", Type: "choice", Label: "Mode", Required: true,
			Options: []ProcessInputOption{{Value: "normal", Name: "Normal", Description: "Normal mode"}},
		},
		{
			ID: "build", Type: "number", Label: "Build", Required: true,
			VisibleWhen: &ProcessCondition{InputID: "mode", Equals: "normal"},
		},
	}
	for _, input := range []string{
		`{}`,
		`{"mode":"other","build":"42"}`,
		`{"mode":"normal"}`,
		`{"mode":"normal","build":"NaN"}`,
		`{"mode":"normal","build":42}`,
		`{"mode":"normal","build":"42","extra":"value"}`,
		`{"mode":"other","build":"42"}`,
	} {
		if _, err := normalizeProcessInputs(inputs, json.RawMessage(input)); err == nil {
			t.Fatalf("input %s was accepted", input)
		}
	}
	hidden := []ProcessInput{
		{
			ID: "mode", Type: "choice", Label: "Mode", Default: "normal", Required: true,
			Options: []ProcessInputOption{
				{Value: "normal", Name: "Normal", Description: "Normal mode"},
				{Value: "rollback", Name: "Rollback", Description: "Rollback mode"},
			},
		},
		{ID: "build", Type: "number", Label: "Build", VisibleWhen: &ProcessCondition{InputID: "mode", Equals: "rollback"}},
	}
	if _, err := normalizeProcessInputs(hidden, json.RawMessage(`{"mode":"normal","build":"42"}`)); err == nil {
		t.Fatal("hidden input was accepted")
	}
}
