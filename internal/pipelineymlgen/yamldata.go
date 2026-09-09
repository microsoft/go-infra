// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v4"
)

type dataIdentity struct {
	typ     reflect.Type
	pointer uintptr
}

type preservedYAMLValue struct {
	// Keep the source value alive so its identity cannot be reused.
	value any
	node  *yaml.Node
}

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

func preserveYAMLValueNodes(value any, node *yaml.Node) (map[dataIdentity]preservedYAMLValue, error) {
	preserved := make(map[dataIdentity]preservedYAMLValue)
	if err := preserveYAMLValueNode(preserved, value, node); err != nil {
		return nil, err
	}
	return preserved, nil
}

func preserveYAMLValueNode(preserved map[dataIdentity]preservedYAMLValue, value any, node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 1 {
			return preserveYAMLValueNode(preserved, value, node.Content[0])
		}
		return nil
	}

	rv := reflect.ValueOf(value)
	identity, ok := identityOf(rv)
	if ok {
		preserved[identity] = preservedYAMLValue{
			value: value,
			node:  node,
		}
	}

	switch node.Kind {
	case yaml.MappingNode:
		if rv.Kind() != reflect.Map {
			return nil
		}
		for i := 0; i < len(node.Content); i += 2 {
			key := reflect.New(rv.Type().Key())
			if err := node.Content[i].Decode(key.Interface()); err != nil {
				return fmt.Errorf("decoding mapping key at index %d: %w", i/2, err)
			}
			mapValue := rv.MapIndex(key.Elem())
			if mapValue.IsValid() {
				if err := preserveYAMLValueNode(preserved, mapValue.Interface(), node.Content[i+1]); err != nil {
					return err
				}
			}
		}
	case yaml.SequenceNode:
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return nil
		}
		for i, child := range node.Content {
			if i >= rv.Len() {
				break
			}
			if err := preserveYAMLValueNode(preserved, rv.Index(i).Interface(), child); err != nil {
				return err
			}
		}
	}
	return nil
}

func identityOf(value reflect.Value) (dataIdentity, bool) {
	switch value.Kind() {
	case reflect.Map:
		if value.IsNil() {
			return dataIdentity{}, false
		}
		return dataIdentity{
			typ:     value.Type(),
			pointer: uintptr(value.UnsafePointer()),
		}, true
	case reflect.Slice:
		if value.IsNil() {
			return dataIdentity{}, false
		}
		return dataIdentity{
			typ:     value.Type(),
			pointer: value.Pointer(),
		}, true
	default:
		return dataIdentity{}, false
	}
}

func (e *EvalState) mergePreservedYAML(preserved map[dataIdentity]preservedYAMLValue) {
	if len(preserved) == 0 {
		return
	}
	cloned := maps.Clone(e.preservedYAML)
	if cloned == nil {
		cloned = make(map[dataIdentity]preservedYAMLValue)
	}
	maps.Copy(cloned, preserved)
	e.preservedYAML = cloned
}

func (e *EvalState) preservedYAMLNode(value any) *yaml.Node {
	identity, ok := identityOf(reflect.ValueOf(value))
	if !ok {
		return nil
	}
	preserved, ok := e.preservedYAML[identity]
	if !ok {
		return nil
	}
	decoded := reflect.New(reflect.TypeOf(value))
	if err := preserved.node.Decode(decoded.Interface()); err != nil ||
		!reflect.DeepEqual(value, decoded.Elem().Interface()) {
		return nil
	}
	return cloneNodeTree(preserved.node)
}

func cloneNodeTree(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	cloned := *node
	cloned.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		cloned.Content[i] = cloneNodeTree(child)
	}
	return &cloned
}
