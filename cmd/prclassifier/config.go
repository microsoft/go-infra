// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

const (
	defaultAreaPrefixes       = "{}"
	maxLabelNameLength        = 50
	maxLabelDescriptionLength = 100
	maxIssueLabels            = 100
	// Reserve capacity for one size, all six kinds, and the classification
	// failure marker. Existing unrelated labels are checked at runtime.
	maxAreaLabels = maxIssueLabels - 1 - 6 - 1
)

var areaLabelPattern = regexp.MustCompile(`^area:[a-z0-9][a-z0-9/_-]*$`)

type config struct {
	Owner        string
	Repo         string
	PRNumber     int
	AreaPrefixes map[string][]string
	Token        string
	SummaryPath  string
}

func parseConfig(args []string, getenv func(string) string, output io.Writer) (config, error) {
	fs := flag.NewFlagSet("prclassifier", flag.ContinueOnError)
	fs.SetOutput(output)

	repo := fs.String("repo", getenv("GITHUB_REPOSITORY"), "target repository in owner/name form")
	prNumber := fs.Int("pr-number", 0, "pull request number, or 0 to classify every open pull request")
	areaPrefixesJSON := fs.String("area-prefixes", defaultEnv(getenv, "AREA_PREFIXES", defaultAreaPrefixes), "JSON object mapping area labels to path-prefix arrays")
	summaryPath := fs.String("summary", getenv("GITHUB_STEP_SUMMARY"), "GitHub Actions step-summary file (optional)")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *prNumber < 0 {
		return config{}, errors.New("pr-number must be non-negative")
	}

	owner, name, err := parseRepo(*repo)
	if err != nil {
		return config{}, err
	}

	areas, err := parseAreaPrefixes(*areaPrefixesJSON)
	if err != nil {
		return config{}, err
	}

	token := getenv("GITHUB_TOKEN")
	if token == "" {
		token = getenv("GH_TOKEN")
	}
	if token == "" {
		return config{}, errors.New("GITHUB_TOKEN or GH_TOKEN must be set")
	}

	return config{
		Owner:        owner,
		Repo:         name,
		PRNumber:     *prNumber,
		AreaPrefixes: areas,
		Token:        token,
		SummaryPath:  *summaryPath,
	}, nil
}

func defaultEnv(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseRepo(value string) (string, string, error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(value), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("repo must be in owner/name form, got %q", value)
	}
	return owner, repo, nil
}

func parseAreaPrefixes(value string) (map[string][]string, error) {
	var areas map[string][]string
	if err := json.Unmarshal([]byte(value), &areas); err != nil {
		return nil, fmt.Errorf("area-prefixes must be a JSON object: %w", err)
	}
	if areas == nil {
		return nil, errors.New("area-prefixes must be a JSON object")
	}
	if len(areas) > maxAreaLabels {
		return nil, fmt.Errorf("area-prefixes defines too many labels: at most %d areas are allowed", maxAreaLabels)
	}

	for label, inputPrefixes := range areas {
		if !areaLabelPattern.MatchString(label) || len(label) > maxLabelNameLength {
			return nil, fmt.Errorf("invalid area label %q", label)
		}
		area := strings.ReplaceAll(strings.TrimPrefix(label, "area:"), "/", " / ")
		if len("Changes files in the "+area+" area") > maxLabelDescriptionLength {
			return nil, fmt.Errorf("generated description for %s is too long", label)
		}
		if len(inputPrefixes) == 0 {
			return nil, fmt.Errorf("%s must map to a non-empty array of path prefixes", label)
		}
		prefixes, err := normalizePrefixes(label, inputPrefixes)
		if err != nil {
			return nil, err
		}
		areas[label] = prefixes
	}
	return areas, nil
}

func normalizePrefixes(name string, input []string) ([]string, error) {
	prefixes := make([]string, 0, len(input))
	for _, value := range input {
		prefix, err := normalizePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%s contains invalid path prefix %q: %w", name, value, err)
		}
		if !slices.Contains(prefixes, prefix) {
			prefixes = append(prefixes, prefix)
		}
	}
	slices.Sort(prefixes)
	return prefixes, nil
}

func normalizePrefix(value string) (string, error) {
	prefix := strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	if prefix == "" || strings.HasPrefix(prefix, "/") {
		return "", errors.New("prefix must be a relative repository path")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		return "", errors.New("prefix must not be empty")
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("prefix must not contain empty, dot, or parent components")
		}
	}
	return prefix, nil
}
