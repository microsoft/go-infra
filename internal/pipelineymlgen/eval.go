// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

// EvalState holds the generator's template evaluation state. It's copied and
// extended when evaluating a new doc or when Data changes.
type EvalState struct {
	// File path being evaluated.
	File string

	// Data map for template evaluation.
	Data map[string]any
}

// EvalFile is a helper to run EvalFileConfig and evalFileWithConfig in one.
func (e *EvalState) EvalFile() (*yaml.Node, error) {
	docs, err := readYAMLFileDocs(e.File)
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file %s: %w", e.File, err)
	}

	config, content, err := e.EvalFileConfig(docs)
	if err != nil {
		return nil, err
	}

	node, err := e.evalFileWithConfig(config, content)
	if err != nil {
		return nil, err
	}
	return node, nil
}

// EvalFileConfig evaluates the first document in the given docs from a YAML
// file. Determines if the first document is a configuration doc, and based on
// that, returns the configuration data (which may be the zero value) and the
// content doc.
func (e *EvalState) EvalFileConfig(docs []*yaml.Node) (*ConfigurationDoc, *yaml.Node, error) {
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("no docs in file %s", e.File)
	}

	var configDoc ConfigurationDoc

	result, err := e.eval(docs[0])
	if err != nil {
		return nil, nil, fmt.Errorf("failed to evaluate first doc in file %#q: %w", e.File, err)
	}

	if n, ok := result.(*yaml.Node); ok {
		if err := n.Decode(&configDoc); err != nil || configDoc.Config == nil {
			// Not config doc. Return first doc as the content with blank config.
			return &configDoc, docs[0], nil
		}
		if len(docs) == 1 {
			return nil, nil, fmt.Errorf("file %s with configuration doc doesn't have a second document", e.File)
		}
		return &configDoc, docs[1], nil
	} else {
		return nil, nil, fmt.Errorf("file %s first doc did not evaluate to a YAML node, got %#v", e.File, result)
	}
}

// evalFileWithConfig evaluates the given content document from a YAML file with
// the given config.
func (e *EvalState) evalFileWithConfig(config *ConfigurationDoc, content *yaml.Node) (*yaml.Node, error) {
	innerState := *e
	innerState.File = e.File
	innerState.MergeData(map[string]any{"filename": filepath.Base(e.File)})
	if config != nil && config.Config != nil && config.Config.Data != nil {
		innerState.MergeData(config.Config.Data)
	}

	result, err := innerState.eval(content)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate %#q content (with config): %w", e.File, err)
	}
	node, ok := result.(*yaml.Node)
	if !ok {
		return nil, fmt.Errorf("file %s did not evaluate to a YAML node", e.File)
	}
	return node, nil
}

