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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesrelease"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const (
	// DefinitionID is the microsoft-go-images (official) pipeline.
	DefinitionID = 1023
	// SourceBranch is the only source branch accepted by the release service.
	SourceBranch = "refs/heads/microsoft/main"

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
	GetTimeline(context.Context, int) (*azdopipeline.Timeline, error)
	ListRecent(context.Context, int) ([]*azdopipeline.Build, error)
}

// QueueClient can only queue the hardcoded go-images definition and a mode-derived payload.
type QueueClient interface {
	QueueRelease(context.Context, QueueRequest) (int, error)
}

// QueueRequest contains immutable metadata for one allowlisted release run.
type QueueRequest struct {
	Mode            releasesteps.GoImagesReleaseMode
	SourceVersion   string
	SourceBuildID   string
	SessionID       string
	ExecutionDigest string
	VersionSet      string
}

// Config binds a release run to an exact source commit and durable session.
type Config struct {
	Mode                 releasesteps.GoImagesReleaseMode
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	VerifyMirrorCommit   func(context.Context, string) error
	MirrorPollInterval   time.Duration
	PollInterval         time.Duration
	TimelinePollInterval time.Duration
	PreviousQueueAttempt bool
	ReconcileAttempts    int
	ReconcileInterval    time.Duration
}

// Sleeper waits between status checks and is replaceable in tests.
type Sleeper func(context.Context, time.Duration) error

