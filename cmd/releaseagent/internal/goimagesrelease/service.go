// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesrelease adapts the generic Azure DevOps client to the focused go-images release
// pipeline workflow.
package goimagesrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const (
	// CorrelationVariable is attached so a restart can find an already-created run.
	CorrelationVariable = "ReleaseUISessionID"
	// WorkflowVariable identifies the release UI workflow that created a run.
	WorkflowVariable = "ReleaseUIWorkflow"
	// VersionsVariable stores the canonical version set for discovery.
	VersionsVariable = "ReleaseUIVersions"
	// ExecutionDigestVariable identifies the immutable confirmed request.
	ExecutionDigestVariable = "ReleaseUIExecutionDigest"
	// WorkflowID identifies this workflow in Azure run metadata.
	WorkflowID = "microsoft-go-images"
)

// PipelineClient is the Azure DevOps behavior needed by Service.
type PipelineClient interface {
	Queue(context.Context, azdopipeline.QueueRequest) (*azdopipeline.Build, error)
	Get(context.Context, int) (*azdopipeline.Build, error)
	FindLatestByVariable(context.Context, int, string, string) (*azdopipeline.Build, error)
	ListRecent(context.Context, int) ([]*azdopipeline.Build, error)
}

// Sleeper waits between status checks and is replaceable in tests.
type Sleeper func(context.Context, time.Duration) error

// Config fixes the target and correlation identity outside browser-controlled input.
type Config struct {
	DefinitionID    int
	SessionID       string
	Versions        []string
	ExecutionDigest string
	SourceBranch    string
	SourceVersion   string
	PollInterval    time.Duration
}

// Service implements the two-method go-images workflow boundary.
type Service struct {
	client     PipelineClient
	config     Config
	versionSet string
	sleep      Sleeper
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
	versionSet, err := CanonicalVersionSet(config.Versions)
	if err != nil {
		return nil, err
	}
	if config.ExecutionDigest == "" {
		return nil, errors.New("release UI execution digest is required")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if sleeper == nil {
		sleeper = sleepContext
	}
	return &Service{client: client, config: config, versionSet: versionSet, sleep: sleeper}, nil
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
		for name, want := range map[string]string{
			WorkflowVariable:        WorkflowID,
			VersionsVariable:        s.versionSet,
			ExecutionDigestVariable: s.config.ExecutionDigest,
		} {
			if got := existing.Parameters[name]; got != "" && got != want {
				return "", fmt.Errorf("correlated build %d has conflicting %s %q, expected %q", existing.ID, name, got, want)
			}
		}
		return strconv.Itoa(existing.ID), nil
	}
	run, err := s.client.Queue(ctx, azdopipeline.QueueRequest{
		DefinitionID:  s.config.DefinitionID,
		SourceBranch:  s.config.SourceBranch,
		SourceVersion: s.config.SourceVersion,
		Parameters:    parameters,
		Variables: map[string]string{
			CorrelationVariable:     s.config.SessionID,
			WorkflowVariable:        WorkflowID,
			VersionsVariable:        s.versionSet,
			ExecutionDigestVariable: s.config.ExecutionDigest,
		},
	})
	if err != nil {
		return "", fmt.Errorf("queue go-images release pipeline: %w", err)
	}
	return strconv.Itoa(run.ID), nil
}

// Candidate is a recent run matching a canonical version set.
type Candidate struct {
	BuildID         int
	Status          string
	Result          string
	State           azdopipeline.RunState
	URL             string
	QueueTime       time.Time
	SessionID       string
	VersionSet      string
	ExecutionDigest string
	CreatedByUI     bool
	Parameters      map[string]string
}

