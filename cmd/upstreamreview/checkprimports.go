// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

func checkPRImports(args []string) error {
	flags := flag.NewFlagSet("upstreamreview check-pr-imports", flag.ContinueOnError)
	repo := flags.String("repo", "", "target GitHub repository (owner/name)")
	n := flags.Int("pr", 0, "open pull request number")
	merge := flags.String("merge", "", "test merge commit for this workflow run")
	updateComment := flags.Bool("update-pr-comment", false, "update the PR review comment and remove stale review labels (requires pull-requests: write)")
	var p policy
	flags.StringVar(&p.upstream, "upstream", "", "upstream GitHub repository (owner/name)")
	flags.StringVar(&p.submodule, "submodule", "", "submodule path in the target repository")
	flags.StringVar(&p.label, "label", "", "PR label that acknowledges matching changes")
	p.rule.BindFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", flags.Args())
	}
	if *n <= 0 || !objectID.MatchString(*merge) {
		return errors.New("a positive PR number and a full test merge SHA are required")
	}
	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, os.Getenv("GH_TOKEN"))
	if err != nil {
		return err
	}
	g, err := newGate(client, *repo, p)
	if err != nil {
		return err
	}
	var event *github.PullRequestEvent
	if path := os.Getenv("GITHUB_EVENT_PATH"); path != "" {
		event, err = readPREvent(path)
		if err != nil {
			return err
		}
	}
	return g.runCheck(ctx, *n, *merge, event, *updateComment)
}

func readPREvent(path string) (*github.PullRequestEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pull request event: %w", err)
	}
	var event github.PullRequestEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("decode pull request event: %w", err)
	}
	return &event, nil
}

func (g *gate) runCheck(ctx context.Context, n int, merge string, event *github.PullRequestEvent, updateComment bool) error {
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	r, scanErr := g.check(scanCtx, n, merge, event)
	if _, writeErr := fmt.Print(r.summary); writeErr != nil {
		return writeErr
	}
	// A missing acknowledgment is a verified verdict worth publishing even though the check fails.
	if updateComment && r.updateComment {
		commentCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := g.updatePRComment(commentCtx, n, event, r.scan); err != nil {
			log.Printf("warning: could not update PR comment or review label: %v", err)
		}
	}
	return scanErr
}
