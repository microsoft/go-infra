// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagessession

import (
	"context"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

func TestDocumentPlanFingerprint(t *testing.T) {
	document := testDocument(t)
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}

	steps := testSteps()
	plan, err := NewPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.MatchesPlan(plan); err != nil {
		t.Fatalf("unchanged graph did not match: %v", err)
	}
	steps[0].Timeout = time.Second
	plan, err = NewPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.MatchesPlan(plan); err == nil {
		t.Fatal("changed graph unexpectedly matched persisted plan")
	}

	document = testDocument(t)
	document.Plan.WorkflowRevision++
	if err := document.Validate(); err == nil {
		t.Fatal("unsupported workflow revision unexpectedly passed validation")
	}

	document = testDocument(t)
	document.Plan.Steps[0].Name = "tampered"
	if err := document.Validate(); err == nil {
		t.Fatal("tampered plan unexpectedly passed validation")
	}

	document = testDocument(t)
	document.Plan.Steps[1].Name = document.Plan.Steps[0].Name
	digest, err := planDigest(document.Plan.Steps)
	if err != nil {
		t.Fatal(err)
	}
	document.Plan.Digest = digest
	if err := document.Validate(); err == nil {
		t.Fatal("duplicate persisted step name unexpectedly passed validation")
	}
}

func TestDocumentWithStateDoesNotMutateOriginal(t *testing.T) {
	document := testDocument(t)
	state := document.State
	state.BuildID = "42"
	updatedAt := document.UpdatedAt.Add(time.Minute)
	updated, err := document.WithState(&state, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if document.State.BuildID != "" {
		t.Fatalf("original build ID = %q, want empty", document.State.BuildID)
	}
	if updated.State.BuildID != "42" {
		t.Fatalf("updated build ID = %q, want 42", updated.State.BuildID)
	}
	if !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated time = %v, want %v", updated.UpdatedAt, updatedAt)
	}
	if updated.ExecutionDigest != document.ExecutionDigest {
		t.Fatalf("state update changed execution digest: %q != %q", updated.ExecutionDigest, document.ExecutionDigest)
	}
}

func TestDocumentExecutionDigestDetectsInputChange(t *testing.T) {
	document := testDocument(t)
	document.Input.SourceVersion = "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"
	if err := document.Validate(); err == nil {
		t.Fatal("modified immutable input unexpectedly passed validation")
	}
}

func testDocument(t *testing.T) *Document {
	t.Helper()
	input := &goimagesworkflow.Input{
		Versions: []string{"1.26.1-1"}, Mode: goimagesworkflow.ModeNormal,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	}
	state, err := goimagesworkflow.NewState(input)
	if err != nil {
		t.Fatal(err)
	}
	document, err := NewDocument(input, state, testSteps(), time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func testSteps() []*coordinator.Step {
	root := coordinator.NewRootStep("Root", coordinator.NoTimeout, func(context.Context) error { return nil })
	leaf := root.Then("Leaf", time.Minute, func(context.Context) error { return nil })
	return []*coordinator.Step{root, leaf}
}