// eval evaluates template functions in the given YAML node given the state in
// e. The orig node (and any children) are not modified. May return a
// *yaml.Node, a pointer to any struct beginning with "eval" in this package,
// or nil to indicate a node entirely dissolved. Or, an error.
//
// Always returns a *yaml.Node when given a DocumentNode. This should always be
// possible because the doc is fully evaluated at that point.
//
// When descending into the tree, looks ahead in some cases to identify how to
// treat some nodes. In particular, if/elseif/else chains in sequences have
// something like "distant siblings" that need to interact with each other.
//
// When ascending, the "eval*" structs are evaluated based on context.
func (e *EvalState) eval(orig *yaml.Node) (any, error) {
	// Helper to wrap errors for more diag info about where they happened.
	// Use newlines and tabs in this func to help readability.
	fail := func(err error) (any, error) {
		return nil, fmt.Errorf("\n\teval %q at %s:%d:%d failed: %w", kindStr(orig), e.File, orig.Line, orig.Column, err)
	}

	switch orig.Kind {

	case yaml.DocumentNode:
		if len(orig.Content) > 1 {
			return fail(fmt.Errorf("document node contains multiple content nodes"))
		}
		n := cloneNode(orig)
		if len(orig.Content) == 0 {
			return n, nil
		}

		result, err := e.eval(orig.Content[0])
		if err != nil {
			return fail(fmt.Errorf("evaluating document content: %w", err))
		}

		resultNode, err := e.resultToYAML(result)
		if err != nil {
			return fail(fmt.Errorf("converting doc content result to YAML node: %w", err))
		}
		n.Content[0] = resultNode

		return n, nil

	case yaml.MappingNode:
		if len(orig.Content)%2 != 0 {
			return fail(fmt.Errorf("mapping node has odd number of content nodes"))
		}

		// A mapping always needs contextual evaluation. It might contain a
		// conditional chain, but it also could be part of a sequence's
		// conditional chain!
		//
		// For example, this is valid:
		//
		// - ${ inlineif ... }: x
		// - ${ inlineelse }: y
		//
		// If we tried to evaluate the conditional chain while all we have is
		// the mapping, we would think that the "${ inlineelse }: y" must be
		// invalid. We also need to be able to determine that this isn't valid:
		//
		// - ${ inlineif ... }: x
		//   ${ inlineelseif ... }: y
		// - ${ inlineelse }: z

		m := evalMapping{
			orig:    orig,
			content: make([]any, 0, len(orig.Content)/2),
		}
		for i := 0; i < len(orig.Content); i += 2 {
			keyNode := orig.Content[i]
			valueNode := orig.Content[i+1]

			evalKey, err := e.eval(keyNode)
			if err != nil {
				return fail(fmt.Errorf("evaluating mapping key: %w", err))
			}

			switch evalKey := evalKey.(type) {
			case *yaml.Node, *evalIf, *evalElseIf, *evalElse:
				// We need to defer evaluating the keys and values to support
				// short-circuiting. In that case, evalKey would be an *evalIf,
				// etc., and here we explicitly make a func to get value later.
				m.content = append(m.content, &evalPair{
					orig: keyNode,
					key:  evalKey,
					value: func() (any, error) {
						return e.eval(valueNode)
					},
				})
			// If the key is an evalTemplate, treat the value as the data map.
			case *evalTemplate:
				if err := valueNode.Decode(&evalKey.data); err != nil {
					return fail(fmt.Errorf("decoding template data for mapping key: %w", err))
				}
				m.content = append(m.content, evalKey)
			default:
				return fail(fmt.Errorf("mapping key evaluated to unexpected type %#v", evalKey))
			}
		}
		return &m, nil

	case yaml.SequenceNode:
		// Unlike a mapping, a sequence can be evaluated completely. Create a
		// copy to build up as we determine items to keep, insert, or exclude.
		n := cloneNode(orig)
		n.Content = n.Content[:0]

		var condState conditionalStateMachine

		for _, contentNode := range orig.Content {
			evalItem, err := e.eval(contentNode)
			if err != nil {
				return fail(fmt.Errorf("evaluating sequence item: %w", err))
			}

			toInsert := evalItem
			var isCondition bool

			// First, check for conditions to deal with. This way we can reuse
			// the rest of the logic once we've figured out what value (if any)
			// we need to insert from this branch.
			if item, ok := evalItem.(*evalMapping); ok {
				if c, fv := item.singleCond(); c != nil {
					satisfied, err := condState.shouldTake(c)
					if err != nil {
						return fail(fmt.Errorf("evaluating condition: %w", err))
					}
					if satisfied {
						v, err := fv()
						if err != nil {
							return fail(fmt.Errorf("evaluating condition result value: %w", err))
						}
						toInsert = v
					} else {
						continue
					}
					isCondition = true
				}
			}
			if !isCondition {
				condState.reset()
			}

			r, err := e.resultToYAML(toInsert)
			if err != nil {
				return fail(fmt.Errorf("converting sequence item result to YAML node:\n\t\t%w", err))
			}
			// Put the template result into the sequence depending on the
			// result type.
			switch r.Kind {
			case yaml.ScalarNode, yaml.MappingNode:
				n.Content = append(n.Content, r)
			case yaml.SequenceNode:
				n.Content = append(n.Content, r.Content...)
			default:
				return fail(fmt.Errorf("inlinetemplate result into sequence evaluated to unexpected type %v", kindStr(r)))
			}
		}
		return n, nil

	case yaml.ScalarNode:
		// We've reached a string leaf. This might need contextual evaluation,
		// and in that case we return an eval* struct.
		out, err := e.evalExpressionScalar(orig)
		if err != nil {
			return fail(fmt.Errorf("evaluating scalar expression: %w", err))
		}
		return out, nil

	case yaml.AliasNode:
		return fail(fmt.Errorf("alias nodes are not supported (anchor: %s)", orig.Value))

	default:
		return fail(fmt.Errorf("unsupported node kind: %v", orig.Kind))
	}
}

