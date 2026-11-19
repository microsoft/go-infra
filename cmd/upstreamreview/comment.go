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
	event, err := readPREvent(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return err
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
	return linkSummary(ctx, client, event, runID, scan, *label)
}

func findSummaryComment(ctx context.Context, client *github.Client, owner, repo string, number int) (*github.IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range comments {
			if c.GetUser().GetLogin() == "github-actions[bot]" && strings.HasPrefix(c.GetBody(), commentMarker) {
				return c, nil
			}
		}
		if resp.NextPage == 0 {
			return nil, nil
		}
		opts.Page = resp.NextPage
	}
}

func linkSummary(ctx context.Context, client *github.Client, event *github.PullRequestEvent, runID int64, scan scanResult, label string) error {
	repository := event.GetRepo().GetFullName()
	number := event.GetNumber()
	if !repoSlug.MatchString(repository) || !fs.ValidPath(repository) || number <= 0 || event.GetPullRequest() == nil {
		return errors.New("expected a pull request event with repository and number")
	}
	if !objectID.MatchString(scan.Base) || !objectID.MatchString(scan.Head) || !objectID.MatchString(scan.Old) || !objectID.MatchString(scan.New) || scan.Attempt <= 0 {
		return errors.New("expected full PR and upstream SHAs and a positive run attempt")
	}
	if strings.TrimSpace(label) == "" {
		return errors.New("-label is required")
	}
	if scan.LabelEvent < 0 || scan.Verdict == acknowledged && scan.LabelEvent == 0 {
		return errors.New("invalid or missing label application ID for review")
	}
	body, err := scan.summaryComment(repository, runID, label)
	if err != nil {
		return err
	}
	owner, repo, _ := strings.Cut(repository, "/")
	existing, err := findSummaryComment(ctx, client, owner, repo, number)
	if err != nil {
		return err
	}
	if existing == nil && scan.Verdict == clean && scan.LabelEvent == 0 {
		return nil
	}
	if scan.LabelEvent != 0 {
		a, err := currentApplication(ctx, client, owner, repo, number, label)
		if err != nil {
			return err
		}
		if a.id != scan.LabelEvent {
			log.Print("Skipping the update: the label was removed or reapplied since this scan.")
			return nil
		}
		if scan.Verdict != acknowledged && (a.ambiguous || !a.existedAt(event) || scan.Verdict == reviewNeeded && scan.recordedIn(existing)) {
			log.Print("Skipping cleanup: a newer label application or acknowledgment superseded this run.")
			return nil
		}
	}
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	if pr.GetState() != "open" || pr.GetHead().GetSHA() != scan.Head || pr.GetBase().GetSHA() != scan.Base {
		log.Print("Skipping the update: the PR changed since this scan.")
		return nil
	}
	if (scan.Verdict != clean || scan.LabelEvent != 0) && hasLabel(pr, label) != (scan.LabelEvent != 0) {
		log.Print("Skipping the update: the review label changed since this scan.")
		return nil
	}
	var removeErr error
	if scan.LabelEvent != 0 && scan.Verdict != acknowledged {
		resp, err := client.Issues.RemoveLabelForIssue(ctx, owner, repo, number, label)
		if err != nil && (resp == nil || resp.StatusCode != http.StatusNotFound) {
			removeErr = fmt.Errorf("remove stale label %q: %w", label, err)
		}
	}
	if existing.GetBody() == body || existing == nil && scan.Verdict == clean {
		return removeErr
	}
	request := github.IssueCommentRequest{Body: body}
	if existing == nil {
		_, _, err = client.Issues.CreateComment(ctx, owner, repo, number, request)
	} else {
		_, _, err = client.Issues.UpdateComment(ctx, owner, repo, existing.GetID(), request)
	}
	return errors.Join(removeErr, err)
}

func (scan scanResult) summaryComment(repository string, runID int64, label string) (string, error) {
	const heading = commentMarker + "### Upstream Go review \u2014 "
	repoURL := "https://github.com/" + repository
	runURL := fmt.Sprintf("%s/actions/runs/%d/attempts/%d", repoURL, runID, scan.Attempt)
	labelText := "`" + label + "`"
	files := " [See the affected files](" + runURL + ")."
	var body string
	switch scan.Verdict {
	case clean:
		body = "no matching changes\n\nNo action needed. [View the scan summary](" + runURL + ")."
	case acknowledged:
		body = fmt.Sprintf("acknowledged\n\nThis review covers upstream Go `%.12s` to `%.12s`. If either pin changes, review the new diff. To request another review now, remove %s under **Labels**.", scan.Old, scan.New, labelText) + files
	case reviewNeeded:
		body = "review needed\n\nAfter reviewing the diffs, open **Labels** (shortcut: `L`) and select " + labelText + "."
		if scan.LabelEvent != 0 {
			body += " If it is still selected, remove it and apply it again."
		}
		body += files
	default:
		return "", fmt.Errorf("unknown review verdict %q", scan.Verdict)
	}
	commitLink := func(sha string) string {
		return fmt.Sprintf("[`%.12s`](%s/commit/%s)", sha, repoURL, sha)
	}
	body = heading + body + "\n\n_Last scanned PR head " + commitLink(scan.Head) + " against base " + commitLink(scan.Base) + "._"
	if scan.Verdict == acknowledged {
		body += scan.ackMarker()
	}
	return body, nil
}
