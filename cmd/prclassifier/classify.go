// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"slices"
	"strings"
)

// GitHub's list-pull-request-files endpoint returns at most 3,000 files.
const maxPullRequestFiles = 3000

const classificationFailedLabel = "failed-auto-classify"

type labelDefinition struct {
	Name        string
	Color       string
	Description string
}

type pullRequest struct {
	Labels       []string
	Additions    int
	Deletions    int
	ChangedFiles int
}

type changedFile struct {
	Path string
}

type classification struct {
	Labels         []string
	ChangedLines   int
	FileCount      int
	FilesTruncated bool
}

func staticLabelDefinitions() []labelDefinition {
	return []labelDefinition{
		{Name: "size:small", Color: "0E8A16", Description: "At most 30 changed lines across at most 2 files"},
		{Name: "size:medium", Color: "FBCA04", Description: "At most 100 changed lines across at most 5 files"},
		{Name: "size:large", Color: "D93F0B", Description: "At most 300 changed lines across at most 10 files"},
		{Name: "size:xlarge", Color: "B60205", Description: "More than 300 changed lines or 10 files"},
		// The deterministic classifier provisions kind labels so the agentic
		// classifier can safely apply them, but it never selects or manages one.
		{Name: "kind:code", Color: "1D76DB", Description: "Changes production behavior or code"},
		{Name: "kind:docs", Color: "0075CA", Description: "Changes documentation or comments"},
		{Name: "kind:tests", Color: "5319E7", Description: "Changes tests, fixtures, or test infrastructure"},
		{Name: "kind:examples", Color: "C5DEF5", Description: "Changes examples or example-support metadata"},
		{Name: "kind:dependencies", Color: "0366D6", Description: "Changes dependencies or manifests"},
		{Name: "kind:ci", Color: "000000", Description: "Changes CI, build, or repository automation"},
		{Name: classificationFailedLabel, Color: "D93F0B", Description: "Automatic kind classification needs maintainer attention"},
	}
}

func labelDefinitions(cfg config) []labelDefinition {
	definitions := staticLabelDefinitions()
	labels := make([]string, 0, len(cfg.AreaPrefixes))
	for label := range cfg.AreaPrefixes {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	for _, label := range labels {
		area := strings.ReplaceAll(strings.TrimPrefix(label, "area:"), "/", " / ")
		definitions = append(definitions, labelDefinition{
			Name:        label,
			Color:       "C2E0C6",
			Description: "Changes files in the " + area + " area",
		})
	}
	return definitions
}

func classify(cfg config, pull pullRequest, files []changedFile) classification {
	changedLines := pull.Additions + pull.Deletions
	fileCount := pull.ChangedFiles
	if fileCount == 0 {
		fileCount = len(files)
	}
	filesTruncated := fileCount > len(files) || len(files) >= maxPullRequestFiles

	size := "size:xlarge"
	switch {
	case changedLines <= 30 && fileCount <= 2:
		size = "size:small"
	case changedLines <= 100 && fileCount <= 5:
		size = "size:medium"
	case changedLines <= 300 && fileCount <= 10:
		size = "size:large"
	}

	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}

	areas := make([]string, 0, len(cfg.AreaPrefixes))
	for label, prefixes := range cfg.AreaPrefixes {
		if anyPath(paths, func(file string) bool { return matchesAnyPrefix(file, prefixes) }) {
			areas = append(areas, label)
		}
	}
	slices.Sort(areas)

	labels := []string{size}
	labels = append(labels, areas...)
	return classification{
		Labels:         labels,
		ChangedLines:   changedLines,
		FileCount:      fileCount,
		FilesTruncated: filesTruncated,
	}
}

func anyPath(paths []string, predicate func(string) bool) bool {
	for _, file := range paths {
		if predicate(file) {
			return true
		}
	}
	return false
}

func matchesAnyPrefix(file string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if file == prefix || strings.HasPrefix(file, prefix+"/") {
			return true
		}
	}
	return false
}
