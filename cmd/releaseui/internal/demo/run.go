// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

const (
	workUnits   = 30
	workDelay   = time.Second
	stepTimeout = 2 * time.Minute
)

type run struct {
	name  string
	rebel bool
	input input
	state state
}

func (r *run) TakeSnapshot() (*contract.StateSnapshot, error) {
	return snapshot.Encode(r.input, r.state)
}

func (r *run) TakeView() *contract.RunView {
	summary := "Ready"
	completedDimensions := 0
	for _, result := range r.state.Determinants {
		if result != 0 {
			summary = "Calculating the number"
			completedDimensions++
		}
	}
	if r.state.Number != 0 {
		summary = fmt.Sprintf("The number is %.6f, an estimate of e", r.state.Number)
	}
	return &contract.RunView{
		Test: true, Summary: summary,
		Detail: fmt.Sprintf("%s: %d/%d dimensions analyzed",
			r.name, completedDimensions, r.input.Dimensions),
	}
}

func (r *run) Plan() *contract.Plan {
	scientists := "Diligent"
	if r.rebel {
		scientists = "Rebellious: sabotage third dimension halfway through analysis"
	}
	return &contract.Plan{
		Subtitle:             r.name,
		ExecutionButtonLabel: "Calculate the number",
		Facts: []contract.PlanFact{
			{Label: "Process", Value: r.name},
			{Label: "Scientist agents", Value: scientists},
			{Label: "Work", Value: fmt.Sprintf("Calculate scaled identity determinants for dimensions 1 to %d in parallel", r.input.Dimensions)},
			{Label: "Equation", Value: "det((1 + 1/n) I_n) = (1 + 1/n)^n approaches e"},
			{Label: "Estimate", Value: "Fit a line to n times each determinant against n; its slope estimates e using every dimension"},
			{Label: "External actions", Value: "No builds are queued, workflows dispatched, or artifacts published"},
		},
	}
}

func (r *run) Build() ([]*coordinator.Step, error) {
	analyses := make([]*coordinator.Step, r.input.Dimensions)
	for dimension := range analyses {
		analyses[dimension] = coordinator.NewRootStep(
			fmt.Sprintf("Analyze dimension %d", dimension+1),
			stepTimeout,
			func(ctx context.Context, ctl coordinator.StepControl) error {
				var d float64
				ctl.Read(func() {
					d = r.state.Determinants[dimension]
				})
				if d != 0 {
					// Resuming, already calculated.
					return nil
				}
				// Simulate increasing workload per dimension.
				workTotal := dimensionWorkTotal(dimension + 1)
				ticker := time.NewTicker(workDelay)
				for work := range workTotal {
					if err := ctx.Err(); err != nil {
						return err
					}
					ctl.ReportProgress(coordinator.StepProgress{
						Summary:   workingPhrases[(work+dimension)%len(workingPhrases)],
						Completed: work,
						Total:     workTotal,
					})
					if r.rebel && dimension == 2 && work >= workTotal/2 {
						return errors.New("rebellious scientist agent sabotaged the third dimension")
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ticker.C:
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				ctl.ReportProgress(coordinator.StepProgress{
					Summary: "Complete",
				})
				// Calculation. For demo purposes, pretend it's expensive, so we don't call it
				// inside the Update fn.
				d = determinant(dimension + 1)
				ctl.Update(func() {
					r.state.Determinants[dimension] = d
				})
				return nil
			},
		)
	}
	finish := coordinator.All(analyses...).Then(
		"Calculate the number",
		stepTimeout,
		func(_ context.Context, ctl coordinator.StepControl) error {
			var number float64
			ctl.Read(func() {
				number = r.state.Number
			})
			if number != 0 {
				// Resuming, already calculated.
				return nil
			}

			var dets []float64
			ctl.Read(func() {
				dets = slices.Clone(r.state.Determinants)
			})
			number = estimateNumber(dets)
			ctl.Update(func() {
				r.state.Number = number
			})
			ctl.ReportProgress(coordinator.StepProgress{
				Summary: "Calculated the number",
				Detail:  fmt.Sprintf("Number: %v", number),
			})
			return nil
		},
	)
	return finish.TransitiveDependencies()
}

func determinant(dimension int) float64 {
	n := float64(dimension)
	return math.Pow(1+1/n, n)
}

func dimensionWorkTotal(dimension int) int {
	return int(math.Ceil(float64(workUnits) * (1 - math.Pow(0.5, float64(dimension)))))
}

func estimateNumber(determinants []float64) float64 {
	if len(determinants) == 1 {
		return determinants[0]
	}
	// n * determinant(n) approaches e*n - e/2, so the fitted slope estimates e.
	meanDimension := float64(len(determinants)+1) / 2
	var covariance, variance float64
	for dimension, result := range determinants {
		n := float64(dimension + 1)
		offset := n - meanDimension
		covariance += offset * n * result
		variance += offset * offset
	}
	return covariance / variance
}

var workingPhrases = []string{
	"Analyzing dimensions...",
	"Calculating determinants...",
	"Consulting colleagues...",
	"Carrying the three...",
	"Reticulating splines...",
	"Raising powers...",
	"Questioning the axioms...",
	"Soothing rounding errors...",
	"Untangling eigenvectors...",
	"Checking under the radical...",
	"Waiting for convergence...",
	"Reassuring imaginary numbers...",
}
