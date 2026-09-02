// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesworkflow defines the focused standalone go-images release workflow.
package goimagesworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
)

// Mode identifies one explicitly allowlisted use of pipeline 1023.
type Mode string

const (
	// InternalMirrorTarget is the only Azure Repos mirror accepted by this workflow.
	InternalMirrorTarget = "dnceng/internal/_git/microsoft-go-images"
	// ModeNormal builds current microsoft/main and publishes to public/.
	ModeNormal Mode = "normal"
	// ModeRollback republishes artifacts from one prior successful build to public/.
	ModeRollback Mode = "rollback"
	// ModeTest builds current microsoft/main and publishes under dev/.
	ModeTest Mode = "test"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Input is the immutable identity of one standalone go-images release.
type Input struct {
	Versions      []string
	Mode          Mode
	SourceBranch  string
	SourceVersion string
	SourceBuildID string
	MirrorTarget  string
	PipelineID    int
}

func (input Input) checksum() (uint32, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	return crc32.ChecksumIEEE(data), nil
}

// State is the durable execution state of one standalone go-images release.
type State struct {
	InputChecksum  uint32
	BuildID        string
	Result         string
	Complete       bool
	QueueAttempted bool
}

// Service is the complete external surface available to the standalone go-images workflow.
type Service interface {
	PollMirror(context.Context, string, string) error
	QueuePipeline(context.Context, int, map[string]string) (string, error)
	PollPipeline(context.Context, string) error
}

// CheckpointFunc durably records State. The state pointer is valid only during the call.
type CheckpointFunc func(context.Context, *State) error

type stateAccess struct {
	mu         sync.Mutex
	state      *State
	checkpoint CheckpointFunc
	dirty      bool
}

func (access *stateAccess) update(ctx context.Context, update func(*State)) error {
	access.mu.Lock()
	defer access.mu.Unlock()
	update(access.state)
	if access.checkpoint == nil {
		return nil
	}
	access.dirty = true
	return access.flushLocked(ctx)
}

func (access *stateAccess) flush(ctx context.Context) error {
	access.mu.Lock()
	defer access.mu.Unlock()
	return access.flushLocked(ctx)
}

func (access *stateAccess) flushLocked(ctx context.Context) error {
	if !access.dirty || access.checkpoint == nil {
		return nil
	}
	if err := access.checkpoint(ctx, access.state); err != nil {
		return err
	}
	access.dirty = false
	return nil
}

func stateValue[T any](access *stateAccess, value func(*State) T) T {
	access.mu.Lock()
	defer access.mu.Unlock()
	return value(access.state)
}

const (
	shortTimeout            = 10 * time.Minute
	internalMirrorTimeout   = 16 * time.Minute
	officialPipelineTimeout = 2 * time.Hour
)

// PipelineParameters derives the complete pipeline parameter set from an allowlisted mode.
func PipelineParameters(mode Mode, sourceBuildID string) (map[string]string, error) {
	parameters := map[string]string{
		"sourceBuildPipelineRunId": "$(Build.BuildId)",
		"publishRepoPrefix":        "public/",
	}
	switch mode {
	case ModeNormal:
		if sourceBuildID != "" {
			return nil, fmt.Errorf("normal go-images release must not specify source build %q", sourceBuildID)
		}
	case ModeRollback:
		buildID, err := strconv.Atoi(sourceBuildID)
		if err != nil || buildID <= 0 {
			return nil, fmt.Errorf("rollback source build ID %q must be a positive integer", sourceBuildID)
		}
		parameters["sourceBuildPipelineRunId"] = sourceBuildID
	case ModeTest:
		if sourceBuildID != "" {
			return nil, fmt.Errorf("test go-images release must not specify source build %q", sourceBuildID)
		}
		parameters["publishRepoPrefix"] = "dev/"
	default:
		return nil, fmt.Errorf("unsupported go-images release mode %q", mode)
	}
	return parameters, nil
}

// NewGraphWithCheckpoint creates the workflow and checkpoints mutation intent and results.
func NewGraphWithCheckpoint(
	input *Input,
	state *State,
	service Service,
	checkpoint CheckpointFunc,
) ([]*coordinator.Step, *State, error) {
	if input == nil || input.PipelineID == 0 {
		return nil, nil, fmt.Errorf("no go-images pipeline specified")
	}
	if input.SourceBranch != "refs/heads/microsoft/main" {
		return nil, nil, fmt.Errorf("go-images source branch %q is not allowlisted", input.SourceBranch)
	}
	if input.MirrorTarget != InternalMirrorTarget {
		return nil, nil, fmt.Errorf("go-images mirror target %q is not allowlisted", input.MirrorTarget)
	}
	if !commitPattern.MatchString(input.SourceVersion) {
		return nil, nil, fmt.Errorf("invalid go-images source commit %q", input.SourceVersion)
	}
	parameters, err := PipelineParameters(input.Mode, input.SourceBuildID)
	if err != nil {
		return nil, nil, err
	}
	if state == nil {
		state, err = NewState(input)
	} else {
		state, err = validateState(input, state)
	}
	if err != nil {
		return nil, nil, err
	}
	access := &stateAccess{state: state, checkpoint: checkpoint}

	verifyMirror := coordinator.NewRootStep(
		"Verify go-images commit is mirrored internally",
		internalMirrorTimeout,
		func(ctx context.Context) error {
			if stateValue(access, func(state *State) string { return state.BuildID }) != "" {
				return nil
			}
			return service.PollMirror(ctx, input.MirrorTarget, input.SourceVersion)
		},
	)
	queue := verifyMirror.Then(
		"🚀 Queue go-images release",
		shortTimeout,
		func(ctx context.Context) error {
			if stateValue(access, func(state *State) string { return state.BuildID }) != "" {
				return nil
			}
			if !stateValue(access, func(state *State) bool { return state.QueueAttempted }) {
				if err := access.update(ctx, func(state *State) { state.QueueAttempted = true }); err != nil {
					return err
				}
			}
			buildID, err := service.QueuePipeline(ctx, input.PipelineID, parameters)
			if err != nil {
				return err
			}
			return access.update(ctx, func(state *State) {
				state.BuildID = buildID
			})
		},
	)
	wait := queue.Then(
		"⌚ Wait for go-images release",
		officialPipelineTimeout,
		func(ctx context.Context) error {
			if stateValue(access, func(state *State) bool { return state.Complete }) {
				return nil
			}
			buildID := stateValue(access, func(state *State) string { return state.BuildID })
			if err := service.PollPipeline(ctx, buildID); err != nil {
				return err
			}
			return access.update(ctx, func(state *State) {
				state.Complete = true
				state.Result = "succeeded"
			})
		},
	)
	complete := coordinator.NewIndicatorStep("✅ Go-images release complete", wait)
	steps, err := complete.TransitiveDependencies()
	if err != nil {
		return nil, nil, err
	}
	wrapStepsWithStateFlush(steps, access, checkpoint)
	return steps, state, nil
}

// NewState creates empty durable state bound to input.
func NewState(input *Input) (*State, error) {
	if input == nil || len(input.Versions) == 0 {
		return nil, fmt.Errorf("no versions to release")
	}
	checksum, err := input.checksum()
	if err != nil {
		return nil, fmt.Errorf("checksum go-images input: %w", err)
	}
	return &State{InputChecksum: checksum}, nil
}

func validateState(input *Input, state *State) (*State, error) {
	initialized, err := NewState(input)
	if err != nil {
		return nil, err
	}
	checksum := initialized.InputChecksum
	if state.InputChecksum != checksum {
		return nil, fmt.Errorf("go-images input does not match initial input: expected checksum %v, got %v", state.InputChecksum, checksum)
	}
	return state, nil
}

func wrapStepsWithStateFlush(steps []*coordinator.Step, state *stateAccess, checkpoint CheckpointFunc) {
	if checkpoint == nil {
		return
	}
	for _, step := range steps {
		run := step.Func
		step.Func = func(ctx context.Context) error {
			if err := state.flush(ctx); err != nil {
				return fmt.Errorf("flush pending go-images state before step: %w", err)
			}
			return run(ctx)
		}
	}
}
