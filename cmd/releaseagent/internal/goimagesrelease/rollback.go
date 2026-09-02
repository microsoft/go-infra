// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goimagesrelease validates read-only Azure DevOps data used to prepare a go-images release.
package goimagesrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
)

// PipelineClient is the Azure DevOps behavior needed to validate a rollback source.
type PipelineClient interface {
	Get(context.Context, int) (*azdopipeline.Build, error)
}

// VersionResolver returns the canonical Microsoft Build of Go versions present in go-images at a commit.
type VersionResolver interface {
	VersionsAtCommit(context.Context, string) ([]string, error)
}

// VersionResolverFunc adapts a function to VersionResolver.
type VersionResolverFunc func(context.Context, string) ([]string, error)

// VersionsAtCommit calls f.
func (f VersionResolverFunc) VersionsAtCommit(ctx context.Context, commit string) ([]string, error) {
	return f(ctx, commit)
}

// RollbackSource is a validated successful build whose artifacts may be republished.
type RollbackSource struct {
	BuildID  int
	URL      string
	Versions []string
}

var sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateRollbackSource verifies that buildID is a successful pipeline 1023 run which produced
// its own artifacts. Versions are informational and come from the build's exact source commit.
func ValidateRollbackSource(
	ctx context.Context,
	client PipelineClient,
	resolver VersionResolver,
	definitionID,
	buildID int,
) (RollbackSource, error) {
	if client == nil || resolver == nil {
		return RollbackSource{}, errors.New("rollback pipeline client and version resolver are required")
	}
	if definitionID <= 0 || buildID <= 0 {
		return RollbackSource{}, errors.New("rollback definition and build IDs must be positive")
	}
	build, err := client.Get(ctx, buildID)
	if err != nil {
		return RollbackSource{}, fmt.Errorf("get rollback source build %d: %w", buildID, err)
	}
	if build.DefinitionID != definitionID {
		return RollbackSource{}, fmt.Errorf("build %d belongs to pipeline %d, expected %d", buildID, build.DefinitionID, definitionID)
	}
	state, err := build.State()
	if err != nil {
		return RollbackSource{}, fmt.Errorf("interpret rollback source build %d: %w", buildID, err)
	}
	if state != azdopipeline.RunStateSucceeded || build.Result != "succeeded" {
		return RollbackSource{}, fmt.Errorf("rollback source build %d must have result succeeded", buildID)
	}
	if build.SourceBranch != "refs/heads/microsoft/main" || !sourceCommitPattern.MatchString(build.SourceVersion) {
		return RollbackSource{}, fmt.Errorf(
			"rollback source build %d has unsupported source %s@%s",
			buildID,
			build.SourceBranch,
			build.SourceVersion,
		)
	}
	if source, ok := build.TemplateParameters["sourceBuildPipelineRunId"].(string); ok &&
		source != "" && source != "$(Build.BuildId)" {

		return RollbackSource{}, fmt.Errorf("rollback source build %d reused artifacts from build %q", buildID, source)
	}
	versions, err := resolver.VersionsAtCommit(ctx, build.SourceVersion)
	if err != nil {
		return RollbackSource{}, fmt.Errorf("resolve rollback source build %d versions: %w", buildID, err)
	}
	versions, err = normalizeVersions(versions)
	if err != nil {
		return RollbackSource{}, fmt.Errorf("validate rollback source build %d versions: %w", buildID, err)
	}
	return RollbackSource{
		BuildID:  build.ID,
		URL:      build.WebURL,
		Versions: versions,
	}, nil
}

func normalizeVersions(versions []string) ([]string, error) {
	if len(versions) == 0 {
		return nil, errors.New("at least one version is required")
	}
	normalized := make([]string, 0, len(versions))
	seen := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		version = strings.TrimSpace(version)
		if version == "" {
			return nil, errors.New("versions must not be empty")
		}
		if _, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate version %q", version)
		}
		seen[version] = struct{}{}
		normalized = append(normalized, version)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// CanonicalVersionSet returns a stable encoding used to correlate queued pipeline runs.
func CanonicalVersionSet(versions []string) (string, error) {
	normalized, err := normalizeVersions(versions)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
