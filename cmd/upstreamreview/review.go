// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/importdiff"
)

const checkName = "upstream-crypto-review"

var objectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

type result struct {
	base, head, merge string
	old, new          string
	files             []importdiff.Change
}

// The worker caches immutable Git comparisons and checks reviews and team
// membership on each reconciliation.
type gate struct {
	client                  *github.Client
	owner, repo, team, slug string
	compare                 func(context.Context, string, string) ([]importdiff.Change, error)
	scans                   map[int]result
}

func compareMirror(dir string) func(context.Context, string, string) ([]importdiff.Change, error) {
	return func(ctx context.Context, old, updated string) ([]importdiff.Change, error) {
		for _, id := range []string{old, updated} {
			if err := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-e", id+"^{commit}").Run(); err != nil {
				// Refresh from the upstream remote configured by the operator.
				if out, err := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--quiet", "origin").CombinedOutput(); err != nil {
					return nil, fmt.Errorf("refresh upstream mirror: %w: %s", err, out)
				}
				break
			}
		}
		return importdiff.Compare(ctx, dir, old, updated, importdiff.Rule{
			SourceRoots: []string{"src"},
			ImportRoots: []string{"crypto/internal/fips140"},
			ExcludeDir:  "src/crypto/internal/fips140",
		})
	}
}

func (g *gate) gitlink(ctx context.Context, commit *github.Commit) (string, error) {
	tree, _, err := g.client.Git.GetTree(ctx, g.owner, g.repo, commit.GetTree().GetSHA(), false)
	if err != nil {
		return "", err
	}
	for _, e := range tree.Entries {
		if e.GetPath() == "go" && e.GetMode() == "160000" && e.GetType() == "commit" && objectID.MatchString(e.GetSHA()) {
			return e.GetSHA(), nil
		}
	}
	return "", fmt.Errorf("no valid go gitlink at %s", commit.GetSHA())
}

func (g *gate) scan(ctx context.Context, pr *github.PullRequest) (result, error) {
	n := pr.GetNumber()
	prev := g.scans[n]
	r := result{base: pr.GetBase().GetSHA(), head: pr.GetHead().GetSHA(), merge: pr.GetMergeCommitSHA()}
	if !objectID.MatchString(r.base) || !objectID.MatchString(r.head) || !objectID.MatchString(r.merge) {
		return result{}, errors.New("waiting for current PR and test merge revisions")
	}
	if prev.merge == r.merge && prev.base == r.base && prev.head == r.head {
		return prev, nil
	}
	merge, _, err := g.client.Git.GetCommit(ctx, g.owner, g.repo, r.merge)
	if err != nil {
		return result{}, err
	}
	if len(merge.Parents) != 2 || merge.Parents[0].GetSHA() != r.base || merge.Parents[1].GetSHA() != r.head {
		return result{}, errors.New("waiting for GitHub to recompute the test merge")
	}
	base, _, err := g.client.Git.GetCommit(ctx, g.owner, g.repo, r.base)
	if err != nil {
		return result{}, err
	}
	if r.old, err = g.gitlink(ctx, base); err != nil {
		return result{}, err
	}
	if r.new, err = g.gitlink(ctx, merge); err != nil {
		return result{}, err
	}
	switch {
	case r.old == r.new:
	case prev.old == r.old && prev.new == r.new:
		r.files = prev.files
	default:
		r.files, err = g.compare(ctx, r.old, r.new)
		if err != nil {
			return result{}, err
		}
	}
	g.scans[n] = r
	return r, nil
}

func (g *gate) approved(ctx context.Context, pr *github.PullRequest) (bool, error) {
	if pr.GetUser().GetLogin() == "" {
		return false, errors.New("missing PR author")
	}
	// ListReviews returns chronological order. A comment doesn't withdraw
	// an approval, but a later dismissal or request for changes does.
	approved := make(map[string]bool)
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := g.client.PullRequests.ListReviews(ctx, g.owner, g.repo, pr.GetNumber(), opts)
		if err != nil {
			return false, err
		}
		for _, r := range reviews {
			user := r.GetUser()
			login := strings.ToLower(user.GetLogin())
			if login == "" || user.GetType() != "User" || strings.EqualFold(login, pr.GetUser().GetLogin()) {
				continue
			}
			switch r.GetState() {
			case "APPROVED":
				approved[login] = r.GetCommitID() == pr.GetHead().GetSHA()
			case "CHANGES_REQUESTED", "DISMISSED":
				delete(approved, login)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	for login, current := range approved {
		if !current {
			continue
		}
		m, resp, err := g.client.Teams.GetTeamMembershipBySlug(ctx, g.owner, g.team, login)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				continue
			}
			return false, err
		}
		if m.GetState() == "active" {
			return true, nil
		}
	}
	return false, nil
}

func (g *gate) reconcile(ctx context.Context, n int) error {
	pr, _, err := g.client.PullRequests.Get(ctx, g.owner, g.repo, n)
	if err != nil {
		return err
	}
	if pr.GetState() != "open" {
		delete(g.scans, n)
		return nil
	}
	sha := pr.GetMergeCommitSHA()
	if !objectID.MatchString(sha) {
		return errors.New("test merge not available")
	}
	failed := func(cause error) error {
		return errors.Join(cause, g.status(ctx, sha, "error", "Could not verify this PR; will retry", pr.GetHTMLURL()))
	}
	r, err := g.scan(ctx, pr)
	if err != nil {
		return failed(err)
	}
	if len(r.files) == 0 {
		return g.status(ctx, sha, "success", "No upstream FIPS importer changes", pr.GetHTMLURL())
	}
	ok, err := g.approved(ctx, pr)
	if err != nil {
		return failed(err)
	}
	link := compareURL(r)
	if !ok {
		// Block first, before any optional reviewer notification can fail.
		if err := g.status(ctx, sha, "pending", "Upstream crypto changes need a team approval on the current head", link); err != nil {
			return err
		}
	}
	if err := g.report(ctx, pr, r, !ok); err != nil {
		if !ok {
			// A notification failure doesn't change the pending verdict.
			return err
		}
		return failed(err)
	}
	if ok {
		return g.status(ctx, sha, "success", "Upstream crypto changes reviewed on the current head", link)
	}
	return nil
}
