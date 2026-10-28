// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/buildreport"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "sync-release-issue-pin",
		Summary: "Pin an open release tracker or unpin a closed one",
		Description: `

Reads the issue's current state rather than relying on an event payload. Issues not created by
create-release-day-issue are skipped.
`,
		Handle: handleSyncReleaseIssuePin,
	})
}

func handleSyncReleaseIssuePin(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	issueNumber := flag.Int("i", 0, "[Required] The release tracking issue number.")
	if err := p(); err != nil {
		return err
	}
	if *issueNumber <= 0 {
		return errors.New("a positive issue number is required")
	}
	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}
	var issue *github.Issue
	if err := githubutil.Retry(func() error {
		var err error
		issue, _, err = client.Issues.Get(ctx, owner, name, *issueNumber)
		return err
	}); err != nil {
		return err
	}
	if !strings.HasPrefix(issue.GetBody(), releaseDayIssueBodyPrefix) {
		log.Printf("Skipping %s/%s#%d: not a release tracking issue.", owner, name, *issueNumber)
		return nil
	}
	if err := buildreport.SyncIssuePin(ctx, client, owner, name, issue); err != nil {
		return fmt.Errorf("sync release tracking issue pin for %s/%s#%d: %w", owner, name, *issueNumber, err)
	}
	return nil
}
