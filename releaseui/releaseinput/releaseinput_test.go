// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseinput

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSetBindsPositiveInteger(t *testing.T) {
	var buildID int
	inputs := &Set{}
	inputs.PositiveIntVar(&buildID, FieldOptions{
		Label: "Source build ID", Description: "A successful build.", Placeholder: "123",
	})
	definitions := inputs.Inputs()
	if len(definitions) != 1 || definitions[0].ID != "input1" ||
		definitions[0].Type != "number" || definitions[0].Label != "Source build ID" {

		t.Fatalf("definitions = %#v", definitions)
	}
	if err := inputs.Parse(json.RawMessage(`{"input1":"42"}`)); err != nil {
		t.Fatal(err)
	}
	if buildID != 42 {
		t.Fatalf("build ID = %d, want 42", buildID)
	}
}

func TestSetRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "missing", data: `{}`, want: `input "Source build ID" is required`},
		{name: "unknown", data: `{"input1":"42","other":"1"}`, want: `unknown process input "other"`},
		{name: "wrong type", data: `{"input1":42}`, want: `input "Source build ID" must be a string`},
		{name: "zero", data: `{"input1":"0"}`, want: `input "Source build ID" must be a positive integer`},
		{name: "negative", data: `{"input1":"-1"}`, want: `input "Source build ID" must be a positive integer`},
		{name: "trailing", data: `{"input1":"42"}{}`, want: `exactly one JSON value`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buildID int
			inputs := &Set{}
			inputs.PositiveIntVar(&buildID, FieldOptions{Label: "Source build ID"})
			if err := inputs.Parse(json.RawMessage(test.data)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestSetBindsFieldsInRegistrationOrder(t *testing.T) {
	var previousIDs []string
	for range 2 {
		var values [2]int
		var inputs Set
		for index := range values {
			inputs.PositiveIntVar(&values[index], FieldOptions{Label: "Dimensional order"})
		}
		if err := inputs.Validate(); err != nil {
			t.Fatal(err)
		}
		definitions := inputs.Inputs()
		ids := []string{definitions[0].ID, definitions[1].ID}
		if ids[0] == ids[1] || previousIDs != nil && !slices.Equal(ids, previousIDs) {
			t.Fatalf("field IDs are not unique and repeatable: current=%v, previous=%v", ids, previousIDs)
		}
		data := json.RawMessage(fmt.Sprintf(`{"%s":"22","%s":"11"}`, ids[1], ids[0]))
		if err := inputs.Parse(data); err != nil {
			t.Fatal(err)
		}
		if values != [2]int{11, 22} {
			t.Fatalf("fields were bound in the wrong order: %v", values)
		}
		previousIDs = ids
	}
}

func TestSetAcceptsEmptyObjectWithoutFields(t *testing.T) {
	if err := (&Set{}).Parse(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}
