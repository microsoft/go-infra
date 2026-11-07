// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestStepCircularDependency(t *testing.T) {
	var a, b, c Step
	a = Step{Name: "A", Func: func(context.Context, StepControl) error { return nil }, DependsOn: []*Step{&b}}
	b = Step{Name: "B", Func: func(context.Context, StepControl) error { return nil }, DependsOn: []*Step{&c}}
	c = Step{Name: "C", Func: func(context.Context, StepControl) error { return nil }, DependsOn: []*Step{&a}}
	_, err := a.TransitiveDependencies()
	if err == nil {
		t.Fatal("expected circular dependency error")
	}
}

func TestTransitiveDependenciesValidation(t *testing.T) {
	nop := func(context.Context, StepControl) error { return nil }
	a := NewRootStep("A", NoTimeout, nop)

	for _, test := range []struct {
		name string
		step *Step
		want string
	}{
		{name: "valid", step: a.Then("B", NoTimeout, nop)},
		{name: "nil dependency", step: &Step{Name: "B", Func: nop, DependsOn: []*Step{nil}}, want: "nil dependency"},
		{name: "duplicate dependency", step: &Step{Name: "B", Func: nop, DependsOn: []*Step{a, a}}, want: "more than once"},
		{name: "duplicate join dependency", step: All(a, a).Then("B", NoTimeout, nop), want: "more than once"},
		{name: "duplicate name", step: a.Then("A", NoTimeout, nop), want: "not unique"},
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

func TestStepSetThenJoinsBranches(t *testing.T) {
	nop := func(context.Context, StepControl) error { return nil }
	root := NewRootStep("Root", NoTimeout, nop)
	a := root.Then("A", NoTimeout, nop)
	b := root.Then("B", NoTimeout, nop)
	join := All(a, b).Then("Join", NoTimeout, nop)

	if !slices.Equal(join.DependsOn, []*Step{a, b}) {
		t.Fatalf("join dependencies = %v, want A and B", join.DependsOn)
	}
	steps, err := join.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps, []*Step{root, a, b, join}) {
		t.Fatalf("steps = %v, want Root, A, B, Join", steps)
	}
}

func TestStepSetThenCopiesDependencies(t *testing.T) {
	nop := func(context.Context, StepControl) error { return nil }
	a := NewRootStep("A", NoTimeout, nop)
	b := NewRootStep("B", NoTimeout, nop)
	dependencies := All(a, b)
	first := dependencies.Then("First", NoTimeout, nop)
	second := dependencies.Then("Second", NoTimeout, nop)

	dependencies[0] = b
	first.DependsOn[1] = a
	if !slices.Equal(first.DependsOn, []*Step{a, a}) {
		t.Fatal("changing the dependency list changed the first join")
	}
	if !slices.Equal(second.DependsOn, []*Step{a, b}) {
		t.Fatal("changing the dependency list or first join changed the second join")
	}
}
