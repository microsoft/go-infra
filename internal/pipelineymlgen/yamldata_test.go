// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestTemplateDataFromNode(t *testing.T) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(`
string: value
integer: 2
boolean: true
nilValue: null
mapping:
  second: 2
  first: 1
sequence:
  - second: 2
    first: 1
`), &document); err != nil {
		t.Fatal(err)
	}
	source := document.Content[0]

	data, err := templateDataFromNode(source)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := data["string"].(string); !ok || got != "value" {
		t.Errorf("string = %#v, want string %q", data["string"], "value")
	}
	if got, ok := data["integer"].(int); !ok || got != 2 {
		t.Errorf("integer = %#v, want int 2", data["integer"])
	}
	if got, ok := data["boolean"].(bool); !ok || !got {
		t.Errorf("boolean = %#v, want bool true", data["boolean"])
	}
	if got, ok := data["nilValue"]; !ok || got != nil {
		t.Errorf("nilValue = %#v, want nil", got)
	}

	for _, test := range []struct {
		key  string
		kind yaml.Kind
	}{
		{"mapping", yaml.MappingNode},
		{"sequence", yaml.SequenceNode},
	} {
		t.Run(test.key, func(t *testing.T) {
			got, ok := data[test.key].(*yaml.Node)
			if !ok {
				t.Fatalf("%s = %T, want *yaml.Node", test.key, data[test.key])
			}
			if got.Kind != test.kind {
				t.Errorf("%s kind = %v, want %v", test.key, got.Kind, test.kind)
			}
		})
	}

	mapping := data["mapping"].(*yaml.Node)
	cloned, err := marshalToNode(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if cloned == mapping {
		t.Fatal("marshalToNode returned the source node without cloning it")
	}
	if got := cloned.Content[0].Value; got != "second" {
		t.Errorf("first mapping key = %q, want %q", got, "second")
	}
	cloned.Content = nil
	if len(mapping.Content) == 0 {
		t.Error("modifying cloned node changed the source node")
	}
}

func TestTemplateDataFromNodeNull(t *testing.T) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte("null\n"), &document); err != nil {
		t.Fatal(err)
	}

	data, err := templateDataFromNode(document.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Errorf("data = %#v, want nil", data)
	}
}

func TestTemplateDataFromNodeRejectsInvalidData(t *testing.T) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte("- not\n- a\n- mapping\n"), &document); err != nil {
		t.Fatal(err)
	}

	_, err := templateDataFromNode(document.Content[0])
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "expected mapping node") {
		t.Fatalf("unexpected error: %v", err)
	}
}
