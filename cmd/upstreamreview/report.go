// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/google/go-github/v92/github"
)

func compareURL(r result) string {
	return fmt.Sprintf("https://github.com/golang/go/compare/%s..%s", r.old, r.new)
}

// Commit statuses can transition from success back to pending on dismissal,
// on the same SHA. Only deduplicate against this installation's bot.
func (g *gate) status(ctx context.Context, sha, state, description, url string) error {
	opts := &github.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := g.client.Repositories.ListStatuses(ctx, g.owner, g.repo, sha, opts)
		if err != nil {
			return err
		}
		found := false
		for _, s := range statuses { // newest first
			if s.GetContext() == checkName && s.GetCreator().GetLogin() == g.slug+"[bot]" {
				if s.GetState() == state && s.GetDescription() == description && s.GetTargetURL() == url {
					return nil
				}
				found = true
				break
			}
		}
		if found || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	_, _, err := g.client.Repositories.CreateStatus(ctx, g.owner, g.repo, sha, github.RepoStatus{
		Context: new(checkName), State: &state, Description: &description, TargetURL: &url,
	})
	return err
}

func (g *gate) report(ctx context.Context, pr *github.PullRequest, r result, needsReview bool) error {
	const marker = "<!-- upstream-crypto-review -->"
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n[Upstream Go diff](%s): %d changed FIPS importer(s). Review the affected operations and update the backend patches if needed.\n\n", marker, compareURL(r), len(r.files))
	for _, f := range r.files {
		// Leave room for the reproduction command within GitHub's comment size limit.
		if b.Len()+len(f.Path) > 55000 {
			b.WriteString("- (list truncated; the command below prints all paths)\n")
			break
		}
		fmt.Fprintf(&b, "- [`%s`](%s#diff-%x)\n", strings.ReplaceAll(f.Path, "`", "'"), compareURL(r), sha256.Sum256([]byte(f.Path)))
	}
	fmt.Fprintf(&b, "\nReproduce with `importdiff -repo PATH_TO_GO -exclude src/crypto/internal/fips140 %s %s`.\n", r.old, r.new)
	body := b.String()
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var id int64
	for {
		comments, resp, err := g.client.Issues.ListComments(ctx, g.owner, g.repo, pr.GetNumber(), opts)
		if err != nil {
			return err
		}
		for _, c := range comments {
			if c.GetUser().GetLogin() == g.slug+"[bot]" && strings.HasPrefix(c.GetBody(), marker+"\n") {
				if c.GetBody() == body {
					id = c.GetID()
					break
				}
				_, _, err := g.client.Issues.UpdateComment(ctx, g.owner, g.repo, c.GetID(), github.IssueCommentRequest{Body: body})
				if err != nil {
					return err
				}
				id = c.GetID()
				break
			}
		}
		if id != 0 || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if id == 0 {
		if _, _, err := g.client.Issues.CreateComment(ctx, g.owner, g.repo, pr.GetNumber(), github.IssueCommentRequest{Body: body}); err != nil {
			return err
		}
	}
	if needsReview && !pr.GetDraft() {
		for _, team := range pr.RequestedTeams {
			if team.GetSlug() == g.team {
				return nil
			}
		}
		_, _, err := g.client.PullRequests.RequestReviewers(ctx, g.owner, g.repo, pr.GetNumber(), github.ReviewersRequest{TeamReviewers: []string{g.team}})
		return err
	}
	return nil
}
