// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

type executionSummary struct {
	Classified int
	Counts     map[string]int
	Failures   []int
}

func execute(ctx context.Context, cfg config, client githubClient, output io.Writer) error {
	definitions := labelDefinitions(cfg)
	if err := ensureLabels(ctx, cfg, client, definitions); err != nil {
		return err
	}

	numbers := []int{cfg.PRNumber}
	if cfg.PRNumber == 0 {
		var err error
		numbers, err = client.ListOpenPullRequests(ctx, cfg.Owner, cfg.Repo)
		if err != nil {
			return err
		}
		slices.Sort(numbers)
	}

	summary := executionSummary{Counts: make(map[string]int)}
	for _, number := range numbers {
		if err := ctx.Err(); err != nil {
			return err
		}

		result, ok, err := classifyPullRequest(ctx, cfg, client, definitions, number)
		if err != nil {
			summary.Failures = append(summary.Failures, number)
			if _, writeErr := fmt.Fprintf(output, "::error::Failed to classify PR #%d: %v\n", number, err); writeErr != nil {
				return fmt.Errorf("write output: %w", writeErr)
			}
			continue
		}
		if !ok {
			if _, err := fmt.Fprintf(output, "::warning::PR #%d has no changed files; skipping\n", number); err != nil {
				return fmt.Errorf("write output: %w", err)
			}
			continue
		}

		summary.Classified++
		for _, label := range result.Labels {
			summary.Counts[label]++
		}
		if _, err := fmt.Fprintf(output, "PR #%d: %s (%d changed lines, %d files)\n",
			number, strings.Join(result.Labels, ", "), result.ChangedLines, result.FileCount); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}

	var runErr error
	if len(summary.Failures) > 0 {
		runErr = fmt.Errorf("failed to classify %d pull request(s)", len(summary.Failures))
	}
	if err := writeSummary(cfg.SummaryPath, summary); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write step summary: %w", err))
	}
	return runErr
}

func ensureLabels(ctx context.Context, cfg config, client githubClient, definitions []labelDefinition) error {
	existing, err := client.ListLabels(ctx, cfg.Owner, cfg.Repo)
	if err != nil {
		return err
	}
	existingSet := make(map[string]bool, len(existing))
	for _, label := range existing {
		existingSet[label] = true
	}

	for _, definition := range definitions {
		if existingSet[definition.Name] {
			continue
		}
		if err := client.CreateLabel(ctx, cfg.Owner, cfg.Repo, definition); err != nil {
			// A concurrent classifier may have created the label first. Only
			// accept the create failure if the label can now be retrieved.
			if verifyErr := client.GetLabel(ctx, cfg.Owner, cfg.Repo, definition.Name); verifyErr != nil {
				return fmt.Errorf("create label %s: %w", definition.Name, err)
			}
		}
		existingSet[definition.Name] = true
	}
	return nil
}

func classifyPullRequest(
	ctx context.Context,
	cfg config,
	client githubClient,
	definitions []labelDefinition,
	number int,
) (classification, bool, error) {
	pull, err := client.GetPullRequest(ctx, cfg.Owner, cfg.Repo, number)
	if err != nil {
		return classification{}, false, err
	}
	files, err := client.ListPullRequestFiles(ctx, cfg.Owner, cfg.Repo, number)
	if err != nil {
		return classification{}, false, err
	}
	if len(files) == 0 {
		return classification{}, false, nil
	}

	result := classify(cfg, pull, files)
	managed := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		// Kind labels and their failure marker are provisioned here but owned by
		// the agentic classifier.
		if strings.HasPrefix(definition.Name, "kind:") || definition.Name == classificationFailedLabel {
			continue
		}
		managed[definition.Name] = true
	}
	desired := make(map[string]bool, len(result.Labels))
	for _, label := range result.Labels {
		desired[label] = true
	}
	current := make(map[string]bool, len(pull.Labels))
	for _, label := range pull.Labels {
		current[label] = true
	}
	unmanagedCount := 0
	for label := range current {
		if !managed[label] {
			unmanagedCount++
		}
	}
	if unmanagedCount+len(result.Labels) > maxIssueLabels {
		return classification{}, false, fmt.Errorf(
			"classifying PR #%d would require %d labels, exceeding GitHub's limit of %d",
			number, unmanagedCount+len(result.Labels), maxIssueLabels)
	}

	var stale []string
	for label := range current {
		if !managed[label] || desired[label] {
			continue
		}
		// A truncated files response cannot prove an area is no longer
		// touched. Preserve existing area labels rather than remove one based
		// on incomplete data.
		if result.FilesTruncated && strings.HasPrefix(label, "area:") {
			continue
		}
		stale = append(stale, label)
	}
	slices.Sort(stale)
	for _, label := range stale {
		if err := client.RemoveLabel(ctx, cfg.Owner, cfg.Repo, number, label); err != nil {
			return classification{}, false, fmt.Errorf("remove label %s from PR #%d: %w", label, number, err)
		}
	}

	var missing []string
	for _, label := range result.Labels {
		if !current[label] {
			missing = append(missing, label)
		}
	}
	if len(missing) > 0 {
		if err := client.AddLabels(ctx, cfg.Owner, cfg.Repo, number, missing); err != nil {
			return classification{}, false, fmt.Errorf("add labels to PR #%d: %w", number, err)
		}
	}
	return result, true, nil
}

func writeSummary(summaryPath string, summary executionSummary) (err error) {
	if summaryPath == "" {
		return nil
	}
	file, err := os.OpenFile(summaryPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	if _, err := fmt.Fprintf(file, "## Pull request classification\n\nClassified %d pull request(s).\n\n", summary.Classified); err != nil {
		return err
	}
	if _, err := fmt.Fprint(file, "| Label | Count |\n| --- | ---: |\n"); err != nil {
		return err
	}
	labels := make([]string, 0, len(summary.Counts))
	for label := range summary.Counts {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	for _, label := range labels {
		if _, err := fmt.Fprintf(file, "| `%s` | %d |\n", label, summary.Counts[label]); err != nil {
			return err
		}
	}
	if len(summary.Failures) > 0 {
		failures := make([]string, 0, len(summary.Failures))
		for _, number := range summary.Failures {
			failures = append(failures, fmt.Sprintf("#%d", number))
		}
		if _, err := fmt.Fprintf(file, "\nFailed PRs: %s\n", strings.Join(failures, ", ")); err != nil {
			return err
		}
	}
	return nil
}