// evalExpressionScalar evaluates template expressions in a scalar node. The
// scalar node might be in a mapping. Returns the same scalar node if unchanged,
// a modified copy if the value changed but is still a string, or one of the
// eval structs if more processing is needed on the way up.
func (e *EvalState) evalExpressionScalar(node *yaml.Node) (any, error) {
	// Accumulate expression errors.
	var exprErr error
	var r any
	outValue := templateExprRegex.ReplaceAllFunc(
		[]byte(node.Value),
		func(matchBytes []byte) []byte {
			match := string(matchBytes)
			fail := func(err error) []byte {
				exprErr = errors.Join(exprErr, fmt.Errorf("evaluating expression %q: %w", match, err))
				return nil
			}

			execute := func() (any, error) {
				return executeExpression(e, match)
			}

			// For a conditional chain, we need to detect an expression but
			// *not* evaluate it yet. We actually need more context to be able
			// to evaluate it correctly with short-circuit.
			if strings.HasPrefix(match, "${ inlineif ") {
				r = &evalIf{execute: execute}
			} else if strings.HasPrefix(match, "${ inlineelseif ") {
				r = &evalElseIf{execute: execute}
			} else if strings.HasPrefix(match, "${ inlineelse ") {
				r = &evalElse{}
			}
			if r != nil {
				return nil
			}

			// Otherwise, go ahead and execute.
			result, err := execute()
			if err != nil {
				return fail(err)
			}

			switch result := result.(type) {

			// Something like "hello ${ .name }!".
			case string:
				return []byte(result)

			// Something like "${ yml ... }". Can't be part of a larger string.
			case *yaml.Node:
				switch result.Kind {
				case yaml.ScalarNode, yaml.MappingNode, yaml.SequenceNode:
					// The expression gave us a fresh node, so carry over comments.
					appendComments(result, node)
					if r == nil {
						r = result
					} else {
						return fail(fmt.Errorf("multiple inlining functions in one node not supported"))
					}
				default:
					return fail(fmt.Errorf("unexpected result kind %v", kindStr(result)))
				}

			// At this point we know what template file to point at, but we
			// don't know the data (this may be a key scalar, and the data may
			// be in a child element), so we can't evaluate the template yet.
			case *exprTemplateResult:
				r = &evalTemplate{path: result.path}

			// We don't expect exprIfResult, exprElseIfResult, exprElseResult.
			// Those will only be returned when r.execute is called.
			default:
				fail(fmt.Errorf("unexpected result type %#v", result))
			}
			return nil
		})
	if exprErr != nil {
		return nil, fmt.Errorf("executing expressions: %w", exprErr)
	}
	// A special result.
	if r != nil {
		return r, nil
	}
	// The string changed, so create a modified node.
	outStr := string(outValue)
	if outStr != node.Value {
		n := cloneNode(node)
		n.Value = outStr
		return n, nil
	}
	// Nothing happened at all.
	return node, nil
}

func (e *EvalState) resultToYAML(r any) (*yaml.Node, error) {
	fail := func(err error) (*yaml.Node, error) {
		return nil, fmt.Errorf("during final conversion of result to YAML node(s): %w", err)
	}

	switch r := r.(type) {

	case *yaml.Node:
		return r, nil

	case *evalMapping:
		return e.mappingToYAML(r)

	case *evalTemplate:
		return e.evalTemplateResult(r)

	case *evalPair:
		n := cloneNode(r.orig)
		k, err := e.resultToYAML(r.key)
		if err != nil {
			return fail(fmt.Errorf("failed to convert key: %w", err))
		}
		va, err := r.value()
		if err != nil {
			return fail(fmt.Errorf("failed to get value: %w", err))
		}
		v, err := e.resultToYAML(va)
		if err != nil {
			return fail(fmt.Errorf("failed to convert value: %w", err))
		}
		n.Kind = yaml.MappingNode
		n.Content = []*yaml.Node{k, v}
		return n, nil

	case *evalIf, *evalElseIf, *evalElse:
		return fail(fmt.Errorf("conditional found in unexpected location: %T", r))

	default:
		return fail(fmt.Errorf("unexpected result type %#v", r))
	}
}

