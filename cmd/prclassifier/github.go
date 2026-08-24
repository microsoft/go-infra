// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/githubutil"
)

type githubClient interface {
	ListLabels(context.Context, string, string) ([]string, error)
	CreateLabel(context.Context, string, string, labelDefinition) error
	GetLabel(context.Context, string, string, string) error
	ListOpenPullRequests(context.Context, string, string) ([]int, error)
	GetPullRequest(context.Context, string, string, int) (pullRequest, error)
	ListPullRequestFiles(context.Context, string, string, int) ([]changedFile, error)
	RemoveLabel(context.Context, string, string, int, string) error
	AddLabels(context.Context, string, string, int, []string) error
}

type githubAPI struct {
	client *github.Client
}

func newGitHubAPI(ctx context.Context, token string) (*githubAPI, error) {
	client, err := githubutil.NewClient(ctx, token)
	if err != nil {
		return nil, err
	}
	return &githubAPI{client: client}, nil
}

func (g *githubAPI) ListLabels(ctx context.Context, owner, repo string) ([]string, error) {
	var labels []string
	options := &github.ListOptions{PerPage: 100}
	for {
		var (
			page []*github.Label
			resp *github.Response
		)
		if err := githubutil.Retry(func() error {
			var err error
			page, resp, err = g.client.Issues.ListLabels(ctx, owner, repo, options)
			return err
		}); err != nil {
			return nil, fmt.Errorf("list repository labels: %w", err)
		}
		for _, label := range page {
			labels = append(labels, label.GetName())
		}
		if resp.NextPage == 0 {
			return labels, nil
		}
		options.Page = resp.NextPage
	}
}

func (g *githubAPI) CreateLabel(ctx context.Context, owner, repo string, definition labelDefinition) error {
	return githubutil.Retry(func() error {
		_, _, err := g.client.Issues.CreateLabel(ctx, owner, repo, &github.Label{
			Name:        new(definition.Name),
			Color:       new(definition.Color),
			Description: new(definition.Description),
		})
		return err
	})
}

func (g *githubAPI) GetLabel(ctx context.Context, owner, repo, name string) error {
	return githubutil.Retry(func() error {
		_, _, err := g.client.Issues.GetLabel(ctx, owner, repo, name)
		return err
	})
}

func (g *githubAPI) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]int, error) {
	var numbers []int
	options := &github.PullRequestListOptions{
		State: "open",
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}
	for {
		var (
			page []*github.PullRequest
			resp *github.Response
		)
		if err := githubutil.Retry(func() error {
			var err error
			page, resp, err = g.client.PullRequests.List(ctx, owner, repo, options)
			return err
		}); err != nil {
			return nil, fmt.Errorf("list open pull requests: %w", err)
		}
		for _, pull := range page {
			numbers = append(numbers, pull.GetNumber())
		}
		if resp.NextPage == 0 {
			return numbers, nil
		}
		options.Page = resp.NextPage
	}
}

func (g *githubAPI) GetPullRequest(ctx context.Context, owner, repo string, number int) (pullRequest, error) {
	var pull *github.PullRequest
	if err := githubutil.Retry(func() error {
		var err error
		pull, _, err = g.client.PullRequests.Get(ctx, owner, repo, number)
		return err
	}); err != nil {
		return pullRequest{}, fmt.Errorf("get pull request #%d: %w", number, err)
	}
	labels := make([]string, 0, len(pull.Labels))
	for _, label := range pull.Labels {
		labels = append(labels, label.GetName())
	}
	return pullRequest{
		Labels:       labels,
		Additions:    pull.GetAdditions(),
		Deletions:    pull.GetDeletions(),
		ChangedFiles: pull.GetChangedFiles(),
	}, nil
}

func (g *githubAPI) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]changedFile, error) {
	var files []changedFile
	options := &github.ListOptions{PerPage: 100}
	for {
		var (
			page []*github.CommitFile
			resp *github.Response
		)
		if err := githubutil.Retry(func() error {
			var err error
			page, resp, err = g.client.PullRequests.ListFiles(ctx, owner, repo, number, options)
			return err
		}); err != nil {
			return nil, fmt.Errorf("list files for pull request #%d: %w", number, err)
		}
		for _, file := range page {
			files = append(files, changedFile{
				Path: file.GetFilename(),
			})
		}
		if resp.NextPage == 0 || len(files) >= maxPullRequestFiles {
			return files, nil
		}
		options.Page = resp.NextPage
	}
}

func (g *githubAPI) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	return githubutil.Retry(func() error {
		_, err := g.client.Issues.RemoveLabelForIssue(ctx, owner, repo, number, label)
		var responseError *github.ErrorResponse
		if errors.As(err, &responseError) && responseError.Response.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	})
}

func (g *githubAPI) AddLabels(ctx context.Context, owner, repo string, number int, labels []string) error {
	return githubutil.Retry(func() error {
		_, _, err := g.client.Issues.AddLabelsToIssue(ctx, owner, repo, number, labels)
		return err
	})
}
