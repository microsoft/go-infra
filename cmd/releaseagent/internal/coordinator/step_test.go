// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"strings"
	"testing"
)

func TestStepCircularDependency(t *testing.T) {
	var a, b, c Step
	a = Step{ID: "a", Name: "A", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&b}}
	b = Step{ID: "b", Name: "B", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&c}}
	c = Step{ID: "c", Name: "C", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&a}}
	_, err := a.TransitiveDependencies()
	if err == nil {
		t.Fatal("expected circular dependency error")
	}
}

func TestValidateSteps(t *testing.T) {
	nop := func(context.Context) error { return nil }
	a := NewRootStep("a", "A", NoTimeout, nop)
	b := NewStep("b", "B", NoTimeout, nop, a)

	for _, test := range []struct {
		name  string
		steps []*Step
		want  string
	}{
		{name: "valid", steps: []*Step{a, b}},
		{name: "empty", want: "empty"},
		{name: "nil step", steps: []*Step{nil}, want: "nil"},
		{name: "duplicate pointer", steps: []*Step{a, a}, want: "more than once"},
		{name: "duplicate ID", steps: []*Step{a, NewRootStep("a", "Other", NoTimeout, nop)}, want: "duplicate ID"},
		{name: "missing dependency", steps: []*Step{b}, want: "unknown step"},
		{name: "nil implementation", steps: []*Step{{ID: "nil-func", Name: "Nil func"}}, want: "no implementation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSteps(test.steps)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateSteps returned unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSteps error = %v, want error containing %q", err, test.want)
			}
		})
	}
}
