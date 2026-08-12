// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/submodule"
)

const submoduleReferencesEnv = "GIT_GO_PATCH_SUBMODULE_REFERENCES"

func resetSubmodule(rootDir, submoduleDir string, force bool) error {
	reference, err := configuredSubmoduleReference(rootDir, submoduleDir)
	if err != nil {
		return err
	}
	if reference != "" {
		fi, err := os.Stat(reference)
		if err != nil {
			return fmt.Errorf("%s reference path %q does not exist: %w", submoduleReferencesEnv, reference, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s reference path %q is not a directory", submoduleReferencesEnv, reference)
		}
	}
	return submodule.ResetWithReference(rootDir, submoduleDir, force, reference)
}

func configuredSubmoduleReference(rootDir, submoduleDir string) (string, error) {
	references, err := parseSubmoduleReferences(os.Getenv(submoduleReferencesEnv))
	if err != nil || len(references) == 0 {
		return "", err
	}

	url, err := submoduleURL(rootDir, submoduleDir)
	if err != nil {
		return "", err
	}
	return references[url], nil
}

func parseSubmoduleReferences(value string) (map[string]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	fields := strings.Split(value, ",")
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("%s must contain comma-separated repository URL and local path pairs", submoduleReferencesEnv)
	}

	references := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		url := strings.TrimSpace(fields[i])
		path := strings.TrimSpace(fields[i+1])
		if url == "" || path == "" {
			return nil, fmt.Errorf("%s contains an empty repository URL or local path", submoduleReferencesEnv)
		}
		references[url] = path
	}
	return references, nil
}

func submoduleURL(rootDir, submoduleDir string) (string, error) {
	relativePath, err := filepath.Rel(rootDir, submoduleDir)
	if err != nil {
		return "", fmt.Errorf("failed to find submodule path: %w", err)
	}

	pathEntries, err := gitcmd.CombinedOutput(
		rootDir,
		"config",
		"--null",
		"--file",
		".gitmodules",
		"--get-regexp",
		`^submodule\..*\.path$`,
	)
	if err != nil {
		return "", fmt.Errorf("failed to read submodule paths from .gitmodules: %w", err)
	}

	// First, examine each submodule path entry to find a match for the submodule we're managing.
	for _, entry := range strings.Split(pathEntries, "\x00") {
		key, path, ok := strings.Cut(entry, "\n")
		if !ok || filepath.Clean(filepath.FromSlash(path)) != filepath.Clean(relativePath) {
			continue
		}

		// We found a match, now determine the URL of this match.
		urlKey := strings.TrimSuffix(key, ".path") + ".url"
		url, err := gitcmd.CombinedOutput(rootDir, "config", "--null", "--file", ".gitmodules", "--get", urlKey)
		if err != nil {
			return "", fmt.Errorf("failed to read URL for submodule %q: %w", relativePath, err)
		}
		return strings.TrimSuffix(url, "\x00"), nil
	}

	return "", fmt.Errorf("failed to find submodule %q in .gitmodules", relativePath)
}
