// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pipelineymlgen

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

type exprRangeResult struct {
	keyName    string
	valueName  string
	collection any
	inline     bool
}

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
//   - *exprRangeResult
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
			n, err := marshalToNode(v)
			if err != nil {
				return "", fmt.Errorf("failed to convert inline value: %w", err)
			}
			result = n
			return "", nil
		},
		"inlinetemplate": func(templatePath string) (string, error) {
			// We can't evaluate the template right here: we don't have access
			// to data that might be in a child node. The caller has to do it.
			result = &exprTemplateResult{path: templatePath}
			return "", nil
		},
		"inlinerange": func(args ...any) (string, error) {
			r, err := parseRangeArgs("inlinerange", args)
			if err != nil {
				return "", err
			}
			r.inline = true
			result = r
			return "", nil
		},
		"ymlrange": func(args ...any) (string, error) {
			r, err := parseRangeArgs("ymlrange", args)
			if err != nil {
				return "", err
			}
			result = r
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

// parseRangeArgs parses the arguments to inlinerange or ymlrange. The name
// parameter is used for error messages.
func parseRangeArgs(name string, args []any) (*exprRangeResult, error) {
	r := &exprRangeResult{}
	switch len(args) {
	case 1:
		r.collection = args[0]
	case 2:
		n, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("%s: expected string for value name, got %T", name, args[0])
		}
		r.valueName = n
		r.collection = args[1]
	case 3:
		kn, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("%s: expected string for key name, got %T", name, args[0])
		}
		vn, ok := args[1].(string)
		if !ok {
			return nil, fmt.Errorf("%s: expected string for value name, got %T", name, args[1])
		}
		r.keyName = kn
		r.valueName = vn
		r.collection = args[2]
	default:
		return nil, fmt.Errorf("%s: expected 1-3 args, got %d", name, len(args))
	}
	return r, nil
}
