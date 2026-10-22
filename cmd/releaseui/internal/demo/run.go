// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package demo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const (
	stateSchema     = 1
	workUnits       = 30
	workDelay       = time.Second
	checkpointUnits = 5
	stepTimeout     = 2 * time.Minute
)

type state struct {
	SchemaVersion int       `json:"schemaVersion"`
	Initialized   bool      `json:"initialized"`
	WorkCompleted [2]int    `json:"workCompleted"`
	Finished      bool      `json:"finished"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type run struct {
	mu    sync.RWMutex
	name  string
	state state
}

func newRun(name string, state state) *run {
	return &run{name: name, state: state}
}

func (r *run) TakeSnapshot() *contract.StateSnapshot {
	snapshot, err := encodeSnapshot(r.readState())
	if err != nil {
		panic(fmt.Sprintf("encode demo snapshot: %v", err))
	}
	return snapshot
}

func (r *run) TakeView() *contract.RunView {
	state := r.readState()
	summary := "Ready"
	if state.Finished {
		summary = "Demo completed"
	} else if state.Initialized {
		summary = "Simulated work in progress"
	}
	return &contract.RunView{
		Test: true, Summary: summary, UpdatedAt: state.UpdatedAt,
		Detail: fmt.Sprintf("%s: work A %d/%d units, work B %d/%d units",
			r.name, state.WorkCompleted[0], workUnits, state.WorkCompleted[1], workUnits),
	}
}

func (r *run) Plan() *contract.Plan {
	return &contract.Plan{
		Subtitle: r.name + " simulated release", ExecutionButtonLabel: "Run demo",
		Facts: []contract.PlanFact{
			{Label: "Process", Value: r.name},
			{Label: "Work", Value: fmt.Sprintf("Two parallel branches, %d units each", workUnits)},
			{Label: "Timing", Value: workDelay.String() + " per unit, plus checkpoint storage time"},
			{Label: "Tracking", Value: "Creates and updates a real Azure DevOps work item tagged test"},
			{Label: "External actions", Value: "No builds are queued, workflows dispatched, or artifacts published"},
		},
	}
}

func (r *run) Build(_ context.Context, checkpoint contract.CheckpointFunc) ([]*coordinator.Step, error) {
	initialize := coordinator.NewRootStep("Initialize demo", stepTimeout, func(ctx context.Context) error {
		return r.initialize(ctx, checkpoint)
	})
	workA := initialize.Then("Fake work A", stepTimeout, func(ctx context.Context) error {
		return r.work(ctx, 0, checkpoint)
	})
	workB := initialize.Then("Fake work B", stepTimeout, func(ctx context.Context) error {
		return r.work(ctx, 1, checkpoint)
	})
	finish := workA.Then("Finish demo", stepTimeout, func(ctx context.Context) error {
		return r.finish(ctx, checkpoint)
	}, workB)
	return finish.TransitiveDependencies()
}

func (r *run) initialize(ctx context.Context, checkpoint contract.CheckpointFunc) error {
	if r.readState().Initialized {
		return ctx.Err()
	}
	return r.update(ctx, checkpoint, func(state *state) { state.Initialized = true })
}

func (r *run) work(ctx context.Context, branch int, checkpoint contract.CheckpointFunc) error {
	current := r.readState().WorkCompleted[branch]
	for {
		coordinator.ReportProgress(ctx, coordinator.StepProgress{
			Summary: "Simulated work", Detail: fmt.Sprintf("%d of %d units complete", current, workUnits),
			Completed: current, Total: workUnits,
		})
		if current == workUnits {
			return ctx.Err()
		}
		if err := wait(ctx, workDelay); err != nil {
			return err
		}
		current++
		var save contract.CheckpointFunc
		if current%checkpointUnits == 0 || current == workUnits {
			save = checkpoint
		}
		if err := r.update(ctx, save, func(state *state) { state.WorkCompleted[branch] = current }); err != nil {
			return err
		}
	}
}

func (r *run) finish(ctx context.Context, checkpoint contract.CheckpointFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	saved := r.readState()
	if saved.WorkCompleted != [2]int{workUnits, workUnits} {
		return errors.New("cannot finish demo before both branches complete")
	}
	if saved.Finished {
		return ctx.Err()
	}
	return r.update(ctx, checkpoint, func(state *state) { state.Finished = true })
}

func (r *run) readState() state {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *run) update(ctx context.Context, checkpoint contract.CheckpointFunc, change func(*state)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		change(&r.state)
		r.state.UpdatedAt = time.Now().UTC()
	}()
	if checkpoint != nil {
		checkpoint()
	}
	return ctx.Err()
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
