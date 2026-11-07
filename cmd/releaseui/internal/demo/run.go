package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/snapshot"
)

const (
	workUnits       = 30
	workDelay       = time.Second
	checkpointUnits = 5
	stepTimeout     = 2 * time.Minute
)

type run struct {
	name  string
	state state
}

func (r *run) TakeSnapshot() (*contract.StateSnapshot, error) {
	return snapshot.Encode(struct{}{}, r.state)
}

func (r *run) TakeView() *contract.RunView {
	state := r.state
	summary := "Ready"
	if state.WorkCompleted == [2]int{workUnits, workUnits} {
		summary = "Simulated work completed"
	} else if state.WorkCompleted != [2]int{} {
		summary = "Simulated work in progress"
	}
	return &contract.RunView{
		Test: true, Summary: summary,
		Detail: fmt.Sprintf("%s: work A %d/%d units, work B %d/%d units",
			r.name, state.WorkCompleted[0], workUnits, state.WorkCompleted[1], workUnits),
	}
}

func (r *run) Plan() *contract.Plan {
	return &contract.Plan{
		Subtitle:             r.name + " simulated release",
		ExecutionButtonLabel: "Run demo",
		Facts: []contract.PlanFact{
			{Label: "Process", Value: r.name},
			{Label: "Work", Value: fmt.Sprintf("Two parallel branches, %d units each", workUnits)},
			{Label: "Timing", Value: workDelay.String() + " per unit, plus checkpoint storage time"},
			{Label: "Tracking", Value: "Creates and updates a real Azure DevOps work item tagged test"},
			{Label: "External actions", Value: "No builds are queued, workflows dispatched, or artifacts published"},
		},
	}
}

func (r *run) Build() ([]*coordinator.Step, error) {
	workA := coordinator.NewRootStep(
		"Fake work A",
		stepTimeout,
		func(ctx context.Context, ctl coordinator.StepControl) error {
			return r.work(ctx, ctl, 0)
		},
	)
	workB := coordinator.NewRootStep(
		"Fake work B",
		stepTimeout,
		func(ctx context.Context, ctl coordinator.StepControl) error {
			return r.work(ctx, ctl, 1)
		},
	)
	finish := coordinator.All(workA, workB).Then(
		"Finish demo",
		stepTimeout,
		func(ctx context.Context, _ coordinator.StepControl) error {
			if r.state.WorkCompleted != [2]int{workUnits, workUnits} {
				return errors.New("cannot finish demo before both branches complete")
			}
			return ctx.Err()
		},
	)
	return finish.TransitiveDependencies()
}

func (r *run) work(ctx context.Context, ctl coordinator.StepControl, branch int) error {
	current := r.state.WorkCompleted[branch]
	for {
		ctl.ReportProgress(coordinator.StepProgress{
			Summary:   "Simulated work",
			Detail:    fmt.Sprintf("%d of %d units complete", current, workUnits),
			Completed: current,
			Total:     workUnits,
		})
		if current == workUnits {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(workDelay):
		}
		current++
		if current%checkpointUnits == 0 || current == workUnits {
			if err := ctl.Update(func() {
				r.state.WorkCompleted[branch] = current
			}); err != nil {
				return err
			}
		}
	}
}