func (e *EvalState) mappingToYAML(m *evalMapping) (*yaml.Node, error) {
	fail := func(err error) (*yaml.Node, error) {
		return nil, fmt.Errorf("during final conversion of mapping to YAML node(s): %w", err)
	}
	n := cloneNode(m.orig)
	n.Content = n.Content[:0]
	// We don't know yet if this mapping actually ends up staying as a mapping.
	// Keep track here.
	// An inlineif can dissolve into a scalar.
	// Or a template can also be a scalar.
	n.Kind = 0
	n.Tag = ""

	var condState conditionalStateMachine

	for _, mapElem := range m.content {
		// Might contain one *yaml.Node, or some mappingPairs.
		var toInsert []any

		var isCondition bool

		switch mapElem := mapElem.(type) {
		case *evalPair:
			switch key := mapElem.key.(type) {
			// First, check for conditions to deal with. This way we can reuse
			// the rest of the logic once we've figured out what value (if any)
			// we need to insert from this branch.
			case evalConditional:
				isCondition = true
				take, err := condState.shouldTake(key)
				if err != nil {
					return fail(fmt.Errorf("evaluating condition: %w", err))
				}
				if take {
					v, err := mapElem.value()
					if err != nil {
						return fail(fmt.Errorf("evaluating condition result value: %w", err))
					}
					toInsert = append(toInsert, v)
				} else {
					// Skip this mapping pair entirely.
					// False condition, or we already took a true condition branch.
				}
			}
		}
		if !isCondition {
			toInsert = append(toInsert, mapElem)
			condState.reset()
		}

		for _, a := range toInsert {
			node, err := e.resultToYAML(a)
			if err != nil {
				return fail(fmt.Errorf("converting mapping item result to YAML node: %w", err))
			}

			switch node.Kind {
			case yaml.ScalarNode:
				if n.Kind == 0 {
					n.Kind = yaml.ScalarNode
				} else if n.Kind == yaml.ScalarNode {
					return fail(fmt.Errorf("attempted to insert multiple scalars into mapping"))
				} else {
					return fail(fmt.Errorf("mapping contains multiple kinds: had %v, now also %v", kindStr(n), kindStr(node)))
				}
				n.Value = node.Value
			case yaml.SequenceNode, yaml.MappingNode:
				if n.Kind == 0 || n.Kind == node.Kind {
					n.Kind = node.Kind
				} else {
					return fail(fmt.Errorf("mapping contains multiple kinds: had %v, now also %v", kindStr(n), kindStr(node)))
				}
				n.Content = append(n.Content, node.Content...)
			default:
				return fail(fmt.Errorf("unexpected node kind to flatten into mapping: %v", kindStr(node)))
			}
		}
	}
	if n.Kind == 0 {
		// If we never found anything to insert, this is still a mapping.
		n.Kind = yaml.MappingNode
	}
	return n, nil
}

func (e *EvalState) evalTemplateResult(t *evalTemplate) (*yaml.Node, error) {
	ee := *e
	ee.File = filepath.Join(filepath.Dir(e.File), t.path)
	ee.MergeData(t.data)
	v, err := ee.EvalFile()
	if err != nil {
		return nil, err
	}
	v, err = unwrapIfDocumentNode(v)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// MergeData merges the given data map into the EvalState's Data map,
// overwriting any existing keys. The Data map is cloned to avoid mutating
// shared maps.
func (e *EvalState) MergeData(data map[string]any) {
	// Start with a shallow copy of the data to avoid mutation of shared maps.
	e.Data = maps.Clone(e.Data)
	if e.Data == nil {
		e.Data = make(map[string]any)
	}
	maps.Copy(e.Data, data)
}

type evalMapping struct {
	orig    *yaml.Node
	content []any
}

// singleCond returns checks if this mapping contains exactly one conditional,
// and if so returns it, along with the deferred value func.
// Otherwise, returns nils.
func (e *evalMapping) singleCond() (evalConditional, func() (any, error)) {
	if len(e.content) != 1 {
		return nil, nil
	}
	c0, ok := e.content[0].(*evalPair)
	if !ok {
		return nil, nil
	}
	ec, ok := c0.key.(evalConditional)
	if !ok {
		return nil, nil
	}
	return ec, c0.value
}

// evalPair is a single mapping pair inside a mapping, with deferred eval.
// Only occurs inside evalMapping.content.
type evalPair struct {
	orig *yaml.Node

	key   any
	value func() (any, error)
}

type evalConditional interface {
	satisfied() (bool, error)
}

type evalIf struct {
	execute func() (any, error)
}

func (e *evalIf) satisfied() (bool, error) {
	r, err := e.execute()
	if err != nil {
		return false, err
	}
	result, ok := r.(*exprIfResult)
	if !ok {
		return false, fmt.Errorf("if expression did not return exprIfResult, got %#v", r)
	}
	return result.satisfied, nil
}

type evalElseIf struct {
	execute func() (any, error)
}

func (e *evalElseIf) satisfied() (bool, error) {
	r, err := e.execute()
	if err != nil {
		return false, err
	}
	result, ok := r.(*exprElseIfResult)
	if !ok {
		return false, fmt.Errorf("else if expression did not return exprElseIfResult, got %#v", r)
	}
	return result.satisfied, nil
}

type evalElse struct{}

func (e *evalElse) satisfied() (bool, error) {
	return true, nil
}

type evalTemplate struct {
	path string
	data map[string]any
}
