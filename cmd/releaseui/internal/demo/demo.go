// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package demo provides simulated release work with real release UI tracking.
package demo

import (
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/releaseinput"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

type input struct {
	Dimensions int
}

type state struct {
	Determinants []float64 `json:"determinants"`
	Number       float64   `json:"number"`
}

const stateSchema = "2026-11-9-estimate"

type processGroup struct{}

// NewProcess creates a demo process group for live testing. It includes two
// independent processes that can run at the same time to test concurrency.
func NewProcess() contract.ProcessGroup {
	return &processGroup{}
}

func (g *processGroup) Identity() *contract.Identity {
	return &contract.Identity{
		Name: "Demo", Mark: "DM",
		Description: "Run a demonstration that uses proper work tracking but performs no external work.",
	}
}

func (g *processGroup) Processes() []contract.Process {
	return []contract.Process{
		&process{id: "demo-science-" + stateSchema},
		&process{id: "demo-bad-science-" + stateSchema, rebel: true},
	}
}

type process struct {
	id    string
	rebel bool
}

func (p *process) Definition() contract.ProcessDefinition {
	name := "Demo: "
	if p.rebel {
		name += "rebellious"
	} else {
		name += "diligent"
	}
	name += " scientist agents calculate the number"
	return contract.ProcessDefinition{
		Identity: contract.Identity{
			Name:        name,
			Description: "Approximate Euler's number using determinants of scaled identity matrices.",
		},
		ID:            p.id,
		InputPreamble: "Choose the conditions for the calculation. This changes the number of steps involved.",
	}
}

func (p *process) Preflight(ctx context.Context) (warning error, blocking error) {
	if p.rebel {
		return errors.New("potential for sabotage"), nil
	}
	return nil, ctx.Err()
}

func (p *process) InputForm(s *releaseinput.Set) any {
	var i input
	s.PositiveIntVar(&i.Dimensions, releaseinput.FieldOptions{Label: "Number of dimensions"})
	return &i
}

func (p *process) Prepare(ctx context.Context, inputFormResult any) (*contract.StateSnapshot, error) {
	i := inputFormResult.(*input)
	// Validate user-facing inputs to make sure they're reasonable.
	if i.Dimensions <= 0 {
		return nil, fmt.Errorf("dimensional order must be positive: %w", contract.ErrInvalidInput)
	}
	if i.Dimensions > 50 {
		return nil, fmt.Errorf("dimensional order must not exceed 50 to prevent multiverse excess: %w", contract.ErrInvalidInput)
	}
	// We only create the slice after verifying the number given to us by the user is reasonable.
	s := state{Determinants: make([]float64, i.Dimensions)}
	return snapshot.Encode(i, s)
}

func (p *process) Validate(saved *contract.StateSnapshot) error {
	input, state, err := snapshot.Decode[input, state](saved)
	if err != nil {
		return err
	}
	// Validate may have looser rules than the user-focused validation in Prepare. This allows a
	// process to load old runs that started before the user-facing limit was reduced, or to support
	// ad-hoc custom-modified runs.
	if input.Dimensions <= 0 || input.Dimensions > 128 {
		return errors.New("dimensional order out of calculable range")
	}
	// If state contains fixed-length slices, it's usually worth validating that it has the expected
	// number of slots.
	if len(state.Determinants) != input.Dimensions {
		return errors.New("determinant result count must match the dimensional order")
	}
	return nil
}

func (p *process) Load(saved *contract.StateSnapshot) (contract.Run, error) {
	input, state, err := snapshot.Decode[input, state](saved)
	if err != nil {
		return nil, err
	}
	return &run{
		name:  p.Definition().Name,
		rebel: p.rebel,
		input: input,
		state: state,
	}, nil
}
