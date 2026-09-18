// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

func TestDocumentOmitsDerivedPlan(t *testing.T) {
	document := testDocument(t)
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != CurrentSchemaVersion || document.LegacyPlan != nil {
		t.Fatalf("new document contains legacy graph metadata: %#v", document)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"plan"`)) {
		t.Fatalf("new document persisted derived plan: %s", data)
	}

	steps := testSteps()
	steps[0].Timeout = time.Second
	if err := document.ValidateGraph(steps); err != nil {
		t.Fatalf("current document depends on persisted graph metadata: %v", err)
	}
}

func TestLegacyDocumentPlanFingerprint(t *testing.T) {
	document := testLegacyDocument(t)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var restored Document
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil {
		t.Fatalf("valid schema-8 document was rejected: %v", err)
	}
	if err := restored.ValidateGraph(testSteps()); err != nil {
		t.Fatalf("unchanged legacy graph did not match: %v", err)
	}

	steps := testSteps()
	steps[0].Timeout = time.Second
	if err := restored.ValidateGraph(steps); err == nil {
		t.Fatal("changed graph unexpectedly matched legacy plan")
	}

	document = testLegacyDocument(t)
	document.LegacyPlan.WorkflowRevision++
	if err := document.Validate(); err == nil {
		t.Fatal("unsupported workflow revision unexpectedly passed validation")
	}

	document = testLegacyDocument(t)
	document.LegacyPlan.Steps[0].Name = "tampered"
	if err := document.Validate(); err == nil {
		t.Fatal("tampered plan unexpectedly passed validation")
	}

	document = testLegacyDocument(t)
	document.LegacyPlan.Steps[1].Name = document.LegacyPlan.Steps[0].Name
	digest, err := legacyPlanDigest(document.LegacyPlan.Steps)
	if err != nil {
		t.Fatal(err)
	}
	document.LegacyPlan.Digest = digest
	document.ExecutionDigest, err = legacyExecutionDigest(document.Input, *document.LegacyPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(); err == nil {
		t.Fatal("duplicate persisted step name unexpectedly passed validation")
	}
}

func TestDocumentWithStateDoesNotMutateOriginal(t *testing.T) {
	document := testDocument(t)
	state := document.State
	state.QueueAttempted = true
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

func TestDocumentRejectsInvalidState(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*State)
	}{
		{name: "checksum", change: func(state *State) { state.InputChecksum++ }},
		{name: "build ID", change: func(state *State) {
			state.QueueAttempted = true
			state.BuildID = "invalid"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := testDocument(t)
			test.change(&document.State)
			if err := document.Validate(); err == nil {
				t.Fatalf("document with invalid %s unexpectedly passed validation", test.name)
			}
		})
	}
}

func testDocument(t *testing.T) *Document {
	t.Helper()
	input := &Input{
		Versions: []string{"1.26.1-1"}, Mode: ModeNormal,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	}
	state, err := NewState(input)
	if err != nil {
		t.Fatal(err)
	}
	document, err := NewDocument(input, state, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func testLegacyDocument(t *testing.T) *Document {
	t.Helper()
	document := testDocument(t)
	plan, err := newLegacyPlan(testSteps())
	if err != nil {
		t.Fatal(err)
	}
	document.SchemaVersion = legacySchemaVersion
	document.LegacyPlan = &plan
	document.ExecutionDigest, err = legacyExecutionDigest(document.Input, plan)
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
