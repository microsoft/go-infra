// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesrelease adapts read-only Azure DevOps data to the focused direct go-images
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
)

// PipelineClient is the Azure DevOps behavior needed by Service.
type PipelineClient interface {
	Get(context.Context, int) (*azdopipeline.Build, error)
	ListRecent(context.Context, int) ([]*azdopipeline.Build, error)
}

// VersionResolver returns the canonical Microsoft Go versions present in go-images at a commit.
type VersionResolver interface {
	VersionsAtCommit(context.Context, string) ([]string, error)
}

// VersionResolverFunc adapts a function to VersionResolver.
type VersionResolverFunc func(context.Context, string) ([]string, error)

// VersionsAtCommit calls f.
func (f VersionResolverFunc) VersionsAtCommit(ctx context.Context, commit string) ([]string, error) {
	return f(ctx, commit)
}

// Sleeper waits between status checks and is replaceable in tests.
type Sleeper func(context.Context, time.Duration) error

// Config fixes the read-only target and requested versions outside browser-controlled input.
type Config struct {
	DefinitionID    int
	Versions        []string
	VersionResolver VersionResolver
	PollInterval    time.Duration
}

// Service discovers, validates, and monitors direct go-images pipeline runs. It has no queue API.
type Service struct {
	client     PipelineClient
	config     Config
	versions   []string
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
	versionSet, err := CanonicalVersionSet(config.Versions)
	if err != nil {
		return nil, err
	}
	var versions []string
	if err := json.Unmarshal([]byte(versionSet), &versions); err != nil {
		return nil, fmt.Errorf("decode canonical version set: %w", err)
	}
	if config.VersionResolver == nil {
		return nil, errors.New("go-images commit version resolver is required")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if sleeper == nil {
		sleeper = sleepContext
	}
	return &Service{client: client, config: config, versions: versions, versionSet: versionSet, sleep: sleeper}, nil
}

// Candidate is a recent run matching a canonical version set.
type Candidate struct {
	BuildID       int
	Status        string
	Result        string
	State         azdopipeline.RunState
	URL           string
	QueueTime     time.Time
	SourceBranch  string
	SourceVersion string
	VersionSet    string
	Parameters    map[string]string
}

// FindCandidates finds recent direct go-images runs whose source commit contains every requested
// version.
func (s *Service) FindCandidates(ctx context.Context) ([]Candidate, error) {
	builds, err := s.client.ListRecent(ctx, s.config.DefinitionID)
	if err != nil {
		return nil, fmt.Errorf("list recent go-images pipeline runs: %w", err)
	}
	versionCache := make(map[string][]string)
	var candidates []Candidate
	for _, build := range builds {
		matches, err := s.matchesBuild(ctx, build, versionCache)
		if err != nil {
			return nil, fmt.Errorf("resolve candidate build %d versions: %w", build.ID, err)
		}
		if !matches {
			continue
		}
		state, err := build.State()
		if err != nil {
			return nil, fmt.Errorf("interpret candidate build %d: %w", build.ID, err)
		}
		candidates = append(candidates, Candidate{
			BuildID:       build.ID,
			Status:        build.Status,
			Result:        build.Result,
			State:         state,
			URL:           build.WebURL,
			QueueTime:     build.QueueTime,
			SourceBranch:  build.SourceBranch,
			SourceVersion: build.SourceVersion,
			VersionSet:    s.versionSet,
			Parameters:    stringifyTemplateParameters(build.TemplateParameters),
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
	matches, err := s.matchesBuild(ctx, build, make(map[string][]string))
	if err != nil {
		return Candidate{}, fmt.Errorf("resolve build %d versions: %w", buildID, err)
	}
	if !matches {
		return Candidate{}, fmt.Errorf("build %d does not match version set %s", buildID, s.versionSet)
	}
	state, err := build.State()
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		BuildID:       build.ID,
		Status:        build.Status,
		Result:        build.Result,
		State:         state,
		URL:           build.WebURL,
		QueueTime:     build.QueueTime,
		SourceBranch:  build.SourceBranch,
		SourceVersion: build.SourceVersion,
		VersionSet:    s.versionSet,
		Parameters:    stringifyTemplateParameters(build.TemplateParameters),
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

func (s *Service) matchesBuild(
	ctx context.Context,
	build *azdopipeline.Build,
	cache map[string][]string,
) (bool, error) {
	if build.SourceVersion == "" {
		return false, nil
	}
	available, ok := cache[build.SourceVersion]
	if !ok {
		resolved, err := s.config.VersionResolver.VersionsAtCommit(ctx, build.SourceVersion)
		if err != nil {
			return false, err
		}
		available = resolved
		cache[build.SourceVersion] = available
	}
	availableSet := make(map[string]struct{}, len(available))
	for _, version := range available {
		availableSet[strings.TrimSpace(version)] = struct{}{}
	}
	for _, version := range s.versions {
		if _, ok := availableSet[version]; !ok {
			return false, nil
		}
	}
	return true, nil
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

// MonitorRun reads one run until it succeeds or reaches a terminal failure state.
func (s *Service) MonitorRun(ctx context.Context, id int) error {
	if id <= 0 {
		return fmt.Errorf("invalid Azure DevOps build ID %d", id)
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
