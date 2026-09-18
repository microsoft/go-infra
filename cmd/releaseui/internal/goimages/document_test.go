// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestDocumentOmitsDerivedPlan(t *testing.T) {
	document := testDocument(t)
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"plan"`)) {
		t.Fatalf("new document persisted derived plan: %s", data)
	}
}

func TestDocumentWithStateDoesNotMutateOriginal(t *testing.T) {
	document := testDocument(t)
	state := document.State
	state.VerifiedMirroredCommit = document.Input.SourceVersion
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

func TestGoImagesProcessRejectsUnsupportedSchemas(t *testing.T) {
	for _, test := range []struct {
		name   string
		decode func(json.RawMessage) error
	}{
		{name: "payload", decode: func(data json.RawMessage) error {
			_, err := decodeGoImagesProcessPayload(data)
			return err
		}},
		{name: "checkpoint", decode: func(data json.RawMessage) error {
			_, err := decodeGoImagesCheckpoint(data)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, data := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"schemaVersion":2}`)} {
				if err := test.decode(data); err == nil {
					t.Fatalf("unsupported schema %s was accepted", data)
				}
			}
		})
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
