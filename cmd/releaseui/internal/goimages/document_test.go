// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
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

func TestDocumentExecutionDigestDetectsInputChange(t *testing.T) {
	document := testDocument(t)
	document.Input.SourceVersion = "2ef65db89e42942c24e3d8f0b8a8eb52bc86857a"
	if err := document.Validate(); err == nil {
		t.Fatal("modified immutable input unexpectedly passed validation")
	}
}

func TestGoImagesProcessRejectsUnsupportedSchemas(t *testing.T) {
	input := json.RawMessage(`{"mode":"normal"}`)
	for _, data := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"schemaVersion":2}`)} {
		if _, _, err := decodeGoImagesSnapshot(&contract.StateSnapshot{Input: input, State: data}); err == nil {
			t.Fatalf("unsupported schema %s was accepted", data)
		}
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
			input, state := testDocumentState(t)
			test.change(state)
			if _, err := NewDocument(input, state, time.Now()); err == nil {
				t.Fatalf("invalid %s state unexpectedly produced a document", test.name)
			}
		})
	}
}

func testDocument(t *testing.T) *Document {
	t.Helper()
	input, state := testDocumentState(t)
	document, err := NewDocument(input, state, time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func testDocumentState(t *testing.T) (*Input, *State) {
	t.Helper()
	input := &Input{
		Versions: []string{"1.26.1-1"}, Mode: ModeNormal,
		SourceVersion: "81ce9afc2b75ec4e153dd15fc3c7539b12024945",
	}
	state, err := NewState(input)
	if err != nil {
		t.Fatal(err)
	}
	return input, state
}