// FindCandidates finds recent runs matching the service's canonical version set. Runs created by
// older tooling are included when Azure exposes their releaseVersions template parameter.
func (s *Service) FindCandidates(ctx context.Context) ([]Candidate, error) {
	builds, err := s.client.ListRecent(ctx, s.config.DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("list recent go-images pipeline runs: %w", err)
	}
	want := s.versionSet
	var candidates []Candidate
	for _, build := range builds {
		versionSet, createdByUI, ok := buildVersionSet(build)
		if !ok || versionSet != want {
			continue
		}
		if workflow := build.Parameters[WorkflowVariable]; workflow != "" && workflow != WorkflowID {
			continue
		}
		state, err := build.State()
		if err != nil {
			return nil, fmt.Errorf("interpret candidate build %d: %w", build.ID, err)
		}
		candidates = append(candidates, Candidate{
			BuildID:         build.ID,
			Status:          build.Status,
			Result:          build.Result,
			State:           state,
			URL:             build.WebURL,
			QueueTime:       build.QueueTime,
			SessionID:       build.Parameters[CorrelationVariable],
			VersionSet:      versionSet,
			ExecutionDigest: build.Parameters[ExecutionDigestVariable],
			CreatedByUI:     createdByUI,
			Parameters:      stringifyTemplateParameters(build.TemplateParameters),
		})
	}
	return candidates, nil
}

// ValidateCandidate reloads one selected build and confirms that it matches the version set.
func (s *Service) ValidateCandidate(ctx context.Context, buildID int) (Candidate, error) {
	build, err := s.client.Get(ctx, buildID)
	if err != nil {
		return Candidate{}, fmt.Errorf("get candidate build %d: %w", buildID, err)
	}
	if build.DefinitionID != s.config.DefinitionID {
		return Candidate{}, fmt.Errorf("build %d belongs to pipeline %d, expected %d", buildID, build.DefinitionID, s.config.DefinitionID)
	}
	versionSet, createdByUI, ok := buildVersionSet(build)
	if !ok || versionSet != s.versionSet {
		return Candidate{}, fmt.Errorf("build %d does not match version set %s", buildID, s.versionSet)
	}
	if workflow := build.Parameters[WorkflowVariable]; workflow != "" && workflow != WorkflowID {
		return Candidate{}, fmt.Errorf("build %d belongs to workflow %q", buildID, workflow)
	}
	state, err := build.State()
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		BuildID:         build.ID,
		Status:          build.Status,
		Result:          build.Result,
		State:           state,
		URL:             build.WebURL,
		QueueTime:       build.QueueTime,
		SessionID:       build.Parameters[CorrelationVariable],
		VersionSet:      versionSet,
		ExecutionDigest: build.Parameters[ExecutionDigestVariable],
		CreatedByUI:     createdByUI,
		Parameters:      stringifyTemplateParameters(build.TemplateParameters),
	}, nil
}

// CanonicalVersionSet normalizes a set for stable discovery metadata.
func CanonicalVersionSet(versions []string) (string, error) {
	if len(versions) == 0 {
		return "", errors.New("at least one version is required")
	}
	normalized := make([]string, 0, len(versions))
	seen := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		version = strings.TrimSpace(version)
		if version == "" {
			return "", errors.New("versions must not be empty")
		}
		if _, ok := seen[version]; ok {
			return "", fmt.Errorf("duplicate version %q", version)
		}
		seen[version] = struct{}{}
		normalized = append(normalized, version)
	}
	sort.Strings(normalized)
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildVersionSet(build *azdopipeline.Build) (string, bool, bool) {
	if versionSet := build.Parameters[VersionsVariable]; versionSet != "" {
		return versionSet, true, true
	}
	value, ok := build.TemplateParameters["releaseVersions"]
	if !ok {
		return "", false, false
	}
	var versions []string
	switch typed := value.(type) {
	case []any:
		for _, raw := range typed {
			version, ok := raw.(string)
			if !ok {
				return "", false, false
			}
			versions = append(versions, version)
		}
	case []string:
		versions = typed
	case string:
		if err := json.Unmarshal([]byte(typed), &versions); err != nil {
			return "", false, false
		}
	default:
		return "", false, false
	}
	versionSet, err := CanonicalVersionSet(versions)
	if err != nil {
		return "", false, false
	}
	return versionSet, false, true
}

func stringifyTemplateParameters(values map[string]any) map[string]string {
	result := make(map[string]string, len(values))
	for name, value := range values {
		switch typed := value.(type) {
		case string:
			result[name] = typed
		case bool:
			result[name] = strconv.FormatBool(typed)
		case float64:
			result[name] = strconv.FormatFloat(typed, 'f', -1, 64)
		default:
			data, err := json.Marshal(typed)
			if err == nil {
				result[name] = string(data)
			}
		}
	}
	return result
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
