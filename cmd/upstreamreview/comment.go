// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/google/go-github/v92/github"
)

const commentMarker = "<!-- upstreamreview:summary -->\n"

func (g *gate) findSummaryComment(ctx context.Context, number int) (*github.IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := g.client.Issues.ListComments(ctx, g.owner, g.repo, number, opts)
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

func (g *gate) updatePRComment(ctx context.Context, number int, event *github.PullRequestEvent, scan scanResult) error {
	repository := g.owner + "/" + g.repo
	if event.GetRepo().GetFullName() != repository || event.GetNumber() != number || event.GetPullRequest() == nil {
		return errors.New("expected the pull request event for the checked repository and PR number")
	}
	runID, err := strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
	if err != nil || runID <= 0 {
		return errors.New("GITHUB_RUN_ID must be a positive workflow run ID")
	}
	attempt, err := strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil || attempt <= 0 {
		return errors.New("GITHUB_RUN_ATTEMPT must be a positive number")
	}
	label := g.policy.label
	body, err := scan.summaryComment(repository, runID, attempt, label)
	if err != nil {
		return err
	}
	existing, err := g.findSummaryComment(ctx, number)
	if err != nil {
		return err
	}
	noCommentNeeded := existing == nil && scan.Verdict == clean
	removeLabel := scan.LabelEvent != 0 && scan.Verdict != acknowledged
	if noCommentNeeded && !removeLabel {
		return nil
	}
	if scan.LabelEvent != 0 {
		a, err := g.currentLabelApplication(ctx, number)
		if err != nil {
			return err
		}
		if a.id != scan.LabelEvent {
			log.Print("Skipping the update: the label was removed or reapplied since this scan.")
			return nil
		}
		alreadyAcknowledged := scan.Verdict == reviewNeeded && scan.recordedIn(existing)
		if removeLabel && (a.ambiguous || !a.existedAt(event) || alreadyAcknowledged) {
			log.Print("Skipping cleanup: a newer label application or acknowledgment superseded this run.")
			return nil
		}
	}
	pr, _, err := g.client.PullRequests.Get(ctx, g.owner, g.repo, number)
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
	if removeLabel {
		resp, err := g.client.Issues.RemoveLabelForIssue(ctx, g.owner, g.repo, number, label)
		if err != nil && (resp == nil || resp.StatusCode != http.StatusNotFound) {
			removeErr = fmt.Errorf("remove stale label %q: %w", label, err)
		}
	}
	if noCommentNeeded || existing.GetBody() == body {
		return removeErr
	}
	request := github.IssueCommentRequest{Body: body}
	if existing == nil {
		_, _, err = g.client.Issues.CreateComment(ctx, g.owner, g.repo, number, request)
	} else {
		_, _, err = g.client.Issues.UpdateComment(ctx, g.owner, g.repo, existing.GetID(), request)
	}
	return errors.Join(removeErr, err)
}

func (scan scanResult) summaryComment(repository string, runID int64, attempt int, label string) (string, error) {
	const heading = commentMarker + "### Upstream Go review \u2014 "
	repoURL := "https://github.com/" + repository
	runURL := fmt.Sprintf("%s/actions/runs/%d/attempts/%d", repoURL, runID, attempt)
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
