// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func normalizeProcessInputs(inputs []ProcessInput, data json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var encoded map[string]json.RawMessage
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("decode process inputs: %w", err)
	}
	if encoded == nil {
		return nil, errors.New("process inputs must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("process inputs must contain exactly one JSON value")
	}

	byID := make(map[string]ProcessInput, len(inputs))
	values := make(map[string]string, len(inputs))
	provided := make(map[string]bool, len(encoded))
	for _, input := range inputs {
		byID[input.ID] = input
		values[input.ID] = strings.TrimSpace(input.Default)
	}
	for id, raw := range encoded {
		input, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown process input %q", id)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("process input %q must be a string", id)
		}
		values[input.ID] = strings.TrimSpace(value)
		provided[input.ID] = true
	}

	normalized := make(map[string]string, len(inputs))
	for _, input := range inputs {
		visible := input.VisibleWhen == nil || values[input.VisibleWhen.InputID] == input.VisibleWhen.Equals
		if !visible {
			if provided[input.ID] {
				return nil, fmt.Errorf("process input %q is not available for the selected options", input.ID)
			}
			continue
		}
		value := values[input.ID]
		if value == "" {
			return nil, fmt.Errorf("process input %q is required", input.ID)
		}
		switch input.Type {
		case "choice":
			valid := false
			for _, option := range input.Options {
				valid = valid || option.Value == value
			}
			if !valid {
				return nil, fmt.Errorf("process input %q has unsupported value %q", input.ID, value)
			}
		case "number":
			number, err := strconv.ParseUint(value, 10, 64)
			if err != nil || number == 0 {
				return nil, fmt.Errorf("process input %q must be a positive integer", input.ID)
			}
		default:
			return nil, fmt.Errorf("process input %q has unsupported type %q", input.ID, input.Type)
		}
		normalized[input.ID] = value
	}
	result, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode normalized process inputs: %w", err)
	}
	return result, nil
}
