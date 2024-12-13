// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func execute(t *testing.T, step *Step) error {
	t.Helper()
	steps, err := step.TransitiveDependencies()
	if err != nil {
		t.Fatal(err)
	}
	var sr StepRunner
	return sr.Execute(context.Background(), steps)
}

func TestStepRunner_Execute_Cancel(t *testing.T) {
	// Test that when a step fails, steps that depend on it don't enter their impls.
	a := NewRootStep(
		"failure", NoTimeout,
		func(ctx context.Context) error {
			return fmt.Errorf("intentional failure")
		},
	).Then(
		"dependent", NoTimeout,
		func(ctx context.Context) error {
			t.Fatal("dependent step ran")
			return nil
		})
	if err := execute(t, a); err == nil {
		t.Fatal("expected error")
	}
}

func TestStepRunner_Execute_PanicToError(t *testing.T) {
	// Test that when a step panics, it is treated as an error.
	a := NewRootStep(
		"panic", NoTimeout,
		func(ctx context.Context) error {
			panic("intentional panic")
		},
	)
	if err := execute(t, a); err != nil {
		if !errors.Is(err, stepPanicErr) {
			t.Fatalf("expected StepPanicErr err, got: %v", err)
		}
	} else {
		t.Fatal("expected error")
	}
}
