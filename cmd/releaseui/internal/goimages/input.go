// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func normalizePlanInput(input PlanInput) (PlanInput, error) {
	input.SourceBuildID = strings.TrimSpace(input.SourceBuildID)
	switch input.Mode {
	case ModeNormal, ModeTest:
		if input.SourceBuildID != "" {
			return PlanInput{}, fmt.Errorf("%s release does not accept a source build ID", input.Mode)
		}
	case ModeRollback:
		buildID, err := strconv.Atoi(input.SourceBuildID)
		if err != nil || buildID <= 0 {
			return PlanInput{}, errors.New("rollback source build ID must be a positive integer")
		}
		input.SourceBuildID = strconv.Itoa(buildID)
	default:
		return PlanInput{}, fmt.Errorf("unsupported go-images release mode %q", input.Mode)
	}
	return input, nil
}

func validateCurrentSource(source Source) error {
	if source.Branch != SourceBranch {
		return fmt.Errorf("resolved go-images branch %q is not allowlisted", source.Branch)
	}
	if len(source.Commit) != 40 {
		return fmt.Errorf("resolved go-images commit %q is invalid", source.Commit)
	}
	for _, character := range source.Commit {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return fmt.Errorf("resolved go-images commit %q is invalid", source.Commit)
		}
	}
	return nil
}

func normalizeResolvedVersions(versions []string) ([]string, error) {
	if len(versions) == 0 {
		return nil, errors.New("resolved go-images source contains no Microsoft Build of Go versions")
	}
	seen := make(map[string]struct{}, len(versions))
	normalized := make([]string, 0, len(versions))
	for _, version := range versions {
		version = strings.TrimSpace(version)
		if version == "" {
			return nil, errors.New("resolved go-images source contains an empty version")
		}
		if _, exists := seen[version]; exists {
			continue
		}
		seen[version] = struct{}{}
		normalized = append(normalized, version)
	}
	sort.Strings(normalized)
	return normalized, nil
}
