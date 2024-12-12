// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"testing"
)

func TestStepCircularDependency(t *testing.T) {
	var a, b, c Step
	a = Step{Name: "a", DependsOn: []*Step{&b}}
	b = Step{Name: "b", DependsOn: []*Step{&c}}
	c = Step{Name: "c", DependsOn: []*Step{&a}}
	_, err := a.TransitiveDependencies()
	if err == nil {
		t.Fatal("expected circular dependency error")
	}
}
