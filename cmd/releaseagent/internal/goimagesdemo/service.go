// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesdemo implements the narrowly allowlisted real demo workflow for the official
// go-images pipeline. It cannot target any other definition, branch, commit, or parameter set.
package goimagesdemo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const (
	// DefinitionID is the microsoft-go-images (official) pipeline.
	DefinitionID = 1023
	// SourceBranch is the only source branch accepted by the demo service.
	SourceBranch = "refs/heads/microsoft/main"

	correlationVariable     = "ReleaseUIProductionDemoSessionID"
	executionDigestVariable = "ReleaseUIProductionDemoExecutionDigest"
	versionsVariable        = "ReleaseUIProductionDemoVersions"
	sourceBuildVariable     = "ReleaseUIProductionDemoSourceBuildID"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PipelineReader is the read-only Azure DevOps behavior required for reconciliation and polling.
type PipelineReader interface {
	Get(context.Context, int) (*azdopipeline.Build, error)
	ListRecent(context.Context, int) ([]*azdopipeline.Build, error)
}

// QueueClient can only queue the hardcoded production demo definition and payload.
type QueueClient interface {
	QueueProductionDemo(context.Context, QueueRequest) (int, error)
}

// QueueRequest contains immutable metadata for one allowlisted demo run.
type QueueRequest struct {
	SourceVersion   string
	SessionID       string
	ExecutionDigest string
	VersionSet      string
	SourceBuildID   string
}

// Config binds a demo run to an imported official run and durable session.
type Config struct {
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	PollInterval         time.Duration
	PreviousQueueAttempt bool
	ReconcileAttempts    int
	ReconcileInterval    time.Duration
}

// Sleeper waits between status checks and is replaceable in tests.
type Sleeper func(context.Context, time.Duration) error

// Service implements only the queue-and-monitor surface required by the focused DAG.
type Service struct {
	reader     PipelineReader
	queue      QueueClient
	config     Config
	versionSet string
	sleep      Sleeper
}

// New validates and creates a production demo service.
func New(reader PipelineReader, queue QueueClient, config Config, sleeper Sleeper) (*Service, error) {
	if reader == nil || queue == nil {
		return nil, errors.New("production demo pipeline reader and queue client are required")
	}
	if config.SessionID == "" || config.ExecutionDigest == "" {
		return nil, errors.New("production demo session ID and execution digest are required")
	}
	if !commitPattern.MatchString(config.SourceVersion) {
		return nil, fmt.Errorf("invalid production demo source commit %q", config.SourceVersion)
	}
	if id, err := strconv.Atoi(config.SourceBuildID); err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid production demo source build ID %q", config.SourceBuildID)
	}
	versionSet, err := goimagesrelease.CanonicalVersionSet(config.Versions)
	if err != nil {
		return nil, err
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.ReconcileAttempts <= 0 {
		config.ReconcileAttempts = 6
	}
	if config.ReconcileInterval <= 0 {
		config.ReconcileInterval = 5 * time.Second
	}
	if sleeper == nil {
		sleeper = sleepContext
	}
	return &Service{
		reader: reader, queue: queue, config: config, versionSet: versionSet, sleep: sleeper,
	}, nil
}

// TriggerBuildPipeline reconciles this session before queueing the hardcoded official pipeline.
func (s *Service) TriggerBuildPipeline(
	ctx context.Context,
	pipelineID int,
	parameters,
	optionalParameters map[string]string,
	_ *releasesteps.Secret,
) (string, error) {
	if pipelineID != DefinitionID {
		return "", fmt.Errorf("pipeline %d is not the allowlisted production demo definition %d", pipelineID, DefinitionID)
	}
	if len(optionalParameters) != 0 {
		return "", errors.New("production demo pipeline does not accept optional parameters")
	}
	if !maps.Equal(parameters, releasesteps.GoImagesProductionDemoPipelineParameters()) {
		return "", fmt.Errorf("production demo parameters are not allowlisted: %#v", parameters)
	}

	attempts := 1
	if s.config.PreviousQueueAttempt {
		attempts = s.config.ReconcileAttempts
	}
	for attempt := range attempts {
		build, err := s.findCorrelatedBuild(ctx)
		if err != nil {
			return "", err
		}
		if build != nil {
			return strconv.Itoa(build.ID), nil
		}
		if attempt+1 < attempts {
			if err := s.sleep(ctx, s.config.ReconcileInterval); err != nil {
				return "", err
			}
		}
	}

	buildID, err := s.queue.QueueProductionDemo(ctx, QueueRequest{
		SourceVersion:   s.config.SourceVersion,
		SessionID:       s.config.SessionID,
		ExecutionDigest: s.config.ExecutionDigest,
		VersionSet:      s.versionSet,
		SourceBuildID:   s.config.SourceBuildID,
	})
	if err != nil {
		return "", fmt.Errorf("queue production go-images demo: %w", err)
	}
	return strconv.Itoa(buildID), nil
}

func (s *Service) findCorrelatedBuild(ctx context.Context) (*azdopipeline.Build, error) {
	builds, err := s.reader.ListRecent(ctx, DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("reconcile production demo pipeline run: %w", err)
	}
	for _, build := range builds {
		if build.Parameters[correlationVariable] != s.config.SessionID {
			continue
		}
		if err := s.validateCorrelatedBuild(build); err != nil {
			return nil, err
		}
		return build, nil
	}
	return nil, nil
}

func (s *Service) validateCorrelatedBuild(build *azdopipeline.Build) error {
	if build == nil || build.ID <= 0 || build.DefinitionID != DefinitionID {
		return fmt.Errorf("correlated production demo build has invalid identity: %#v", build)
	}
	if build.SourceBranch != SourceBranch || build.SourceVersion != s.config.SourceVersion {
		return fmt.Errorf(
			"correlated production demo build %d has source %s@%s, expected %s@%s",
			build.ID, build.SourceBranch, build.SourceVersion, SourceBranch, s.config.SourceVersion,
		)
	}
	for name, want := range map[string]string{
		correlationVariable:     s.config.SessionID,
		executionDigestVariable: s.config.ExecutionDigest,
		versionsVariable:        s.versionSet,
		sourceBuildVariable:     s.config.SourceBuildID,
	} {
		if got := build.Parameters[name]; got != want {
			return fmt.Errorf("correlated production demo build %d has %s %q, expected %q", build.ID, name, got, want)
		}
	}
	for name, want := range releasesteps.GoImagesProductionDemoPipelineParameters() {
		if got, ok := templateParameterString(build.TemplateParameters[name]); !ok || got != want {
			return fmt.Errorf("correlated production demo build %d has parameter %s %q, expected %q", build.ID, name, got, want)
		}
	}
	return nil
}

func templateParameterString(value any) (string, bool) {
	valueString, ok := value.(string)
	return valueString, ok
}

// PollPipelineComplete waits for the correlated production run using read-only GET requests.
func (s *Service) PollPipelineComplete(ctx context.Context, buildID string, _ *releasesteps.Secret) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid production demo build ID %q", buildID)
	}
	for {
		build, err := s.reader.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("get production demo build %d: %w", id, err)
		}
		if build.DefinitionID != 0 && build.DefinitionID != DefinitionID {
			return fmt.Errorf("build %d belongs to pipeline %d, expected %d", id, build.DefinitionID, DefinitionID)
		}
		state, err := build.State()
		if err != nil {
			return fmt.Errorf("interpret production demo build %d: %w", id, err)
		}
		switch state {
		case azdopipeline.RunStateSucceeded:
			return nil
		case azdopipeline.RunStateFailed, azdopipeline.RunStateCanceled:
			return fmt.Errorf("production demo build %d finished with state %q and result %q: %s", id, state, build.Result, build.WebURL)
		case azdopipeline.RunStateWaiting, azdopipeline.RunStateRunning:
			if err := s.sleep(ctx, s.config.PollInterval); err != nil {
				return err
			}
		default:
			return fmt.Errorf("production demo build %d has unsupported state %q", id, state)
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
