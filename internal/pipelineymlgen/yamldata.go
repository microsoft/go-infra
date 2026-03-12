// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"fmt"
	"reflect"
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

// anyToYAMLNode converts a Go value to a *yaml.Node.
// For map[string]any values it preserves YAML key order (using the
// yamlMapOrderKey sentinel) and strips the sentinel from the output.
// For other map types with string keys it sorts the keys.
func anyToYAMLNode(v any) (*yaml.Node, error) {
	if v == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}, nil
	}

	// Use reflect so we can handle any map/slice type, not just map[string]any.
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Interface || rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}, nil
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return marshalToNode(v)
		}
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		// Use ordered keys if available.
		var keys []string
		if m, ok := v.(map[string]any); ok {
			keys = getOrderedMapKeys(m)
		} else {
			// For other map[string]V types: sort for determinism.
			rkeys := rv.MapKeys()
			keys = make([]string, 0, len(rkeys))
			for _, k := range rkeys {
				keys = append(keys, k.String())
			}
			sort.Strings(keys)
		}
		for _, k := range keys {
			valNode, err := anyToYAMLNode(rv.MapIndex(reflect.ValueOf(k)).Interface())
			if err != nil {
				return nil, fmt.Errorf("converting map key %q: %w", k, err)
			}
			n.Content = append(n.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				valNode,
			)
		}
		return n, nil

	case reflect.Slice, reflect.Array:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i := range rv.Len() {
			elemNode, err := anyToYAMLNode(rv.Index(i).Interface())
			if err != nil {
				return nil, fmt.Errorf("converting slice index %d: %w", i, err)
			}
			n.Content = append(n.Content, elemNode)
		}
		return n, nil

	default:
		return marshalToNode(v)
	}
}

// marshalToNode marshals v to YAML bytes and returns the resulting scalar node.
func marshalToNode(v any) (*yaml.Node, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(out, &n); err != nil {
		return nil, fmt.Errorf("failed to unmarshal marshaled value: %w", err)
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return n.Content[0], nil
	}
	return &n, nil
}
