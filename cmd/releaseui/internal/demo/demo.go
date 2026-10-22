// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package demo provides simulated release work with real release UI tracking.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
)

type processGroup struct{}

// NewProcess creates a demo process group for live testing. It includes two
// independent processes that can run at the same time to test concurrency.
func NewProcess() contract.ProcessGroup {
	return &processGroup{}
}

func (g *processGroup) Identity() *contract.Identity {
	return &contract.Identity{
		Name: "Demo", Mark: "DM",
		Description: "Run simulated work using real Azure DevOps test records.",
	}
}

func (g *processGroup) Processes() []contract.Process {
	return []contract.Process{
		&process{id: "demo-a", name: "Demo A"},
		&process{id: "demo-b", name: "Demo B"},
	}
}

type process struct {
	id   string
	name string
}

func (p *process) Definition() contract.ProcessDefinition {
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name: p.name, Description: "Simulate two parallel branches of work without running a release.",
		},
		ID: p.id, InputSubmitLabel: "Review demo",
		InputPreamble: "The workload and timing are fixed. Run both demos to observe independent releases.",
		Notice: &contract.Notice{
			Title:   "Fake work, real tracking items",
			Message: "Confirmation creates a real Azure DevOps test work item. No builds, workflows, or artifacts are published.",
		},
	}
}

func (p *process) Preflight(ctx context.Context) (warning error, blocking error) {
	return nil, ctx.Err()
}

func (p *process) InputForm(*releaseinput.Set) any {
	return nil
}

func (p *process) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	if inputFormResult != nil {
		panic("demo process received an input value")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encodeSnapshot(state{
		SchemaVersion: stateSchema, UpdatedAt: time.Now().UTC(),
	})
}

func (p *process) Load(snapshot *contract.StateSnapshot) (contract.Run, error) {
	if snapshot == nil {
		return nil, errors.New("demo snapshot is nil")
	}
	input, err := decodeStrictJSON[*struct{}](snapshot.Input)
	if err != nil {
		return nil, fmt.Errorf("decode demo input: %w", err)
	}
	if input == nil {
		return nil, errors.New("demo input must be an object")
	}
	state, err := decodeStrictJSON[state](snapshot.State)
	if err != nil {
		return nil, fmt.Errorf("decode demo state: %w", err)
	}
	if err := validateState(state); err != nil {
		return nil, err
	}
	return newRun(p.name, state), nil
}

func encodeSnapshot(state state) (*contract.StateSnapshot, error) {
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode demo state: %w", err)
	}
	return &contract.StateSnapshot{Input: json.RawMessage(`{}`), State: stateJSON}, nil
}

func validateState(state state) error {
	if state.SchemaVersion != stateSchema {
		return fmt.Errorf("unsupported demo state schema %d", state.SchemaVersion)
	}
	if state.UpdatedAt.IsZero() {
		return errors.New("demo update time is zero")
	}
	for _, completed := range state.WorkCompleted {
		if completed < 0 || completed > workUnits {
			return errors.New("demo work count is out of range")
		}
		if !state.Initialized && (completed != 0 || state.Finished) {
			return errors.New("demo work started before initialization")
		}
		if state.Finished && completed != workUnits {
			return errors.New("demo finished before both branches completed")
		}
	}
	return nil
}

func decodeStrictJSON[T any](data json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return value, errors.New("expected exactly one JSON value")
	}
	return value, nil
}
