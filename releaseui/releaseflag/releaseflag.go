// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package releaseflag provides the model for defining release UI inputs and utilities for setting
// up input bindings.
package releaseflag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/releaseui/internal/webview"
)

// FieldOptions contains the display text for one bound input field.
type FieldOptions struct {
	// Label names the field.
	Label string

	// Description explains the value expected from the operator.
	Description string

	// Placeholder is example text shown by an empty field.
	Placeholder string
}

// InputSet is the set of inputs presented to the release runner to determine how the release will
// run. It contains the bindings between browser fields and Go fields.
type InputSet struct {
	bindings []inputBinding
	err      error
}

// Validate reports errors in the input declarations without parsing user input.
func (s *InputSet) Validate() error {
	return s.err
}

// PositiveIntVar binds id to target and renders it as a positive integer field.
func (s *InputSet) PositiveIntVar(target *int, id string, options FieldOptions) {
	if s.err != nil {
		return
	}
	if target == nil {
		s.err = fmt.Errorf("input %q has a nil target", id)
		return
	}
	for _, binding := range s.bindings {
		if binding.definition.ID == id {
			s.err = fmt.Errorf("input %q is bound more than once", id)
			return
		}
	}
	s.bindings = append(s.bindings, inputBinding{
		definition: webview.Input{
			ID: id, Type: "number", Label: options.Label,
			Description: options.Description, Placeholder: options.Placeholder,
		},
		set: func(raw json.RawMessage) error {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("input %q must be a string", id)
			}
			number, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil || number == 0 || uint64(int(number)) != number {
				return fmt.Errorf("input %q must be a positive integer", id)
			}
			*target = int(number)
			return nil
		},
	})
}

// Inputs returns the UI definitions for the bound fields.
func (s *InputSet) Inputs() []webview.Input {
	inputs := make([]webview.Input, len(s.bindings))
	for index, binding := range s.bindings {
		inputs[index] = binding.definition
	}
	return inputs
}

// Parse binds one JSON object to the registered Go fields.
func (s *InputSet) Parse(data json.RawMessage) error {
	if s.err != nil {
		return s.err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var encoded map[string]json.RawMessage
	if err := decoder.Decode(&encoded); err != nil {
		return fmt.Errorf("decode process inputs: %w", err)
	}
	if encoded == nil {
		return errors.New("process inputs must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("process inputs must contain exactly one JSON value")
	}
	for _, binding := range s.bindings {
		raw, ok := encoded[binding.definition.ID]
		if !ok {
			return fmt.Errorf("input %q is required", binding.definition.ID)
		}
		if err := binding.set(raw); err != nil {
			return err
		}
		delete(encoded, binding.definition.ID)
	}
	if len(encoded) > 0 {
		unknown := make([]string, 0, len(encoded))
		for id := range encoded {
			unknown = append(unknown, id)
		}
		sort.Strings(unknown)
		return fmt.Errorf("unknown process input %q", unknown[0])
	}
	return nil
}

type inputBinding struct {
	definition webview.Input
	set        func(json.RawMessage) error
}
