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
	SchemaVersion  int       `json:"schemaVersion"`
	Initialized    bool      `json:"initialized"`
	WorkACompleted int       `json:"workACompleted"`
	WorkBCompleted int       `json:"workBCompleted"`
	Finished       bool      `json:"finished"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type run struct {
	mu    sync.RWMutex
	input input
	name  string
	state state
}

func newRun(input input, name string, state state) *run {
	return &run{input: input, name: name, state: state}
}

func (r *run) TakeSnapshot() *contract.StateSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot, err := encodeSnapshot(r.input, r.state)
	if err != nil {
		panic(fmt.Sprintf("encode demo snapshot: %v", err))
	}
	return snapshot
}

func (r *run) TakeView() *contract.RunView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	summary := "Ready"
	if r.state.Finished {
		summary = "Demo completed"
	} else if r.state.Initialized {
		summary = "Simulated work in progress"
	}
	return &contract.RunView{
		Test: true, Summary: summary, UpdatedAt: r.state.UpdatedAt,
		Detail: fmt.Sprintf("%s: work A %d/%d units, work B %d/%d units",
			r.name, r.state.WorkACompleted, workUnits, r.state.WorkBCompleted, workUnits),
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
		return r.work(ctx, &r.state.WorkACompleted, checkpoint)
	})
	workB := initialize.Then("Fake work B", stepTimeout, func(ctx context.Context) error {
		return r.work(ctx, &r.state.WorkBCompleted, checkpoint)
	})
	finish := workA.Then("Finish demo", stepTimeout, func(ctx context.Context) error {
		return r.finish(ctx, checkpoint)
	}, workB)
	return finish.TransitiveDependencies()
}

func (r *run) initialize(ctx context.Context, checkpoint contract.CheckpointFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	initialized := r.state.Initialized
	if !initialized {
		r.state.Initialized = true
		r.state.UpdatedAt = time.Now().UTC()
	}
	r.mu.Unlock()
	if initialized {
		return nil
	}
	return saveCheckpoint(ctx, checkpoint)
}

func (r *run) work(ctx context.Context, completed *int, checkpoint contract.CheckpointFunc) error {
	r.mu.RLock()
	current := *completed
	r.mu.RUnlock()
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
		r.mu.Lock()
		current++
		*completed = current
		r.state.UpdatedAt = time.Now().UTC()
		r.mu.Unlock()
		if current%checkpointUnits == 0 || current == workUnits {
			if err := saveCheckpoint(ctx, checkpoint); err != nil {
				return err
			}
		}
	}
}

func (r *run) finish(ctx context.Context, checkpoint contract.CheckpointFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.state.WorkACompleted != workUnits || r.state.WorkBCompleted != workUnits {
		r.mu.Unlock()
		return errors.New("cannot finish demo before both branches complete")
	}
	finished := r.state.Finished
	if !finished {
		r.state.Finished = true
		r.state.UpdatedAt = time.Now().UTC()
	}
	r.mu.Unlock()
	if finished {
		return nil
	}
	return saveCheckpoint(ctx, checkpoint)
}

func saveCheckpoint(ctx context.Context, checkpoint contract.CheckpointFunc) error {
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
