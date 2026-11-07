// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package demo provides simulated release work with real release UI tracking.
package demo

import (
	"context"
	"errors"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

type state struct {
	WorkCompleted [2]int `json:"workCompleted"`
}

const stateSchema = "2026-11-6"

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
		&process{id: "demo-a-" + stateSchema, name: "Demo A"},
		&process{id: "demo-b-" + stateSchema, name: "Demo B"},
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
	return snapshot.Encode(struct{}{}, state{})
}

func (p *process) Validate(saved *contract.StateSnapshot) error {
	_, state, err := snapshot.Decode[struct{}, state](saved)
	if err != nil {
		return err
	}
	for _, completed := range state.WorkCompleted {
		if completed < 0 || completed > workUnits {
			return errors.New("demo work count is out of range")
		}
	}
	return nil
}

func (p *process) Load(saved *contract.StateSnapshot) (contract.Run, error) {
	_, state, err := snapshot.Decode[struct{}, state](saved)
	if err != nil {
		return nil, err
	}
	return &run{name: p.name, state: state}, nil
}
