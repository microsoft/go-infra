// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/executil"
)

func migrationBranch(area, source string, replaces int) string {
	branch := "dev/dependency-audit/" + area + "/" + source
	if replaces != 0 {
		branch += "-r" + strconv.Itoa(replaces)
	}
	return branch
}

func parseMigrationBranch(branch, area string) (string, int, bool) {
	suffix, ok := strings.CutPrefix(branch, "dev/dependency-audit/"+area+"/")
	if !ok {
		return "", 0, false
	}
	source, replacement, hasReplacement := strings.Cut(suffix, "-r")
	if !shaPattern.MatchString(source) {
		return "", 0, false
	}
	replaces := 0
	if hasReplacement {
		var err error
		replaces, err = strconv.Atoi(replacement)
		if err != nil || replaces <= 0 || strconv.Itoa(replaces) != replacement {
			return "", 0, false
		}
	}
	return source, replaces, true
}

func migrationHeader(p prepared) string {
	header := areaMarker(p.Area) + "Dependency audit source: " + p.State.HeadSHA + "\n"
	if p.Replaces != 0 {
		header += replacementMarker(p.Replaces)
	}
	return header
}

func matchesReplacement(pull *github.PullRequest, area string, number int) bool {
	if _, replaces, ok := parseMigrationBranch(pull.GetHead().GetRef(), area); ok && replaces == number {
		return true
	}
	return strings.Contains(strings.ReplaceAll(pull.GetBody(), "\r\n", "\n"), replacementMarker(number))
}

func manualPRURL(c config, p prepared) string {
	source, _, _ := parseMigrationBranch(p.WorkBranch, p.Area)
	body := migrationHeader(prepared{Area: p.Area, State: checkpoint{HeadSHA: source}, Replaces: p.Replaces}) +
		fmt.Sprintf("\nTracker: #%d. See the branch commit for the validated migration explanation.", p.Issue)
	query := url.Values{
		"expand": {"1"},
		"title":  {"[dependency-audit] Modernize " + p.Area},
		"body":   {body},
	}
	return fmt.Sprintf("https://github.com/%s/compare/%s...%s?%s",
		c.Repository, url.PathEscape(p.State.Branch), url.PathEscape(p.WorkBranch), query.Encode())
}

func findMigration(ctx context.Context, client *github.Client, c config) (*github.PullRequest, *prepared, error) {
	opts := &github.PullRequestListOptions{State: "all", Sort: "created", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	branches := make(map[string]bool)
	var open, closed *github.PullRequest
	for {
		pulls, response, err := client.PullRequests.List(ctx, c.Owner, c.Repo, opts)
		if err != nil {
			return nil, nil, err
		}
		for _, pull := range pulls {
			if !matchesArea(pull, c, c.Area) {
				continue
			}
			if pull.GetHead().GetRepo().GetFullName() == c.Repository {
				branches[pull.GetHead().GetRef()] = true
			}
			if pull.GetState() == "open" {
				if open != nil {
					return nil, nil, errors.New("multiple open migration PRs exist for this area")
				}
				open = pull
			} else if closed == nil {
				closed = pull
			}
		}
		if response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	if open != nil {
		return open, nil, nil
	}
	refs, _, err := client.Git.ListMatchingRefs(ctx, c.Owner, c.Repo, "heads/dev/dependency-audit/"+c.Area+"/")
	if err != nil {
		return nil, nil, fmt.Errorf("find pending migration branches: %w", err)
	}
	var found *prepared
	for _, ref := range refs {
		branch := strings.TrimPrefix(ref.GetRef(), "refs/heads/")
		source, replaces, ok := parseMigrationBranch(branch, c.Area)
		if !ok || branches[branch] {
			continue
		}
		commit, _, err := client.Git.GetCommit(ctx, c.Owner, c.Repo, ref.GetObject().GetSHA())
		if err != nil {
			return nil, nil, err
		}
		p := prepared{Area: c.Area, WorkBranch: branch, Replaces: replaces, State: checkpoint{HeadSHA: source}}
		if len(commit.Parents) != 1 || commit.Parents[0].GetSHA() != source ||
			!strings.HasPrefix(commit.GetMessage(), "Prepare dependency migration\n\n"+migrationHeader(p)) {

			return nil, nil, fmt.Errorf("branch %q has no matching validated migration identity; review it without overwriting it", branch)
		}
		if found != nil {
			return nil, nil, errors.New("multiple migration branches are awaiting a PR for this area")
		}
		found = &p
	}
	return closed, found, nil
}

func migrationGit(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
	return executil.Output(command)
}

func publishManualBranch(ctx context.Context, client *github.Client, c config, p prepared, body string) error {
	head, err := migrationGit(ctx, c.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	tree, err := migrationGit(ctx, c.Worktree, "write-tree")
	if err != nil {
		return err
	}
	if !shaPattern.MatchString(c.Tree) || strings.TrimSpace(head) != p.State.HeadSHA || strings.TrimSpace(tree) != c.Tree {
		return errors.New("manual migration does not match the independently validated source and tree")
	}
	names, err := migrationGit(ctx, c.Worktree, "diff", "--cached", "--no-renames", "--name-only", "-z")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return errors.New("manual migration must contain between 1 and 20 validated Go files")
	}
	files := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	if len(files) > 20 {
		return errors.New("manual migration must contain between 1 and 20 validated Go files")
	}
	base, _, err := client.Git.GetCommit(ctx, c.Owner, c.Repo, p.State.HeadSHA)
	if err != nil {
		return err
	}
	entries := make([]*github.TreeEntry, 0, len(files))
	for _, file := range files {
		if !safePath(file) || !strings.HasSuffix(file, ".go") {
			return fmt.Errorf("invalid validated migration path %q", file)
		}
		entry := &github.TreeEntry{Path: new(file)}
		staged, err := migrationGit(ctx, c.Worktree, "ls-files", "--stage", "--", file)
		if err != nil {
			return err
		}
		if len(staged) != 0 {
			mode, _, _ := strings.Cut(staged, " ")
			if mode != "100644" && mode != "100755" {
				return fmt.Errorf("unsupported migration file mode %q", mode)
			}
			content, err := migrationGit(ctx, c.Worktree, "show", ":"+file)
			if err != nil {
				return err
			}
			if !utf8.ValidString(content) {
				return fmt.Errorf("migration source %q is not valid UTF-8 Go source", file)
			}
			entry.Mode, entry.Type, entry.Content = new(mode), new("blob"), new(content)
		}
		entries = append(entries, entry)
	}
	result, _, err := client.Git.CreateTree(ctx, c.Owner, c.Repo, base.GetTree().GetSHA(), entries)
	if err != nil {
		return err
	}
	if result.GetSHA() != c.Tree {
		return errors.New("published migration tree does not match independent validation")
	}
	commit, _, err := client.Git.CreateCommit(ctx, c.Owner, c.Repo, github.Commit{
		Message: new("Prepare dependency migration\n\n" + body), Tree: result,
		Parents: []*github.Commit{{SHA: new(p.State.HeadSHA)}},
	}, nil)
	if err != nil {
		return err
	}
	_, _, err = client.Git.CreateRef(ctx, c.Owner, c.Repo, github.CreateRef{
		Ref: "refs/heads/" + p.WorkBranch, SHA: commit.GetSHA(),
	})
	if err != nil {
		return fmt.Errorf("create migration branch without replacing an existing ref: %w", err)
	}
	return nil
}
