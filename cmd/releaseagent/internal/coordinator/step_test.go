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
	a = Step{Name: "A", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&b}}
	b = Step{Name: "B", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&c}}
	c = Step{Name: "C", Func: func(context.Context) error { return nil }, DependsOn: []*Step{&a}}
	_, err := a.TransitiveDependencies()
	if err == nil {
		t.Fatal("expected circular dependency error")
	}
}

func TestTransitiveDependenciesValidation(t *testing.T) {
	nop := func(context.Context) error { return nil }
	a := NewRootStep("A", NoTimeout, nop)

	for _, test := range []struct {
		name string
		step *Step
		want string
	}{
		{name: "valid", step: NewStep("B", NoTimeout, nop, a)},
		{name: "nil dependency", step: NewStep("B", NoTimeout, nop, nil), want: "nil dependency"},
		{name: "duplicate dependency", step: NewStep("B", NoTimeout, nop, a, a), want: "more than once"},
		{name: "duplicate name", step: NewStep("A", NoTimeout, nop, a), want: "not unique"},
		{name: "empty name", step: &Step{Func: nop}, want: "empty name"},
		{name: "nil implementation", step: &Step{Name: "Nil func"}, want: "no implementation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.step.TransitiveDependencies()
			if test.want == "" {
				if err != nil {
					t.Fatalf("TransitiveDependencies returned unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("TransitiveDependencies error = %v, want error containing %q", err, test.want)
			}
		})
	}
}
