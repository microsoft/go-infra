// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

const commentMarker = "<!-- upstreamreview:summary -->\n"

func comment(args []string) error {
	flags := flag.NewFlagSet("upstreamreview comment", flag.ContinueOnError)
	matched := flags.Bool("matched", false, "create a comment for a match; otherwise only update an existing comment")
	label := flags.String("label", "", "PR label that acknowledges matching changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", flags.Args())
	}
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return fmt.Errorf("read pull request event: %w", err)
	}
	var event github.PullRequestEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("decode pull request event: %w", err)
	}
	runID, err := strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
	if err != nil || runID <= 0 {
		return errors.New("GITHUB_RUN_ID must be a positive workflow run ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := githubutil.NewClient(ctx, os.Getenv("GH_TOKEN"))
	if err != nil {
		return err
	}
	return linkSummary(ctx, client, &event, runID, *matched, *label)
}

func linkSummary(ctx context.Context, client *github.Client, event *github.PullRequestEvent, runID int64, matched bool, label string) error {
	repository := event.GetRepo().GetFullName()
	number := event.GetNumber()
	base, head := event.GetPullRequest().GetBase().GetSHA(), event.GetPullRequest().GetHead().GetSHA()
	if !repoSlug.MatchString(repository) || !fs.ValidPath(repository) || number <= 0 || !objectID.MatchString(base) || !objectID.MatchString(head) {
		return errors.New("expected a pull request event with repository, number, base and head")
	}
	owner, repo, _ := strings.Cut(repository, "/")
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var existing *github.IssueComment
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return err
		}
		for _, c := range comments {
			if c.GetUser().GetLogin() == "github-actions[bot]" && strings.HasPrefix(c.GetBody(), commentMarker) {
				existing = c
				break
			}
		}
		if existing != nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if existing == nil && !matched {
		return nil
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	if pr.GetState() != "open" || pr.GetHead().GetSHA() != head || pr.GetBase().GetSHA() != base {
		log.Print("Skipping the summary link: the PR changed since this scan.")
		return nil
	}
	runURL := fmt.Sprintf("https://github.com/%s/actions/runs/%d", repository, runID)
	body := summaryComment(pr, runURL, matched, label)
	if existing.GetBody() == body {
		return nil
	}
	request := github.IssueCommentRequest{Body: body}
	if existing == nil {
		_, _, err = client.Issues.CreateComment(ctx, owner, repo, number, request)
	} else {
		_, _, err = client.Issues.UpdateComment(ctx, owner, repo, existing.GetID(), request)
	}
	if denied, ok := errors.AsType[*github.ErrorResponse](err); ok && denied.Response.StatusCode == http.StatusForbidden {
		log.Printf("Skipping the summary link: %s", denied.Message)
		return nil
	}
	return err
}

func summaryComment(pr *github.PullRequest, runURL string, matched bool, label string) string {
	const heading = commentMarker + "### Upstream Go review \u2014 "
	if !matched {
		return heading + "no matching changes\n\nNo action needed. [View the scan summary](" + runURL + ")."
	}
	files := "[See the affected files](" + runURL + ")."
	if label == "" {
		return heading + "matching changes\n\n" + files
	}
	if hasLabel(pr, label) {
		return heading + "acknowledged\n\n" + files + " This acknowledgment carries over to new commits. To request another review, remove `" + label + "` under **Labels**."
	}
	return heading + "review needed\n\n" + files + " After reviewing the diffs, open **Labels** (shortcut: `L`) and select `" + label + "`."
}
