// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// dependencyaudit prepares history checkpoints and validates native gh-aw outputs.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

const marker = "<!-- dependency-audit:v1\n"

var (
	repoPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	slugPattern  = regexp.MustCompile("^[a-z0-9]+(?:-[a-z0-9]+)*$")
	areaHeading  = regexp.MustCompile("^### `([a-z0-9]+(?:-[a-z0-9]+)*)`")
	resolvedArea = regexp.MustCompile("^- `([a-z0-9]+(?:-[a-z0-9]+)*)`: (.+)")
	moduleLine   = regexp.MustCompile("(?m)^- Module: `([^`\\r\\n]+)`")
	botPattern   = regexp.MustCompile(`^[a-zA-Z0-9-]+\[bot\]$`)
)

type checkpoint struct {
	Repository string   `json:"repository"`
	Branch     string   `json:"branch"`
	BaseSHA    string   `json:"base_sha"`
	HeadSHA    string   `json:"head_sha"`
	Previous   int      `json:"previous_issue"`
	Areas      []string `json:"areas"`
}

type prepared struct {
	State      checkpoint `json:"checkpoint"`
	Publisher  string     `json:"publisher"`
	Issue      int        `json:"issue"`
	Previous   string     `json:"previous"`
	Area       string     `json:"area"`
	Module     string     `json:"module"`
	WorkBranch string     `json:"work_branch"`
	Pull       int        `json:"pull_request"`
	Replaces   int        `json:"replaces_pr"`
	Comment    int64      `json:"comment_id"`
}

