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

func updatePRComment(args []string) error {
	flags := flag.NewFlagSet("upstreamreview update-pr-comment", flag.ContinueOnError)
	review := flags.String("review", "", "verified JSON review output from check-pr-imports")
	label := flags.String("label", "", "PR label that acknowledges matching changes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", flags.Args())
	}
	var scan scanResult
	if err := json.Unmarshal([]byte(*review), &scan); err != nil {
		return fmt.Errorf("decode -review: %w", err)
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
	return linkSummary(ctx, client, &event, runID, scan, *label)
}

func linkSummary(ctx context.Context, client *github.Client, event *github.PullRequestEvent, runID int64, scan scanResult, label string) error {
	repository := event.GetRepo().GetFullName()
	number := event.GetNumber()
	if !repoSlug.MatchString(repository) || !fs.ValidPath(repository) || number <= 0 || event.GetPullRequest() == nil {
		return errors.New("expected a pull request event with repository and number")
	}
	if !objectID.MatchString(scan.Base) || !objectID.MatchString(scan.Head) || scan.Attempt <= 0 {
		return errors.New("expected a review with full base and head SHAs and a positive run attempt")
	}
	body, err := scan.summaryComment(repository, runID, label)
	if err != nil {
		return err
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
	if existing == nil && scan.Verdict == clean {
		return nil
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	if pr.GetState() != "open" || pr.GetHead().GetSHA() != scan.Head || pr.GetBase().GetSHA() != scan.Base {
		log.Print("Skipping the summary link: the PR changed since this scan.")
		return nil
	}
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

func (scan scanResult) summaryComment(repository string, runID int64, label string) (string, error) {
	const heading = commentMarker + "### Upstream Go review \u2014 "
	repoURL := "https://github.com/" + repository
	runURL := fmt.Sprintf("%s/actions/runs/%d/attempts/%d", repoURL, runID, scan.Attempt)
	labelText := "the review label"
	if label != "" {
		labelText = "`" + label + "`"
	}
	files := " [See the affected files](" + runURL + ")."
	var body string
	switch scan.Verdict {
	case clean:
		body = "no matching changes\n\nNo action needed. [View the scan summary](" + runURL + ")."
	case acknowledged:
		body = "acknowledged\n\nThis acknowledgment carries over to new commits. To request another review, remove " + labelText + " under **Labels**." + files
	case reviewNeeded:
		body = "review needed\n\nAfter reviewing the diffs, open **Labels** (shortcut: `L`) and select " + labelText + "." + files
	default:
		return "", fmt.Errorf("unknown review verdict %q", scan.Verdict)
	}
	commitLink := func(sha string) string {
		return fmt.Sprintf("[`%.12s`](%s/commit/%s)", sha, repoURL, sha)
	}
	return heading + body + "\n\n_Last scanned PR head " + commitLink(scan.Head) + " against base " + commitLink(scan.Base) + "._", nil
}
