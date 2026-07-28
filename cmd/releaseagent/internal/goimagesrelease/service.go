// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesrelease adapts the generic Azure DevOps client to the focused go-images release
// pipeline workflow.
package goimagesrelease

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

// CorrelationVariable is attached to queued builds so a restart can find an already-created run.
const CorrelationVariable = "ReleaseUISessionID"

// PipelineClient is the Azure DevOps behavior needed by Service.
type PipelineClient interface {
	Queue(context.Context, azdopipeline.QueueRequest) (*azdopipeline.Build, error)
	Get(context.Context, int) (*azdopipeline.Build, error)
	FindLatestByVariable(context.Context, int, string, string) (*azdopipeline.Build, error)
}

// Sleeper waits between status checks and is replaceable in tests.
type Sleeper func(context.Context, time.Duration) error

// Config fixes the target and correlation identity outside browser-controlled input.
type Config struct {
	DefinitionID  int
	SessionID     string
	SourceBranch  string
	SourceVersion string
	PollInterval  time.Duration
}

// Service implements the two-method go-images workflow boundary.
type Service struct {
	client PipelineClient
	config Config
	sleep  Sleeper
}

// New validates and creates a focused pipeline service.
func New(client PipelineClient, config Config, sleeper Sleeper) (*Service, error) {
	if client == nil {
		return nil, errors.New("go-images pipeline client is nil")
	}
	if config.DefinitionID <= 0 {
		return nil, errors.New("go-images pipeline definition ID must be positive")
	}
	if config.SessionID == "" {
		return nil, errors.New("release UI session ID is required")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if sleeper == nil {
		sleeper = sleepContext
	}
	return &Service{client: client, config: config, sleep: sleeper}, nil
}

// TriggerBuildPipeline reconciles an existing correlated run before queueing a new one.
func (s *Service) TriggerBuildPipeline(
	ctx context.Context,
	pipelineID int,
	parameters,
	optionalParameters map[string]string,
	_ *releasesteps.Secret,
) (string, error) {
	if pipelineID != s.config.DefinitionID {
		return "", fmt.Errorf("pipeline %d is not allowlisted; expected %d", pipelineID, s.config.DefinitionID)
	}
	if len(optionalParameters) != 0 {
		return "", errors.New("go-images release pipeline does not accept optional parameters")
	}
	existing, err := s.client.FindLatestByVariable(
		ctx,
		s.config.DefinitionID,
		CorrelationVariable,
		s.config.SessionID,
	)
	if err != nil {
		return "", fmt.Errorf("reconcile existing go-images pipeline run: %w", err)
	}
	if existing != nil {
		return strconv.Itoa(existing.ID), nil
	}
	run, err := s.client.Queue(ctx, azdopipeline.QueueRequest{
		DefinitionID:  s.config.DefinitionID,
		SourceBranch:  s.config.SourceBranch,
		SourceVersion: s.config.SourceVersion,
		Parameters:    parameters,
		Variables: map[string]string{
			CorrelationVariable: s.config.SessionID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("queue go-images release pipeline: %w", err)
	}
	return strconv.Itoa(run.ID), nil
}

// PollPipelineComplete waits until the run succeeds or reaches a terminal failure state.
func (s *Service) PollPipelineComplete(ctx context.Context, buildID string, _ *releasesteps.Secret) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid Azure DevOps build ID %q", buildID)
	}
	for {
		run, err := s.client.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("get go-images pipeline run %d: %w", id, err)
		}
		state, err := run.State()
		if err != nil {
			return fmt.Errorf("interpret go-images pipeline run %d: %w", id, err)
		}
		switch state {
		case azdopipeline.RunStateSucceeded:
			return nil
		case azdopipeline.RunStateFailed, azdopipeline.RunStateCanceled:
			return fmt.Errorf("go-images pipeline run %d finished with state %q and result %q: %s", id, state, run.Result, run.WebURL)
		case azdopipeline.RunStateWaiting, azdopipeline.RunStateRunning:
			if err := s.sleep(ctx, s.config.PollInterval); err != nil {
				return err
			}
		default:
			return fmt.Errorf("go-images pipeline run %d has unsupported state %q", id, state)
		}
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ releasesteps.GoImagesReleaseService = (*Service)(nil)
