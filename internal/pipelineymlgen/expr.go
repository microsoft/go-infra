// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"go.yaml.in/yaml/v4"
)

type exprIfResult struct {
	// satisfied indicates the condition was true.
	satisfied bool
}

type exprElseIfResult struct {
	// satisfied indicates the condition was true. Note that if this node
	// follows an if, it still might be discarded.
	satisfied bool
}

type exprElseResult struct{}

type exprTemplateResult struct {
	path string
}

// executeExpression evaluates the given template expression using Data from e.
// The expression string includes "${" and "}". Returns the result, one of:
//   - string
//   - *yaml.Node
//   - *exprIfResult
//   - *exprElseIfResult
//   - *exprElseResult
//   - *exprTemplateResult
//   - nil, and an error
func executeExpression(e *EvalState, expr string) (any, error) {
	fail := func(err error) (any, error) {
		return nil, fmt.Errorf("evaluating expression %q: %w", expr, err)
	}

	var ok bool
	expr, ok = cutExpr(expr)
	if !ok {
		return nil, fmt.Errorf("expression %q is missing ${ ... } wrapper", expr)
	}

	var result any

	tmpl := template.New("pipelineymlgenexpr").Funcs(sprig.HermeticTxtFuncMap())
	tmpl = tmpl.Funcs(template.FuncMap{
		"inlineif": func(v bool) (string, error) {
			result = &exprIfResult{satisfied: v}
			return "", nil
		},
		"inlineelseif": func(v bool) (string, error) {
			result = &exprElseIfResult{satisfied: v}
			return "", nil
		},
		"inlineelse": func() (string, error) {
			result = &exprElseResult{}
			return "", nil
		},
		"yml": func(v any) (string, error) {
			// We can't go directly from "any" to a yaml.Node, so use a []byte.
			out, err := yaml.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("failed to marshal inline value: %w", err)
			}
			var n yaml.Node
			if err := yaml.Unmarshal(out, &n); err != nil {
				return "", fmt.Errorf("failed to unmarshal inline value: %w", err)
			}
			// The result is wrapped in a Document node. Pull it out.
			if n.Kind != yaml.DocumentNode {
				return "", fmt.Errorf("inlined expression resulted in non-document node of kind %v", n.Kind)
			}
			if len(n.Content) != 1 {
				return "", fmt.Errorf("inlined expression resulted in document node with %d content nodes (expected 1)", len(n.Content))
			}
			n = *n.Content[0]
			result = &n
			return "", nil
		},
		"inlinetemplate": func(templatePath string) (string, error) {
			// We can't evaluate the template right here: we don't have access
			// to data that might be in a child node. The caller has to do it.
			result = &exprTemplateResult{path: templatePath}
			return "", nil
		},
	})

	tmpl, err := tmpl.Parse("{{ " + expr + " }}")
	if err != nil {
		return fail(fmt.Errorf("failed to parse template: %w", err))
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, e.Data); err != nil {
		return fail(fmt.Errorf("failed to execute template: %w", err))
	}

	// Many expressions simply result in a string. If we didn't encounter any
	// special case, put in the evaluated text/template result.
	if result == nil {
		result = buf.String()
	}
	return result, nil
}

func cutExpr(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s, ok := strings.CutPrefix(s, "${ ")
	if !ok {
		return "", false
	}
	s, ok = strings.CutSuffix(s, " }")
	if !ok {
		return "", false
	}
	return s, true
}
