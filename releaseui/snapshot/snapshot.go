// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package snapshot encodes and strictly decodes typed release input and state.
package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/microsoft/go-infra/releaseui/contract"
)

// Encode returns independent JSON copies of input and state. Neither value may encode as null.
// Use struct{}{} for a process with no input. Process-specific validation belongs to the caller.
func Encode[I, S any](input I, state S) (*contract.StateSnapshot, error) {
	inputJSON, err := encode(input)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot input: %w", err)
	}
	stateJSON, err := encode(state)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot state: %w", err)
	}
	return &contract.StateSnapshot{Input: inputJSON, State: stateJSON}, nil
}

// Decode returns independent typed input and state. It rejects nil snapshots, null values,
// unknown struct fields, and trailing JSON values. Process-specific validation belongs to the
// caller. Use struct{} as the input type for a process with no input.
func Decode[I, S any](snapshot *contract.StateSnapshot) (I, S, error) {
	var input I
	var state S
	if snapshot == nil {
		return input, state, errors.New("snapshot is nil")
	}
	input, err := decode[I](snapshot.Input)
	if err != nil {
		return input, state, fmt.Errorf("decode snapshot input: %w", err)
	}
	state, err = decode[S](snapshot.State)
	if err != nil {
		var zeroInput I
		var zeroState S
		return zeroInput, zeroState, fmt.Errorf("decode snapshot state: %w", err)
	}
	return input, state, nil
}

func encode[T any](value T) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(data, []byte("null")) {
		return nil, errors.New("value must not be null")
	}
	return data, nil
}

func decode[T any](data json.RawMessage) (T, error) {
	var zero T
	var value *T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return zero, err
	}
	if value == nil {
		return zero, errors.New("value must not be null")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return zero, errors.New("expected exactly one JSON value")
	}
	return *value, nil
}
