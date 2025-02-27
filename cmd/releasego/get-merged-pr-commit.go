// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-merged-pr-commit",
		Summary: "Get the commit that merging the given PR created, or poll if the PR is not yet merged.",
		Description: `

If the PR is not on track to be merged automatically, prints an error and return exit code 1. For
example, fail if auto-merge is not enabled or CI failed. This lets release automation continue and
alert a dev that the process is not proceeding smoothly.
`,
		Handle: handleGetMergedPRCommit,
	})
}

func handleGetMergedPRCommit(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags()
	prNumber := flag.Int("pr", 0, "[Required] The PR number to check.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")
	setVariable := flag.String("set-azdo-variable", "", "An AzDO variable name to set.")

	if err := p(); err != nil {
		return err
	}

	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}
	if *prNumber == 0 {
		return errors.New("no pr number specified")
	}
	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	ctx := context.Background()
	client, err := gitcmd.NewClientFromFlags(gitHubAuthFlags, ctx)
	if err != nil {
		return err
	}

	var commit string
	for {
		// Use retry to fetch the PR info: handles rate limit and other temporary errors.
		var pr *github.PullRequest
		if err := githubutil.Retry(func() error {
			pr, _, err = client.PullRequests.Get(ctx, owner, name, *prNumber)
			return err
		}); err != nil {
			return err
		}

		commit, err = checkPRCompleteMergeCommit(pr)
		if err != nil {
			return err
		}

		if commit != "" {
			break
		}

		headCommit := pr.Head.GetSHA()

		log.Printf(
			"Examining CI checks on commit %v from PR %v to determine if PR still has a chance to be merged...",
			headCommit, pr.GetHTMLURL())

		var checks []*github.CheckRun
		if err := githubutil.FetchEachPage(func(options github.ListOptions) (*github.Response, error) {
			completed := "completed"
			result, resp, err := client.Checks.ListCheckRunsForRef(
				ctx, owner, name, headCommit,
				&github.ListCheckRunsOptions{
					Status:      &completed,
					ListOptions: options,
				})
			if err != nil {
				return nil, err
			}
			checks = append(checks, result.CheckRuns...)
			return resp, nil
		}); err != nil {
			return err
		}

		log.Printf("Found %v complete check runs.\n", len(checks))
		for _, c := range checks {
			if c.GetConclusion() == "failure" {
				return fmt.Errorf("failed check detected: %v PR most likely won't auto-merge", c.GetHTMLURL())
			}
		}

		log.Printf("It looks like this PR might merge in the future. Will check again in after %v...\n", pollDelay)
		time.Sleep(pollDelay)
	}

	if *setVariable != "" {
		azdo.LogCmdSetVariable(*setVariable, commit)
	}
	log.Printf("Found merged commit hash: %v\n", commit)
	return nil
}

func checkPRCompleteMergeCommit(pr *github.PullRequest) (string, error) {
	if pr.GetMerged() {
		return pr.GetMergeCommitSHA(), nil
	}
	if pr.GetState() == "closed" {
		return "", fmt.Errorf("pull request is closed without being merged: %v", pr.GetHTMLURL())
	}
	return "", nil
}
