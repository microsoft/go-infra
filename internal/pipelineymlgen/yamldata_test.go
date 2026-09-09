// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"testing"

	"go.yaml.in/yaml/v4"
)

func TestPreservedYAMLNode(t *testing.T) {
	tests := []struct {
		name   string
		source string
		mutate func(any) any
	}{
		{
			name:   "mutated map",
			source: "first: 1\nsecond: 2\n",
			mutate: func(value any) any {
				value.(map[string]any)["third"] = 3
				return value
			},
		},
		{
			name:   "shortened slice",
			source: "- first\n- second\n",
			mutate: func(value any) any {
				return value.([]any)[:1]
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(test.source), &node); err != nil {
				t.Fatal(err)
			}
			var value any
			if err := node.Decode(&value); err != nil {
				t.Fatal(err)
			}
			preserved, err := preserveYAMLValueNodes(value, &node)
			if err != nil {
				t.Fatal(err)
			}
			state := EvalState{preservedYAML: preserved}

			if state.preservedYAMLNode(value) == nil {
				t.Fatal("expected unchanged value to use its preserved YAML node")
			}
			if state.preservedYAMLNode(test.mutate(value)) != nil {
				t.Fatal("expected mutated value not to use its preserved YAML node")
			}
		})
	}
}
