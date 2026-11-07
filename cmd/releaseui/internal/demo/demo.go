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
	s := state{Determinants: make([]float64, i.Dimensions)}
	return snapshot.Encode(i, s)
}

func (p *process) Validate(saved *contract.StateSnapshot) error {
	input, state, err := snapshot.Decode[input, state](saved)
	if err != nil {
		return err
	}

	if input.Dimensions <= 0 {
		return errors.Join(errors.New("dimensional order must be positive"), contract.ErrInvalidInput)
	}
	if input.Dimensions > 50 {
		return errors.Join(errors.New("dimensional order must not exceed 50 to prevent multiverse excess"), contract.ErrInvalidInput)
	}

	if len(state.Determinants) != input.Dimensions {
		return errors.New("demo determinant results must match the dimensional order")
	}
	for dimension, result := range state.Determinants {
		if result != 0 && result != determinant(dimension+1) {
			return errors.New("demo result must be zero or the determinant of the dimension's scaled identity matrix")
		}
		if state.Number != 0 && result == 0 {
			return errors.New("demo number requires all determinant results")
		}
	}
	if state.Number != 0 && state.Number != estimateNumber(state.Determinants) {
		return errors.New("demo number must match the estimate from all determinant results")
	}
	return nil
}

func (p *process) Load(saved *contract.StateSnapshot) (contract.Run, error) {
	input, state, err := snapshot.Decode[input, state](saved)
	if err != nil {
		return nil, err
	}
	return &run{
		name:  p.Definition().Identity.Name,
		rebel: p.rebel,
		input: input,
		state: state,
	}, nil
}
