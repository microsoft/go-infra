// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesexecution implements the narrowly allowlisted execution workflow for the
// official go-images pipeline. It supports normal, rollback, and test releases and cannot target
// another definition, branch, commit, or arbitrary parameter set.
package goimagesexecution

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

const (
	correlationVariable     = "ReleaseUISessionID"
	executionDigestVariable = "ReleaseUIExecutionDigest"
	modeVariable            = "ReleaseUIGoImagesMode"
	versionsVariable        = "ReleaseUIGoImagesVersions"
	sourceBuildVariable     = "ReleaseUIGoImagesSourceBuildID"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PipelineReader is the read-only Azure DevOps behavior required for reconciliation and polling.
type PipelineReader interface {
	Get(context.Context, int) (*azdopipeline.Build, error)
	ListRecent(context.Context, int) ([]*azdopipeline.Build, error)
}

// QueueClient can only queue the hardcoded go-images definition and a mode-derived payload.
type QueueClient interface {
	QueueRelease(context.Context, QueueRequest) (int, error)
}

// QueueRequest contains immutable metadata for one allowlisted release run.
type QueueRequest struct {
	Mode            goimagesworkflow.Mode
	SourceVersion   string
	SourceBuildID   string
	SessionID       string
	ExecutionDigest string
	VersionSet      string
}

// Config binds a release run to an exact source commit and durable session.
type Config struct {
	Mode                 goimagesworkflow.Mode
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	VerifyMirrorCommit   func(context.Context, string) error
	MirrorPollInterval   time.Duration
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
	parameters map[string]string
	versionSet string
	sleep      Sleeper
}

// New validates and creates a go-images release service.
func New(reader PipelineReader, queue QueueClient, config Config, sleeper Sleeper) (*Service, error) {
	if reader == nil || queue == nil {
		return nil, errors.New("go-images pipeline reader and queue client are required")
	}
	if config.SessionID == "" || config.ExecutionDigest == "" {
		return nil, errors.New("go-images release session ID and execution digest are required")
	}
	if !commitPattern.MatchString(config.SourceVersion) {
		return nil, fmt.Errorf("invalid go-images release source commit %q", config.SourceVersion)
	}
	if config.VerifyMirrorCommit == nil {
		return nil, errors.New("go-images internal mirror verifier is required")
	}
	parameters, err := goimagesworkflow.PipelineParameters(config.Mode, config.SourceBuildID)
	if err != nil {
		return nil, err
	}
	versionSet, err := goimagesrelease.CanonicalVersionSet(config.Versions)
	if err != nil {
		return nil, err
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.MirrorPollInterval <= 0 {
		config.MirrorPollInterval = 5 * time.Second
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
		reader: reader, queue: queue, config: config, parameters: parameters, versionSet: versionSet,
		sleep: sleeper,
	}, nil
}

// PollMirror waits until the plan's exact source commit is available in the allowlisted
// internal microsoft-go-images repository.
func (s *Service) PollMirror(ctx context.Context, commit string) error {
	if commit != s.config.SourceVersion {
		return fmt.Errorf("go-images mirror commit %q does not match planned source %q", commit, s.config.SourceVersion)
	}
	for {
		verifyErr := s.config.VerifyMirrorCommit(ctx, commit)
		if verifyErr == nil {
			coordinator.ReportProgress(ctx, coordinator.StepProgress{
				Summary: "Source commit is available in the internal mirror",
				Detail:  commit,
			})
			return nil
		}
		coordinator.ReportProgress(ctx, coordinator.StepProgress{
			Summary: "Waiting for source commit in the internal mirror",
			Detail:  commit,
		})
		if err := s.sleep(ctx, s.config.MirrorPollInterval); err != nil {
			return fmt.Errorf("wait for go-images commit %s in the internal mirror: last check: %v: %w", commit, verifyErr, err)
		}
	}
}

// QueuePipeline reconciles this session before queueing the hardcoded official pipeline.
func (s *Service) QueuePipeline(
	ctx context.Context,
	parameters map[string]string,
) (string, error) {
	if !maps.Equal(parameters, s.parameters) {
		return "", fmt.Errorf("go-images release parameters are not allowlisted: %#v", parameters)
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

	buildID, err := s.queue.QueueRelease(ctx, QueueRequest{
		Mode:            s.config.Mode,
		SourceVersion:   s.config.SourceVersion,
		SourceBuildID:   s.config.SourceBuildID,
		SessionID:       s.config.SessionID,
		ExecutionDigest: s.config.ExecutionDigest,
		VersionSet:      s.versionSet,
	})
	if err != nil {
		return "", fmt.Errorf("queue go-images %s release: %w", s.config.Mode, err)
	}
	return strconv.Itoa(buildID), nil
}

func (s *Service) findCorrelatedBuild(ctx context.Context) (*azdopipeline.Build, error) {
	builds, err := s.reader.ListRecent(ctx, goimagesworkflow.DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("reconcile go-images release pipeline run: %w", err)
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
	if build == nil || build.ID <= 0 || build.DefinitionID != goimagesworkflow.DefinitionID {
		return fmt.Errorf("correlated go-images release build has invalid identity: %#v", build)
	}
	if build.SourceBranch != goimagesworkflow.SourceBranch || build.SourceVersion != s.config.SourceVersion {
		return fmt.Errorf(
			"correlated go-images release build %d has source %s@%s, expected %s@%s",
			build.ID, build.SourceBranch, build.SourceVersion, goimagesworkflow.SourceBranch, s.config.SourceVersion,
		)
	}
	for name, want := range map[string]string{
		correlationVariable:     s.config.SessionID,
		executionDigestVariable: s.config.ExecutionDigest,
		modeVariable:            string(s.config.Mode),
		versionsVariable:        s.versionSet,
		sourceBuildVariable:     s.config.SourceBuildID,
	} {
		if got := build.Parameters[name]; got != want {
			return fmt.Errorf("correlated go-images release build %d has %s %q, expected %q", build.ID, name, got, want)
		}
	}
	for name, want := range s.parameters {
		if got, ok := templateParameterString(build.TemplateParameters[name]); !ok || got != want {
			return fmt.Errorf("correlated go-images release build %d has parameter %s %q, expected %q", build.ID, name, got, want)
		}
	}
	return nil
}

func templateParameterString(value any) (string, bool) {
	valueString, ok := value.(string)
	return valueString, ok
}

// PollPipeline waits for the correlated release run using read-only GET requests.
func (s *Service) PollPipeline(ctx context.Context, buildID string) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid go-images release build ID %q", buildID)
	}
	for {
		build, err := s.reader.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("get go-images release build %d: %w", id, err)
		}
		if build.DefinitionID != goimagesworkflow.DefinitionID {
			return fmt.Errorf("build %d belongs to pipeline %d, expected %d", id, build.DefinitionID, goimagesworkflow.DefinitionID)
		}
		state, err := build.State()
		if err != nil {
			return fmt.Errorf("interpret go-images release build %d: %w", id, err)
		}
		switch state {
		case azdopipeline.RunStateSucceeded:
			coordinator.ReportProgress(ctx, coordinator.StepProgress{
				Summary:   "Azure pipeline completed successfully",
				Detail:    fmt.Sprintf("Build %d completed", id),
				Completed: 1,
				Total:     1,
			})
			return nil
		case azdopipeline.RunStateFailed, azdopipeline.RunStateCanceled:
			coordinator.ReportProgress(ctx, coordinator.StepProgress{
				Summary: fmt.Sprintf("Azure pipeline %s", state),
				Detail:  fmt.Sprintf("Build %d finished with result %s", id, build.Result),
			})
			return fmt.Errorf("go-images release build %d finished with state %q and result %q: %s", id, state, build.Result, build.WebURL)
		case azdopipeline.RunStateWaiting, azdopipeline.RunStateRunning:
			s.reportPipelineProgress(ctx, id, state)
			if err := s.sleep(ctx, s.config.PollInterval); err != nil {
				return err
			}
		default:
			return fmt.Errorf("go-images release build %d has unsupported state %q", id, state)
		}
	}
}

func (s *Service) reportPipelineProgress(ctx context.Context, buildID int, state azdopipeline.RunState) {
	progress := coordinator.StepProgress{
		Summary: "Azure pipeline is queued",
		Detail:  fmt.Sprintf("Waiting for build %d to start", buildID),
	}
	if state == azdopipeline.RunStateRunning {
		progress.Summary = "Azure pipeline is running"
		progress.Detail = fmt.Sprintf("Build %d is in progress", buildID)
	}
	coordinator.ReportProgress(ctx, progress)
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

var _ goimagesworkflow.Service = (*Service)(nil)
