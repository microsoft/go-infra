// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package snapshot_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

type input struct {
	Target string `json:"target"`
}

type state struct {
	Completed []int `json:"completed"`
}

func TestRoundTripIsolation(t *testing.T) {
	wantInput := input{Target: "test"}
	wantState := state{Completed: []int{2, 5}}
	original := state{Completed: []int{2, 5}}
	saved, err := snapshot.Encode(wantInput, original)
	if err != nil {
		t.Fatal(err)
	}
	original.Completed[0] = 99
	decodedInput, decodedState, err := snapshot.Decode[input, state](saved)
	if err != nil {
		t.Fatal(err)
	}
	if decodedInput != wantInput || !reflect.DeepEqual(decodedState, wantState) {
		t.Fatalf("decoded = %#v, %#v", decodedInput, decodedState)
	}
	decodedState.Completed[0] = 99
	_, second, err := snapshot.Decode[input, state](saved)
	if err != nil {
		t.Fatal(err)
	}
	saved.State[0] = '!'
	if !reflect.DeepEqual(second, wantState) {
		t.Fatalf("decoded state aliases another value: %#v", second)
	}
}

func TestDecodeRejectsInvalidPayloads(t *testing.T) {
	if _, _, err := snapshot.Decode[input, state](nil); err == nil {
		t.Fatal("nil snapshot was accepted")
	}
	for _, field := range []string{"input", "state"} {
		for _, test := range []struct {
			name string
			data string
		}{
			{"empty", ""},
			{"null", " \nnull\t"},
			{"unknown field", `{"unexpected":true}`},
			{"invalid JSON", "{"},
			{"wrong type", "[]"},
			{"extra value", "{} {}"},
			{"trailing garbage", "{} !"},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				saved := &contract.StateSnapshot{Input: json.RawMessage(`{}`), State: json.RawMessage(`{}`)}
				if field == "input" {
					saved.Input = json.RawMessage(test.data)
				} else {
					saved.State = json.RawMessage(test.data)
				}
				if _, _, err := snapshot.Decode[input, state](saved); err == nil ||
					!strings.Contains(err.Error(), "snapshot "+field) {

					t.Fatalf("decode error = %v, want %s error", err, field)
				}
			})
		}
	}
}

func TestEncodeRejectsInvalidValues(t *testing.T) {
	for _, field := range []string{"input", "state"} {
		for _, test := range []struct {
			name  string
			value any
		}{
			{"null", (*input)(nil)},
			{"unsupported type", func() {}},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				var inputValue, stateValue any = struct{}{}, struct{}{}
				if field == "input" {
					inputValue = test.value
				} else {
					stateValue = test.value
				}
				saved, err := snapshot.Encode(inputValue, stateValue)
				if saved != nil || err == nil || !strings.Contains(err.Error(), "snapshot "+field) {
					t.Fatalf("encode = %#v, %v, want %s error", saved, err, field)
				}
			})
		}
	}
}