type nativeRequest struct {
	Type        string          `json:"type"`
	Title       string          `json:"title,omitempty"`
	Body        string          `json:"body,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	Repo        string          `json:"repo,omitempty"`
	IssueNumber int             `json:"issue_number,omitempty"`
	ItemNumber  int             `json:"item_number,omitempty"`
	CommentID   int64           `json:"comment_id,omitempty"`
	TemporaryID string          `json:"temporary_id,omitempty"`
	Draft       bool            `json:"draft,omitempty"`
	Integrity   json.RawMessage `json:"integrity,omitempty"`
	Secrecy     json.RawMessage `json:"secrecy,omitempty"`
}

type config struct {
	Command, Repository, Owner, Repo, Initial, Area string
	Input, Output, Publisher, Token, GitHubOutput   string
	Issue, Replace                                  int
	Repositories                                    []string
	Private                                         bool
}

func main() {
	log.SetPrefix("dependencyaudit: ")
	log.SetFlags(0)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func parseConfig(args []string, getenv func(string) string, stderr io.Writer) (config, error) {
	var c config
	if len(args) == 0 || !slices.Contains([]string{"targets", "prepare", "stamp", "link"}, args[0]) {
		return c, errors.New("expected targets, prepare, stamp, or link")
	}
	c.Command = args[0]
	fs := flag.NewFlagSet("dependencyaudit "+c.Command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.Repository, "repo", "", "enrolled owner/repo")
	fs.StringVar(&c.Initial, "initial-sha", "", "exclusive starting commit for the first audit")
	fs.StringVar(&c.Area, "area", "", "area to apply; empty means audit")
	fs.IntVar(&c.Issue, "issue", 0, "optional assertion of the current tracker number")
	fs.IntVar(&c.Replace, "replace-pr", 0, "closed, unmerged migration PR to explicitly replace")
	fs.StringVar(&c.Input, "input", "", "prepared context JSON")
	fs.StringVar(&c.Output, "output", "", "prepared context JSON destination")
	fs.StringVar(&c.Publisher, "publisher-login", "", "publishing GitHub App bot login")
	if err := fs.Parse(args[1:]); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if err := json.Unmarshal([]byte(getenv("DEPENDENCY_AUDIT_REPOSITORIES")), &c.Repositories); err != nil {
		return c, fmt.Errorf("DEPENDENCY_AUDIT_REPOSITORIES must be a JSON array: %w", err)
	}
	seen := make(map[string]bool)
	for i, repository := range c.Repositories {
		repository = strings.ToLower(repository)
		if !repoPattern.MatchString(repository) || seen[repository] {
			return c, fmt.Errorf("invalid or duplicate enrolled repository %q", repository)
		}
		seen[repository], c.Repositories[i] = true, repository
	}
	if len(c.Repositories) == 0 {
		return c, errors.New("enroll at least one repository in DEPENDENCY_AUDIT_REPOSITORIES")
	}
	c.Repository = strings.ToLower(c.Repository)
	if c.Repository != "" && !seen[c.Repository] {
		return c, fmt.Errorf("repository %q is not enrolled", c.Repository)
	}
	if c.Repository == "" && c.Command != "targets" {
		return c, errors.New("repo is required")
	}
	if c.Initial != "" && (!shaPattern.MatchString(c.Initial) || c.Repository == "" || c.Area != "") {
		return c, errors.New("initial-sha requires one audit target and a lowercase 40-character SHA")
	}
	apply := c.Area != ""
	if apply && (c.Repository == "" || !slugPattern.MatchString(c.Area) || len(c.Area) > 80 || getenv("DEPENDENCY_AUDIT_ENABLE_APPLY") != "true") {
		return c, errors.New("apply requires a valid area slug and DEPENDENCY_AUDIT_ENABLE_APPLY=true")
	}
	if c.Issue < 0 || (c.Issue != 0 && !apply) {
		return c, errors.New("issue must be nonnegative and is only valid when applying an area")
	}
	if c.Replace < 0 || (c.Replace != 0 && !apply) || (c.Command == "link" && !apply) {
		return c, errors.New("link and replace-pr require an apply area; replace-pr must be nonnegative")
	}
	c.Owner, c.Repo, _ = strings.Cut(c.Repository, "/")
	c.Token, c.GitHubOutput = getenv("GH_TOKEN"), getenv("GITHUB_OUTPUT")
	if c.Token == "" {
		c.Token = getenv("GITHUB_TOKEN")
	}
	c.Private = getenv("DEPENDENCY_AUDIT_CONTROL_PRIVATE") == "true"
	if c.Command != "targets" && (c.Token == "" || !botPattern.MatchString(c.Publisher)) {
		return c, errors.New("GH_TOKEN or GITHUB_TOKEN and publisher-login identifying a GitHub App bot are required")
	}
	if (c.Command == "prepare" && c.Output == "") || (slices.Contains([]string{"stamp", "link"}, c.Command) && c.Input == "") {
		return c, errors.New("prepare requires output; stamp and link require input")
	}
	return c, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	c, err := parseConfig(args, getenv, stderr)
	if err != nil {
		return err
	}
	values := map[string]string{"owner": c.Owner, "repo": c.Repo, "repository": c.Repository, "key": fmt.Sprintf("%x", sha256.Sum256([]byte(c.Repository)))}
	if c.Command == "targets" {
		repositories := c.Repositories
		if c.Repository != "" {
			repositories = []string{c.Repository}
		}
		data, err := json.Marshal(repositories)
		if err != nil {
			return err
		}
		values["repositories"] = string(data)
		fmt.Fprintln(stdout, string(data))
		return outputs(c.GitHubOutput, values)
	}
	client, err := githubutil.NewClient(ctx, c.Token)
	if err != nil {
		return err
	}
	if c.Command == "stamp" || c.Command == "link" {
		var p prepared
		if err := readJSON(c.Input, &p); err != nil {
			return err
		}
		if c.Command == "link" {
			link, err := linkExisting(ctx, client, c, p)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Linked existing migration PR %s; no branch or PR was changed.\n", link)
			return nil
		}
		return stamp(ctx, client, c, p, getenv("GH_AW_AGENT_OUTPUT"))
	}
	p, err := prepare(ctx, client, c)
	if err != nil {
		return err
	}
	if err := writeJSON(c.Output, p); err != nil {
		return err
	}
	values["branch"], values["base_sha"], values["head_sha"] = p.State.Branch, p.State.BaseSHA, p.State.HeadSHA
	values["work_branch"], values["module"] = p.WorkBranch, p.Module
	values["tracker_issue"], values["pull_request"] = fmt.Sprint(p.Issue), fmt.Sprint(p.Pull)
	values["replaces_pr"] = fmt.Sprint(p.Replaces)
	values["comment_id"] = ""
	if p.Comment != 0 {
		values["comment_id"] = fmt.Sprint(p.Comment)
	}
	return outputs(c.GitHubOutput, values)
}

func readCheckpoint(issue *github.Issue, publisher string) (*checkpoint, error) {
	if issue.IsPullRequest() || issue.GetUser().GetLogin() != publisher || issue.GetUser().GetType() != "Bot" {
		return nil, nil
	}
	body := strings.ReplaceAll(issue.GetBody(), "\r\n", "\n")
	start := strings.Index(body, "<!-- dependency-audit:")
	if start < 0 {
		return nil, nil
	}
	if !strings.HasPrefix(body[start:], marker) || strings.Count(body, marker) != 1 {
		return nil, errors.New("unsupported or duplicate tracker checkpoint metadata")
	}
	encoded, _, ok := strings.Cut(strings.TrimPrefix(body[start:], marker), "\n-->")
	var state checkpoint
	if !ok || json.Unmarshal([]byte(encoded), &state) != nil || !repoPattern.MatchString(state.Repository) ||
		state.Branch == "" || !shaPattern.MatchString(state.BaseSHA) || !shaPattern.MatchString(state.HeadSHA) ||
		state.Areas == nil || state.Previous < 0 || state.Previous == issue.GetNumber() {

		return nil, fmt.Errorf("tracker #%d has corrupt checkpoint metadata", issue.GetNumber())
	}
	seen := make(map[string]bool)
	for _, slug := range state.Areas {
		if !slugPattern.MatchString(slug) || seen[slug] {
			return nil, errors.New("checkpoint contains invalid or duplicate area slugs")
		}
		seen[slug] = true
	}
	return &state, nil
}

func latest(ctx context.Context, client *github.Client, c config) (*github.Issue, *checkpoint, error) {
	opts := &github.IssueListByRepoOptions{State: "all", Creator: c.Publisher, Sort: "created", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		issues, response, err := client.Issues.ListByRepo(ctx, c.Owner, c.Repo, opts)
		if err != nil {
			return nil, nil, err
		}
		for _, issue := range issues {
			state, err := readCheckpoint(issue, c.Publisher)
			if err != nil {
				return nil, nil, err
			}
			if state != nil {
				if state.Repository != c.Repository {
					return nil, nil, errors.New("checkpoint repository does not match the enrolled target")
				}
				return issue, state, nil
			}
		}
		if response.NextPage == 0 {
			return nil, nil, nil
		}
		opts.ListOptions.Page = response.NextPage
	}
}

func currentHead(ctx context.Context, client *github.Client, c config) (string, string, error) {
	repo, _, err := client.Repositories.Get(ctx, c.Owner, c.Repo)
	if err != nil {
		return "", "", err
	}
	if repo.GetPrivate() && !c.Private {
		return "", "", errors.New("private target source cannot be copied into a public central repository")
	}
	branch, _, err := client.Repositories.GetBranch(ctx, c.Owner, c.Repo, repo.GetDefaultBranch(), 0)
	if err != nil {
		return "", "", err
	}
	if repo.GetDefaultBranch() == "" || !shaPattern.MatchString(branch.GetCommit().GetSHA()) {
		return "", "", errors.New("target has no valid default branch head")
	}
	return repo.GetDefaultBranch(), branch.GetCommit().GetSHA(), nil
}

func prepare(ctx context.Context, client *github.Client, c config) (prepared, error) {
	p := prepared{Publisher: c.Publisher, Area: c.Area}
	branch, head, err := currentHead(ctx, client, c)
	if err != nil {
		return p, err
	}
	p.State = checkpoint{Repository: c.Repository, Branch: branch, BaseSHA: c.Initial, HeadSHA: head, Areas: []string{}}
	p.WorkBranch = "dev/dependency-audit/" + c.Area
	issue, previous, err := latest(ctx, client, c)
	if err != nil {
		return p, err
	}
	if previous == nil {
		if c.Area != "" || c.Initial == "" {
			return p, errors.New("no checkpoint exists; run the first audit with an explicit initial-sha")
		}
		return p, nil
	}
	if previous.Branch != branch || c.Initial != "" {
		return p, errors.New("default branch changed or initial-sha would reset an existing checkpoint")
	}
	p.Issue, p.Previous = issue.GetNumber(), issue.GetBody()
	p.State.BaseSHA, p.State.Previous, p.State.Areas = previous.HeadSHA, p.Issue, previous.Areas
	if previous.HeadSHA == head {
		p.State = *previous
	}
	if c.Area == "" {
		return p, nil
	}
	p.State = *previous
	if issue.GetState() != "open" || (c.Issue != 0 && c.Issue != p.Issue) || !slices.Contains(previous.Areas, c.Area) {
		return p, errors.New("apply requires a selected area in the current open tracker")
	}
	pull, err := findPull(ctx, client, c, c.Area)
	if err != nil {
		return p, err
	}
	p.Replaces = c.Replace
	if pull != nil && pull.GetState() == "open" {
		if c.Replace != 0 && !strings.Contains(pull.GetBody(), areaMarker(c.Area)+replacementMarker(c.Replace)) {
			return p, fmt.Errorf("migration PR #%d is still open; close it before explicitly replacing it", pull.GetNumber())
		}
		p.Pull = pull.GetNumber()
	} else {
		if previous.HeadSHA != head {
			return p, errors.New("run an audit at the current default branch head before creating a fresh draft")
		}
		if c.Replace == 0 && pull != nil {
			return p, fmt.Errorf("previous migration PR #%d is closed; review it and use replace-pr explicitly for a new draft", pull.GetNumber())
		}
		if c.Replace != 0 {
			if err := checkReplacement(ctx, client, c, c.Replace); err != nil {
				return p, err
			}
		}
		module := moduleLine.FindStringSubmatch(areaSection(p.Previous, c.Area))
		if len(module) != 2 || !safePath(module[1]) || path.Base(module[1]) != "go.mod" {
			return p, errors.New("selected area must identify its owning go.mod with '- Module: `path/go.mod`'")
		}
		p.Module = path.Dir(module[1])
	}
	comment, err := findComment(ctx, client, c, p.Issue, c.Area)
	if err != nil {
		return p, err
	}
	if comment != nil {
		p.Comment = comment.GetID()
	}
	return p, nil
}

func findComment(ctx context.Context, client *github.Client, c config, issue int, area string) (*github.IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var found *github.IssueComment
	for {
		comments, response, err := client.Issues.ListComments(ctx, c.Owner, c.Repo, issue, opts)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			if comment.GetUser().GetLogin() == c.Publisher && strings.HasPrefix(comment.GetBody(), areaMarker(area)) {
				if found != nil {
					return nil, errors.New("multiple managed comments exist for the selected area")
				}
				found = comment
			}
		}
		if response.NextPage == 0 {
			return found, nil
		}
		opts.Page = response.NextPage
	}
}

func areaMarker(slug string) string { return "<!-- dependency-audit-area:" + slug + " -->\n" }

func replacementMarker(number int) string {
	return fmt.Sprintf("<!-- dependency-audit-replaces:%d -->\n", number)
}

func matchesArea(pull *github.PullRequest, c config, slug string) bool {
	return pull.GetUser().GetLogin() == c.Publisher && pull.GetUser().GetType() == "Bot" &&
		(strings.Contains(pull.GetBody(), areaMarker(slug)) || pull.GetTitle() == "[dependency-audit] Modernize "+slug)
}

func findPull(ctx context.Context, client *github.Client, c config, slug string) (*github.PullRequest, error) {
	opts := &github.PullRequestListOptions{State: "all", Sort: "created", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	var open, closed *github.PullRequest
	for {
		pulls, response, err := client.PullRequests.List(ctx, c.Owner, c.Repo, opts)
		if err != nil {
			return nil, err
		}
		for _, pull := range pulls {
			if matchesArea(pull, c, slug) {
				if pull.GetState() == "open" {
					if open != nil {
						return nil, errors.New("multiple open migration PRs exist for this area")
					}
					open = pull
				} else if closed == nil {
					closed = pull
				}
			}
		}
		if response.NextPage == 0 {
			if open != nil {
				return open, nil
			}
			return closed, nil
		}
		opts.Page = response.NextPage
	}
}

func checkReplacement(ctx context.Context, client *github.Client, c config, number int) error {
	pull, _, err := client.PullRequests.Get(ctx, c.Owner, c.Repo, number)
	if err != nil {
		return err
	}
	if !matchesArea(pull, c, c.Area) || pull.GetState() != "closed" || pull.GetMerged() {
		return errors.New("replace-pr must identify a closed, unmerged migration PR for the selected area; PRs are never closed automatically")
	}
	return nil
}

func migrationComment(p prepared, reference string) string {
	body := areaMarker(p.Area) + "Migration PR: " + reference + ". "
	if p.Replaces != 0 {
		body += fmt.Sprintf("Replaces #%d. ", p.Replaces)
	}
	return body + "The area stays open until a later audit verifies the merged code."
}

func linkExisting(ctx context.Context, client *github.Client, c config, p prepared) (string, error) {
	if p.State.Repository != c.Repository || p.Publisher != c.Publisher || p.Area != c.Area || p.Replaces != c.Replace || p.Pull <= 0 {
		return "", errors.New("prepared existing-PR context does not match this apply request")
	}
	issue, state, err := latest(ctx, client, c)
	if err != nil {
		return "", err
	}
	if issue == nil || issue.GetNumber() != p.Issue || issue.GetState() != "open" ||
		state.HeadSHA != p.State.HeadSHA || !slices.Contains(state.Areas, c.Area) {

		return "", errors.New("selected tracker changed; rerun apply to refresh its existing PR link")
	}
	pull, _, err := client.PullRequests.Get(ctx, c.Owner, c.Repo, p.Pull)
	if err != nil {
		return "", err
	}
	if !matchesArea(pull, c, c.Area) || pull.GetState() != "open" {
		return "", errors.New("existing migration PR changed or closed; rerun apply")
	}
	if p.Replaces != 0 && !strings.Contains(pull.GetBody(), areaMarker(c.Area)+replacementMarker(p.Replaces)) {
		return "", errors.New("existing migration does not match the requested replacement")
	}
	comment, err := findComment(ctx, client, c, p.Issue, c.Area)
	if err != nil {
		return "", err
	}
	reference := fmt.Sprintf("https://github.com/%s/pull/%d", c.Repository, p.Pull)
	body := migrationComment(p, reference)
	if comment == nil {
		_, _, err = client.Issues.CreateComment(ctx, c.Owner, c.Repo, p.Issue, github.IssueCommentRequest{Body: body})
	} else if comment.GetBody() != body {
		_, _, err = client.Issues.UpdateComment(ctx, c.Owner, c.Repo, comment.GetID(), github.IssueCommentRequest{Body: body})
	}
	return reference, err
}

func areaSection(body, slug string) string {
	lines := reportLines(body)
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			if start >= 0 {
				return strings.Join(lines[start:i], "\n")
			}
			if match := areaHeading.FindStringSubmatch(line); len(match) == 2 && match[1] == slug {
				start = i
			}
		}
	}
	if start >= 0 {
		return strings.Join(lines[start:], "\n")
	}
	return ""
}

func reportLines(body string) []string {
	lines := []string{}
	fence := ""
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		text := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(text, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
			fence = text[:len(text)-len(strings.TrimLeft(text, text[:1]))]
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func reportAreas(body string, previous []string) ([]string, error) {
	headings := make(map[string]bool)
	areas, covered := []string{}, make(map[string]bool)
	section := ""
	for _, line := range reportLines(body) {
		if strings.HasPrefix(line, "## ") {
			section = line
			headings[line] = true
		}
		if match := areaHeading.FindStringSubmatch(line); section == "## Areas" && len(match) == 2 {
			if covered[match[1]] {
				return nil, errors.New("duplicate area slug")
			}
			areas, covered[match[1]] = append(areas, match[1]), true
		}
		if match := resolvedArea.FindStringSubmatch(line); section == "## Resolved areas" && len(match) == 3 {
			if covered[match[1]] || !slices.Contains(previous, match[1]) || strings.TrimSpace(match[2]) == "" {
				return nil, errors.New("resolved area must name a previous unresolved slug once and explain its resolution")
			}
			covered[match[1]] = true
		}
	}
	for _, heading := range []string{"## Summary", "## Inventory", "## Areas", "## Notes"} {
		if !headings[heading] {
			return nil, fmt.Errorf("report is missing %s", heading)
		}
	}
	for _, slug := range previous {
		if !covered[slug] {
			return nil, fmt.Errorf("previous area %q must be carried forward or explicitly resolved", slug)
		}
	}
	return areas, nil
}

func stamp(ctx context.Context, client *github.Client, c config, p prepared, output string) error {
	if p.State.Repository != c.Repository || p.Publisher != c.Publisher || p.Area != c.Area {
		return errors.New("prepared context does not match the target, publishing identity, or selected area")
	}
	if !shaPattern.MatchString(p.State.BaseSHA) || !shaPattern.MatchString(p.State.HeadSHA) {
		return errors.New("prepared context contains invalid source revisions")
	}
	branch, head, err := currentHead(ctx, client, c)
	if err != nil {
		return err
	}
	if branch != p.State.Branch {
		return errors.New("default branch changed during analysis")
	}
	if head != p.State.HeadSHA {
		if c.Area != "" {
			return errors.New("default branch changed during apply; rerun the audit first")
		}
		comparison, _, err := client.Repositories.CompareCommits(ctx, c.Owner, c.Repo, p.State.HeadSHA, head, nil)
		if err != nil {
			return err
		}
		if comparison.GetStatus() != "ahead" {
			return errors.New("audited head is no longer on the default branch history")
		}
	}
	issue, state, err := latest(ctx, client, c)
	if err != nil {
		return err
	}
	if (state == nil && p.Issue != 0) || (state != nil && state.Branch != p.State.Branch) {
		return errors.New("tracker checkpoint disappeared or changed branch during analysis")
	}
	if state != nil && state.HeadSHA != p.State.BaseSHA && state.HeadSHA != p.State.HeadSHA {
		return errors.New("checkpoint advanced during analysis; retry from the current tracker")
	}
	var envelope map[string]json.RawMessage
	if err := readJSON(output, &envelope); err != nil {
		return err
	}
	var items []nativeRequest
	if err := json.Unmarshal(envelope["items"], &items); err != nil {
		return fmt.Errorf("decode native publication request: %w", err)
	}
	if len(items) != 1 {
		return errors.New("exactly one native publication request is required")
	}
	item := items[0]
	if c.Area != "" {
		if issue == nil || issue.GetNumber() != p.Issue || issue.GetState() != "open" || state.HeadSHA != p.State.HeadSHA ||
			!slices.Contains(state.Areas, c.Area) || p.Replaces != c.Replace {

			return errors.New("selected tracker changed during apply")
		}
		if p.Pull != 0 {
			return errors.New("existing migration PRs must be linked without running the fix agent")
		}
		existing, err := findPull(ctx, client, c, c.Area)
		if err != nil {
			return err
		}
		if existing != nil && (existing.GetState() == "open" || p.Replaces == 0) {
			return errors.New("a migration PR appeared during analysis; rerun apply to link it or explicitly replace a closed PR")
		}
		if p.Replaces != 0 {
			if err := checkReplacement(ctx, client, c, p.Replaces); err != nil {
				return err
			}
		}
		if item.Type != "create_pull_request" || item.Branch != p.WorkBranch {
			return errors.New("migration must create a fresh draft on the prepared branch")
		}
		body := item.Body
		if strings.TrimSpace(body) == "" || len(body) > 58000 || strings.Contains(body, "<!-- dependency-audit-") {
			return errors.New("new migration draft requires a bounded explanation without reserved automation markers")
		}
		header := areaMarker(c.Area)
		if p.Replaces != 0 {
			header += replacementMarker(p.Replaces) + fmt.Sprintf("Replaces #%d.\n\n", p.Replaces)
		}
		body = header + body + fmt.Sprintf("\n\nTracker: #%d. Source: `%s`. Nothing is automatically approved or merged.", p.Issue, p.State.HeadSHA)
		write := nativeRequest{
			Type: "create_pull_request", Branch: p.WorkBranch, Repo: c.Repository,
			Title: "Modernize " + c.Area, TemporaryID: "aw_dep_fix", Draft: true, Body: body,
		}
		comment := nativeRequest{
			Type: "add_comment", ItemNumber: p.Issue, CommentID: p.Comment,
			Body: migrationComment(p, "#aw_dep_fix"),
		}
		items = []nativeRequest{write, comment}
	} else {
		body := item.Body
		if item.Type != "create_issue" || strings.Contains(body, "<!-- dependency-audit:") {
			return errors.New("audit must submit one report without model-generated checkpoint metadata")
		}
		areas, err := reportAreas(body, p.State.Areas)
		if err != nil {
			return err
		}
		p.State.Areas = areas
		encoded, err := json.Marshal(p.State)
		if err != nil {
			return err
		}
		body = marker + string(encoded) + "\n-->\n\n" + body
		if len(body) > 60000 {
			return errors.New("report exceeds the 60000-byte limit")
		}
		items = []nativeRequest{{Type: "create_issue", Title: "Code modernization tracker " + p.State.HeadSHA[:12], Body: body}}
		if state != nil && state.HeadSHA == p.State.HeadSHA {
			items[0] = nativeRequest{Type: "update_issue", IssueNumber: issue.GetNumber(), Body: body}
			if state.Previous != 0 {
				old, _, err := client.Issues.Get(ctx, c.Owner, c.Repo, state.Previous)
				if err != nil {
					return err
				}
				checkpoint, err := readCheckpoint(old, c.Publisher)
				if err != nil {
					return err
				}
				if checkpoint == nil || checkpoint.Repository != c.Repository || checkpoint.Branch != state.Branch || checkpoint.HeadSHA != state.BaseSHA {
					return errors.New("previous issue is not the expected automation-owned checkpoint")
				}
				if old.GetState() == "open" {
					items = append(items, nativeRequest{Type: "close_issue", IssueNumber: old.GetNumber(), Body: fmt.Sprintf("Superseded by #%d.", issue.GetNumber())})
				}
			}
		}
	}
	for i := range items {
		items[i].Integrity, items[i].Secrecy = item.Integrity, item.Secrecy
	}
	envelope["items"], err = json.Marshal(items)
	if err != nil {
		return err
	}
	return writeJSON(output, envelope)
}

func safePath(name string) bool {
	return name != "" && name != "." && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\:\x00\r\n")
}

func outputs(name string, values map[string]string) error {
	if name == "" {
		return nil
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	for key, value := range values {
		if _, err := fmt.Fprintf(f, "%s=%s\n", key, value); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	return f.Close()
}

func readJSON(name string, value any) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("JSON exceeds the 2 MiB limit")
	}
	return json.Unmarshal(data, value)
}

func writeJSON(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	return os.WriteFile(name, append(data, '\n'), 0o600)
}
