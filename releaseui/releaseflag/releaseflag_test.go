// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseflag

import (
	"encoding/json"
	"testing"
)

func TestInputSetBindsPositiveInteger(t *testing.T) {
	var buildID int
	inputs := &InputSet{}
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
			inputs := &InputSet{}
			inputs.PositiveIntVar(&buildID, "sourceBuildId", FieldOptions{Label: "Source build ID"})
			if err := inputs.Parse(json.RawMessage(test.data)); err == nil {
				t.Fatalf("invalid input %s was accepted", test.data)
			}
		})
	}
}

func TestInputSetAcceptsEmptyObjectWithoutFields(t *testing.T) {
	if err := (&InputSet{}).Parse(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}
