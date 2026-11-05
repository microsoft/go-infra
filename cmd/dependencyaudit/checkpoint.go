// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/go-github/v92/github"
)

const checkpointArtifact = "dependency-audit-checkpoint"

var (
	errCheckpointUnavailable = errors.New("previous audit checkpoint is no longer available")
	checkpointHTTPClient     = http.Client{Timeout: 30 * time.Second}
)

type runCheckpoint struct {
	Version int        `json:"version"`
	State   checkpoint `json:"checkpoint"`
	Report  string     `json:"report"`
}

func latest(ctx context.Context, client *github.Client, c config) (*github.Issue, *checkpoint, string, error) {
	issue, state, err := latestIssue(ctx, client, c)
	if err != nil {
		return nil, nil, "", err
	}
	saved, err := previousRunCheckpoint(ctx, client, c)
	if errors.Is(err, errCheckpointUnavailable) && (state != nil || c.Initial != "") {
		log.Printf("%v; resuming from the existing tracker or explicit starting SHA", err)
		err = nil
	}
	if err != nil {
		if errors.Is(err, errCheckpointUnavailable) {
			return nil, nil, "", fmt.Errorf("%w; restore the checkpoint or supply an explicit initial-sha when no tracker remains", err)
		}
		return nil, nil, "", fmt.Errorf("read previous successful audit checkpoint: %w", err)
	}
	if saved == nil {
		if state != nil {
			log.Printf("No successful-run checkpoint is available; resuming tracker #%d at %s", issue.GetNumber(), state.HeadSHA)
		}
		return issue, state, issue.GetBody(), nil
	}
	if saved.State.Repository != c.Repository {
		return nil, nil, "", errors.New("saved audit checkpoint belongs to a different repository")
	}
	if state != nil && state.Branch != saved.State.Branch {
		return nil, nil, "", errors.New("tracker and saved audit checkpoint disagree on the default branch")
	}
	if state != nil && state.HeadSHA == saved.State.HeadSHA && state.AuditedAt.After(saved.State.AuditedAt) {
		return issue, state, issue.GetBody(), nil
	}
	if state != nil && state.HeadSHA != saved.State.HeadSHA {
		comparison, _, err := client.Repositories.CompareCommits(ctx, c.Owner, c.Repo, saved.State.HeadSHA, state.HeadSHA, nil)
		if err != nil {
			return nil, nil, "", err
		}
		switch comparison.GetStatus() {
		case "ahead":
			return issue, state, issue.GetBody(), nil
		case "behind":
		default:
			return nil, nil, "", errors.New("tracker and saved audit checkpoint have diverged")
		}
	}
	return issue, &saved.State, saved.Report, nil
}

func previousRunCheckpoint(ctx context.Context, client *github.Client, c config) (*runCheckpoint, error) {
	current, _, err := client.Actions.GetWorkflowRunByID(ctx, c.Owner, c.Repo, c.RunID)
	if err != nil {
		return nil, err
	}
	if current.GetWorkflowID() < 1 {
		return nil, errors.New("current run has no workflow identity")
	}
	opts := &github.ListArtifactsOptions{Name: new(checkpointArtifact), ListOptions: github.ListOptions{PerPage: 100}}
	var candidates []*github.Artifact
	counts := make(map[int64]int)
	for {
		artifacts, response, err := client.Actions.ListArtifacts(ctx, c.Owner, c.Repo, opts)
		if err != nil {
			return nil, err
		}
		for _, artifact := range artifacts.Artifacts {
			if artifact.GetName() != checkpointArtifact {
				continue
			}
			candidates = append(candidates, artifact)
			counts[artifact.GetWorkflowRun().GetID()]++
		}
		if response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	slices.SortFunc(candidates, func(a, b *github.Artifact) int {
		return b.GetCreatedAt().Compare(a.GetCreatedAt().Time)
	})
	for _, artifact := range candidates {
		runID := artifact.GetWorkflowRun().GetID()
		if runID == c.RunID {
			continue
		}
		if runID < 1 || artifact.GetID() < 1 || artifact.GetCreatedAt().IsZero() {
			return nil, errors.New("checkpoint artifact has invalid run metadata")
		}
		run, _, err := client.Actions.GetWorkflowRunByID(ctx, c.Owner, c.Repo, runID)
		if err != nil {
			return nil, err
		}
		if run.GetWorkflowID() != current.GetWorkflowID() || run.GetConclusion() != "success" || run.GetStatus() != "completed" {
			continue
		}
		if counts[runID] != 1 {
			return nil, errors.New("audit run contains duplicate checkpoint artifacts")
		}
		if artifact.GetExpired() {
			return nil, fmt.Errorf("%w: artifact from run %d expired", errCheckpointUnavailable, runID)
		}
		saved, err := downloadCheckpoint(ctx, client, c, artifact.GetID())
		if err != nil {
			return nil, err
		}
		if saved.State.RunID != runID {
			return nil, errors.New("saved checkpoint does not belong to its workflow run")
		}
		return saved, nil
	}
	return nil, nil
}

func downloadCheckpoint(ctx context.Context, client *github.Client, c config, artifactID int64) (*runCheckpoint, error) {
	location, _, err := client.Actions.DownloadArtifact(ctx, c.Owner, c.Repo, artifactID, 1)
	if err != nil {
		var apiError *github.ErrorResponse
		if errors.As(err, &apiError) && (apiError.Response.StatusCode == http.StatusNotFound || apiError.Response.StatusCode == http.StatusGone) {
			return nil, fmt.Errorf("%w: %w", errCheckpointUnavailable, err)
		}
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := checkpointHTTPClient.Do(request)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("download checkpoint archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download audit checkpoint: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("checkpoint archive exceeds the 2 MiB limit")
	}
	return decodeCheckpoint(data)
}

func decodeCheckpoint(data []byte) (*runCheckpoint, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open checkpoint archive: %w", err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "checkpoint.json" {
		return nil, errors.New("checkpoint artifact must contain only checkpoint.json")
	}
	file, err := archive.File[0].Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > 2<<20 {
		return nil, errors.New("saved checkpoint exceeds the 2 MiB limit")
	}
	var saved runCheckpoint
	if err := json.Unmarshal(encoded, &saved); err != nil {
		return nil, fmt.Errorf("decode saved checkpoint: %w", err)
	}
	if saved.Version != 1 || saved.Report == "" || saved.State.RunID < 1 {
		return nil, errors.New("unsupported or incomplete saved checkpoint")
	}
	if err := validateCheckpoint(saved.State); err != nil {
		return nil, err
	}
	areas, err := reportAreas(saved.Report, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid saved audit report: %w", err)
	}
	if !slices.Equal(areas, saved.State.Areas) {
		return nil, errors.New("saved audit report does not match its checkpoint areas")
	}
	return &saved, nil
}