// Service implements only the queue-and-monitor surface required by the focused DAG.
type Service struct {
	reader            PipelineReader
	queue             QueueClient
	config            Config
	parameters        map[string]string
	versionSet        string
	sleep             Sleeper
	timelinePollEvery int
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
	parameters, err := releasesteps.GoImagesPipelineParametersForMode(config.Mode, config.SourceBuildID)
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
	if config.TimelinePollInterval <= 0 {
		config.TimelinePollInterval = 30 * time.Second
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
	timelinePollEvery := int((config.TimelinePollInterval + config.PollInterval - 1) / config.PollInterval)
	if timelinePollEvery < 1 {
		timelinePollEvery = 1
	}
	return &Service{
		reader: reader, queue: queue, config: config, parameters: parameters, versionSet: versionSet,
		sleep: sleeper, timelinePollEvery: timelinePollEvery,
	}, nil
}

// PollAzDOMirror waits until the plan's exact source commit is available in the allowlisted
// internal microsoft-go-images repository.
func (s *Service) PollAzDOMirror(
	ctx context.Context,
	target,
	commit string,
	_ *releasesteps.Secret,
) error {
	if target != releasesteps.GoImagesInternalMirrorTarget {
		return fmt.Errorf("go-images mirror target %q is not allowlisted", target)
	}
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

// TriggerBuildPipeline reconciles this session before queueing the hardcoded official pipeline.
func (s *Service) TriggerBuildPipeline(
	ctx context.Context,
	pipelineID int,
	parameters,
	optionalParameters map[string]string,
	_ *releasesteps.Secret,
) (string, error) {
	if pipelineID != DefinitionID {
		return "", fmt.Errorf("pipeline %d is not the allowlisted go-images definition %d", pipelineID, DefinitionID)
	}
	if len(optionalParameters) != 0 {
		return "", errors.New("go-images release pipeline does not accept optional parameters")
	}
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
	builds, err := s.reader.ListRecent(ctx, DefinitionID)
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
	if build == nil || build.ID <= 0 || build.DefinitionID != DefinitionID {
		return fmt.Errorf("correlated go-images release build has invalid identity: %#v", build)
	}
	if build.SourceBranch != SourceBranch || build.SourceVersion != s.config.SourceVersion {
		return fmt.Errorf(
			"correlated go-images release build %d has source %s@%s, expected %s@%s",
			build.ID, build.SourceBranch, build.SourceVersion, SourceBranch, s.config.SourceVersion,
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

// PollPipelineComplete waits for the correlated release run using read-only GET requests.
func (s *Service) PollPipelineComplete(ctx context.Context, buildID string, _ *releasesteps.Secret) error {
	id, err := strconv.Atoi(buildID)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid go-images release build ID %q", buildID)
	}
	pollsUntilTimeline := 0
	for {
		build, err := s.reader.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("get go-images release build %d: %w", id, err)
		}
		if build.DefinitionID != 0 && build.DefinitionID != DefinitionID {
			return fmt.Errorf("build %d belongs to pipeline %d, expected %d", id, build.DefinitionID, DefinitionID)
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
			if pollsUntilTimeline == 0 {
				s.reportPipelineProgress(ctx, id, state)
				pollsUntilTimeline = s.timelinePollEvery
			}
			pollsUntilTimeline--
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
		progress.Detail = fmt.Sprintf("Reading live stage details for build %d", buildID)
	}
	timeline, err := s.reader.GetTimeline(ctx, buildID)
	if err == nil && timeline != nil {
		progress = timelineStepProgress(buildID, state, timeline.Records)
	} else if state == azdopipeline.RunStateRunning {
		progress.Detail = "Detailed Azure stage status is not available yet"
	}
	coordinator.ReportProgress(ctx, progress)
}

func timelineStepProgress(
	buildID int,
	state azdopipeline.RunState,
	records []azdopipeline.TimelineRecord,
) coordinator.StepProgress {
	progress := coordinator.StepProgress{
		Summary: "Azure pipeline is running",
		Detail:  fmt.Sprintf("Build %d is running", buildID),
	}
	if state == azdopipeline.RunStateWaiting {
		progress.Summary = "Azure pipeline is queued"
		progress.Detail = fmt.Sprintf("Build %d is waiting to start", buildID)
	}
	byID := make(map[string]azdopipeline.TimelineRecord, len(records))
	byType := make(map[string][]azdopipeline.TimelineRecord)
	for _, record := range records {
		byID[record.ID] = record
		typeName := strings.ToLower(record.Type)
		switch typeName {
		case "stage", "job", "task":
			byType[typeName] = append(byType[typeName], record)
		}
	}

	var countDetails []string
	for _, typeName := range []string{"stage", "job", "task"} {
		total := len(byType[typeName])
		if total == 0 {
			continue
		}
		completed := 0
		for _, record := range byType[typeName] {
			if strings.EqualFold(record.State, "completed") {
				completed++
			}
		}
		countDetails = append(countDetails, fmt.Sprintf("%d/%d %ss", completed, total, typeName))
		if typeName == "stage" {
			progress.Completed = completed
			progress.Total = total
		}
	}
	if len(countDetails) != 0 {
		progress.Detail = fmt.Sprintf("Build %d · %s complete", buildID, strings.Join(countDetails, " · "))
	}

	activeType := ""
	var active []azdopipeline.TimelineRecord
	for _, typeName := range []string{"task", "job", "stage"} {
		for _, record := range byType[typeName] {
			if strings.EqualFold(record.State, "inProgress") {
				active = append(active, record)
			}
		}
		if len(active) != 0 {
			activeType = typeName
			break
		}
	}
	sort.SliceStable(active, func(left, right int) bool {
		if active[left].Order != active[right].Order {
			return active[left].Order < active[right].Order
		}
		return active[left].Name < active[right].Name
	})

	seen := make(map[string]struct{}, len(active))
	const maxItems = 8
	for _, record := range active {
		path := timelineRecordPath(record, byID)
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		if len(progress.Items) < maxItems {
			progress.Items = append(progress.Items, path)
		}
	}
	if len(seen) == 1 && len(active) != 0 {
		progress.Summary = "Running: " + active[0].Name
	} else if len(seen) > 1 {
		progress.Summary = fmt.Sprintf("Running %d pipeline %ss in parallel", len(seen), activeType)
	}
	if hidden := len(seen) - len(progress.Items); hidden > 0 {
		progress.Items = append(progress.Items, fmt.Sprintf("… and %d more active %ss", hidden, activeType))
	}
	return progress
}

func timelineRecordPath(
	record azdopipeline.TimelineRecord,
	byID map[string]azdopipeline.TimelineRecord,
) string {
	var reversed []string
	visited := make(map[string]struct{})
	for current := record; current.ID != ""; {
		if _, exists := visited[current.ID]; exists {
			break
		}
		visited[current.ID] = struct{}{}
		switch strings.ToLower(current.Type) {
		case "stage", "job", "task":
			if current.Name != "" {
				reversed = append(reversed, current.Name)
			}
		}
		parent, exists := byID[current.ParentID]
		if !exists {
			break
		}
		current = parent
	}
	path := make([]string, len(reversed))
	for index := range reversed {
		path[len(reversed)-1-index] = reversed[index]
	}
	return strings.Join(path, " › ")
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
