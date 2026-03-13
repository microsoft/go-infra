// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"fmt"
	"sort"

	"go.yaml.in/yaml/v4"
)

// yamlMapOrderKey is stored in map[string]any values decoded from YAML to
// preserve the original document key order for inlinerange iteration.
// A null-byte prefix makes it impossible to collide with normal YAML keys.
const yamlMapOrderKey = "\x00yk"

// yamlNodeToData recursively converts a yaml.Node to a Go value.
// For mapping nodes it stores the original key order under yamlMapOrderKey
// so that inlinerange can iterate in YAML document order.
func yamlNodeToData(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("document node has %d children, expected 1", len(node.Content))
		}
		return yamlNodeToData(node.Content[0])

	case yaml.MappingNode:
		m := make(map[string]any, len(node.Content)/2+1)
		keys := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			val, err := yamlNodeToData(node.Content[i+1])
			if err != nil {
				return nil, fmt.Errorf("decoding key %q: %w", key, err)
			}
			m[key] = val
			keys = append(keys, key)
		}
		m[yamlMapOrderKey] = keys
		return m, nil

	case yaml.SequenceNode:
		s := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			val, err := yamlNodeToData(child)
			if err != nil {
				return nil, err
			}
			s = append(s, val)
		}
		return s, nil

	default:
		// Scalars, aliases, etc.
		var val any
		if err := node.Decode(&val); err != nil {
			return nil, err
		}
		return val, nil
	}
}

// getOrderedMapKeys returns the keys of m in their YAML-document order if
// the map was decoded with yamlNodeToData; otherwise it returns them in
// sorted order for deterministic output.
// The yamlMapOrderKey sentinel is never included in the result.
func getOrderedMapKeys(m map[string]any) []string {
	if keys, ok := m[yamlMapOrderKey].([]string); ok {
		return keys
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != yamlMapOrderKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// stripSentinel removes the yamlMapOrderKey from all map[string]any values in
// v recursively. Used before marshaling to prevent the internal ordering key
// from appearing in YAML output.
func stripSentinel(v any) any {
	switch v := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for k, val := range v {
			if k != yamlMapOrderKey {
				result[k] = stripSentinel(val)
			}
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, elem := range v {
			result[i] = stripSentinel(elem)
		}
		return result
	default:
		return v
	}
}
