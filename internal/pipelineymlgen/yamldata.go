// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v4"
)

// sortedMapKeys returns the keys of m in sorted order for deterministic output.
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// marshalToNode marshals v to YAML and returns the inner node.
func marshalToNode(v any) (*yaml.Node, error) {
	if n, ok := v.(*yaml.Node); ok {
		return cloneNode(n), nil
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(out, &n); err != nil {
		return nil, err
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return n.Content[0], nil
	}
	return &n, nil
}

// templateDataFromNode converts a template data mapping to expression values.
// Structured values remain YAML nodes so yml can preserve their mapping order.
func templateDataFromNode(n *yaml.Node) (map[string]any, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping node, got %v", kindStr(n))
	}

	data := make(map[string]any, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		var key string
		if err := n.Content[i].Decode(&key); err != nil {
			return nil, fmt.Errorf("decoding key at index %d: %w", i/2, err)
		}
		if _, ok := data[key]; ok {
			return nil, fmt.Errorf("duplicate key %q", key)
		}

		valueNode := n.Content[i+1]
		switch valueNode.Kind {
		case yaml.MappingNode, yaml.SequenceNode:
			data[key] = valueNode
		default:
			var value any
			if err := valueNode.Decode(&value); err != nil {
				return nil, fmt.Errorf("decoding value for key %q: %w", key, err)
			}
			data[key] = value
		}
	}
	return data, nil
}
