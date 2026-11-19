// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/internal/importdiff"
)

var (
	objectID       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoSlug       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+$`)
	errAckRequired = errors.New("acknowledgment required")
)

type policy struct {
	upstream, submodule, label string
	rule                       importdiff.Rule
}

type result struct {
	old, new string
	files    []importdiff.Change
}

func (r result) summary(head string, p policy) string {
	link := fmt.Sprintf("https://github.com/%s/compare/%s..%s", p.upstream, r.old, r.new)
	var b strings.Builder
	fmt.Fprintf(&b, "[Upstream diff](%s): %d changed Go importer(s) at PR head `%s`.\n\n", link, len(r.files), head)
	for _, file := range r.files {
		if b.Len()+len(file.Path) > 900*1024 { // Step summaries are limited to 1 MiB.
			b.WriteString("- (list truncated; the command below prints all paths)\n")
			break
		}
		fmt.Fprintf(&b, "- [`%s`](%s#diff-%x)\n", strings.ReplaceAll(file.Path, "`", "'"), link, sha256.Sum256([]byte(file.Path)))
	}
	b.WriteString("\nReproduce in an upstream clone:\n\n```sh\nimportdiff -repo PATH_TO_UPSTREAM")
	writeFlags := func(name string, values []string) {
		for _, v := range values {
			fmt.Fprintf(&b, " -%s '%s'", name, strings.ReplaceAll(v, "'", `'\''`))
		}
	}
	writeFlags("src", p.rule.SourceRoots)
	writeFlags("import", p.rule.ImportRoots)
	writeFlags("exclude", p.rule.ExcludeDirs)
	fmt.Fprintf(&b, " %s %s\n```\n", r.old, r.new)
	return b.String()
}

type gate struct {
	client      *github.Client
	owner, repo string
	policy      policy
	compare     func(context.Context, string, string) ([]importdiff.Change, error)
}

func newGate(client *github.Client, repository string, p policy) (*gate, error) {
	if !repoSlug.MatchString(repository) || !fs.ValidPath(repository) {
		return nil, errors.New("-repo must be in owner/repo form")
	}
	if !repoSlug.MatchString(p.upstream) || !fs.ValidPath(p.upstream) {
		return nil, errors.New("-upstream must be in owner/repo form")
	}
	if !fs.ValidPath(p.submodule) || p.submodule == "." || strings.ContainsAny(p.submodule, "\\\x00") {
		return nil, errors.New("-submodule must be a slash-separated path relative to the repository root")
	}
	if strings.TrimSpace(p.label) == "" {
		return nil, errors.New("-label is required")
	}
	if err := p.rule.Validate(); err != nil {
		return nil, err
	}
	owner, repo, _ := strings.Cut(repository, "/")
	return &gate{
		client: client, owner: owner, repo: repo, policy: p,
		compare: compareUpstream("https://github.com/"+p.upstream+".git", p.rule),
	}, nil
}

func compareUpstream(remote string, rule importdiff.Rule) func(context.Context, string, string) ([]importdiff.Change, error) {
	return func(ctx context.Context, old, updated string) ([]importdiff.Change, error) {
		dir, err := os.MkdirTemp("", "upstream-review-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		if out, err := exec.CommandContext(ctx, "git", "init", "--quiet", dir).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("initialize upstream repo: %w: %s", err, out)
		}
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--quiet", "--depth=1", "--no-tags", "--", remote, old, updated).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("fetch upstream revisions: %w: %s", err, out)
		}
		return importdiff.Compare(ctx, dir, old, updated, rule)
	}
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", args...).Output()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && len(exit.Stderr) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}

func gitlink(ctx context.Context, rev, submodule string) (string, error) {
	out, err := gitOutput(ctx, "--literal-pathspecs", "ls-tree", "-z", "--full-tree", rev, "--", submodule)
	if err != nil {
		return "", fmt.Errorf("read gitlink %q at %s: %w", submodule, rev, err)
	}
	meta, path, _ := strings.Cut(out, "\t")
	fields := strings.Fields(meta)
	if path != submodule+"\x00" || len(fields) != 3 || fields[0] != "160000" || fields[1] != "commit" || !objectID.MatchString(fields[2]) {
		return "", fmt.Errorf("no valid gitlink %q at %s", submodule, rev)
	}
	return fields[2], nil
}

func (g *gate) scan(ctx context.Context, baseSHA, mergeSHA string) (result, error) {
	old, err := gitlink(ctx, baseSHA, g.policy.submodule)
	if err != nil {
		return result{}, err
	}
	updated, err := gitlink(ctx, mergeSHA, g.policy.submodule)
	if err != nil {
		return result{}, err
	}
	r := result{old: old, new: updated}
	if r.old != r.new {
		r.files, err = g.compare(ctx, r.old, r.new)
		if err != nil {
			return result{}, err
		}
	}
	return r, nil
}

func (g *gate) check(ctx context.Context, n int, mergeSHA string) (string, error) {
	out, err := gitOutput(ctx, "rev-list", "-1", "--parents", "HEAD", "--")
	if err != nil {
		return "", fmt.Errorf("read test merge parents: %w", err)
	}
	revs := strings.Fields(out)
	if len(revs) != 3 || revs[0] != mergeSHA {
		return "", fmt.Errorf("expected a checkout of test merge %s with both parents", mergeSHA)
	}
	base, head := revs[1], revs[2]
	r, err := g.scan(ctx, base, mergeSHA)
	if err != nil {
		return "", err
	}

	// The PR head, base, or labels may have changed during the upstream scan.
	pr, _, err := g.client.PullRequests.Get(ctx, g.owner, g.repo, n)
	if err != nil {
		return "", err
	}
	if pr.GetState() != "open" {
		return "", fmt.Errorf("PR #%d is not open", n)
	}
	if base != pr.GetBase().GetSHA() || head != pr.GetHead().GetSHA() {
		return "", fmt.Errorf("test merge %s does not match the current PR base %s and head %s", mergeSHA, pr.GetBase().GetSHA(), pr.GetHead().GetSHA())
	}
	if len(r.files) == 0 {
		return "No upstream importer changes.\n", nil
	}
	summary := r.summary(head, g.policy)
	for _, label := range pr.Labels {
		if label.GetName() == g.policy.label {
			return "**Acknowledged** by the `" + g.policy.label + "` PR label.\n\n" + summary, nil
		}
	}
	return "**Acknowledgment required.** Review the affected files and add the `" + g.policy.label + "` label to this PR.\n\n" + summary, fmt.Errorf("%w: add PR label %q", errAckRequired, g.policy.label)
}
