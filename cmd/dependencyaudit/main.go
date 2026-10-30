// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// dependencyaudit prepares audit checkpoints and validates audit reports.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
)

const (
	marker            = "```dependency-audit-v1\n"
	actionsPublisher  = "github-actions[bot]"
	applyHeading      = "## Apply this audit"
	applyInstructions = `## Apply this audit

To apply a finding, assign this issue to Copilot or implement a selected open area yourself.

1. Choose an open area and check for an existing PR before making changes.
2. Start from the current default branch. Recheck the affected callers and cited upstream sources because dependencies or code may have changed since this audit.
3. Keep the change focused on that area. Preserve behavior unless the documented migration requires a change, and add regression tests.
4. Run the area's validation commands and the repository's documented formatting, lint, test, and build checks for each affected module. Report any checks you cannot run.
5. Open a draft PR that references this tracker and the area slug without closing keywords such as "Fixes" or "Closes". Do not automatically approve or merge the PR. Leave the area open until a later audit verifies the merged code.
`
)

var (
	repoPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	slugPattern  = regexp.MustCompile("^[a-z0-9]+(?:-[a-z0-9]+)*$")
	areaHeading  = regexp.MustCompile("^### `([a-z0-9]+(?:-[a-z0-9]+)*)`")
	resolvedArea = regexp.MustCompile("^- `([a-z0-9]+(?:-[a-z0-9]+)*)`: (.+)")
)

type checkpoint struct {
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	BaseSHA    string    `json:"base_sha"`
	HeadSHA    string    `json:"head_sha"`
	Previous   int       `json:"previous_issue"`
	Areas      []string  `json:"areas"`
	RunID      int64     `json:"run_id,omitempty"`
	AuditedAt  time.Time `json:"audited_at,omitzero"`
}

type prepared struct {
	State    checkpoint `json:"checkpoint"`
	Issue    int        `json:"issue"`
	Previous string     `json:"previous"`
}

type nativeRequest struct {
	Type        string          `json:"type"`
	Operation   string          `json:"operation,omitempty"`
	Title       string          `json:"title,omitempty"`
	Body        string          `json:"body,omitempty"`
	IssueNumber int             `json:"issue_number,omitempty"`
	Status      string          `json:"status,omitempty"`
	Message     string          `json:"message,omitempty"`
	Integrity   json.RawMessage `json:"integrity,omitempty"`
	Secrecy     json.RawMessage `json:"secrecy,omitempty"`
}

type config struct {
	Command, Repository, Owner, Repo, Initial string
	Input, Output, Token, GitHubOutput        string
	Summary                                   string
	RunID                                     int64
}

func main() {
	log.SetPrefix("dependencyaudit: ")
	log.SetFlags(0)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func parseConfig(args []string, getenv func(string) string, stderr io.Writer) (config, error) {
	var c config
	if len(args) == 0 || !slices.Contains([]string{"prepare", "stamp"}, args[0]) {
		return c, errors.New("expected prepare or stamp")
	}
	c.Command = args[0]
	fs := flag.NewFlagSet("dependencyaudit "+c.Command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflowRepo := strings.ToLower(getenv("GITHUB_REPOSITORY"))
	fs.StringVar(&c.Repository, "repo", workflowRepo, "workflow repository in owner/repo form")
	fs.StringVar(&c.Initial, "initial-sha", "", "exclusive starting commit for the first audit")
	fs.StringVar(&c.Input, "input", "", "prepared context JSON")
	fs.StringVar(&c.Output, "output", "", "prepared context or validated checkpoint JSON destination")
	if err := fs.Parse(args[1:]); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	c.Repository = strings.ToLower(c.Repository)
	if !repoPattern.MatchString(c.Repository) {
		return c, errors.New("repo must be in owner/repo form")
	}
	if workflowRepo != "" && c.Repository != workflowRepo {
		return c, errors.New("repo must match the workflow's GITHUB_REPOSITORY")
	}
	if c.Initial != "" && (c.Command != "prepare" || !shaPattern.MatchString(c.Initial)) {
		return c, errors.New("initial-sha requires prepare and a lowercase 40-character SHA")
	}
	c.Owner, c.Repo, _ = strings.Cut(c.Repository, "/")
	c.Token, c.GitHubOutput = getenv("GITHUB_TOKEN"), getenv("GITHUB_OUTPUT")
	c.Summary = getenv("GITHUB_STEP_SUMMARY")
	var err error
	c.RunID, err = strconv.ParseInt(getenv("GITHUB_RUN_ID"), 10, 64)
	if err != nil {
		return c, fmt.Errorf("GITHUB_RUN_ID must identify the current workflow run: %w", err)
	}
	if c.RunID < 1 {
		return c, errors.New("GITHUB_RUN_ID must be positive")
	}
	if c.Token == "" {
		c.Token = getenv("GH_TOKEN")
	}
	if c.Token == "" {
		return c, errors.New("GITHUB_TOKEN or GH_TOKEN is required")
	}
	if c.Output == "" || (c.Command == "stamp" && c.Input == "") {
		return c, errors.New("prepare requires output; stamp requires input and output")
	}
	return c, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	c, err := parseConfig(args, getenv, stderr)
	if err != nil {
		return err
	}
	client, err := githubutil.NewClient(ctx, c.Token)
	if err != nil {
		return err
	}
	if c.Command == "stamp" {
		var p prepared
		if err := readJSON(c.Input, &p); err != nil {
			return err
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
	return outputs(c.GitHubOutput, map[string]string{
		"base_sha": p.State.BaseSHA, "head_sha": p.State.HeadSHA,
	})
}

func readCheckpoint(issue *github.Issue) (*checkpoint, error) {
	if issue.IsPullRequest() || issue.GetUser().GetLogin() != actionsPublisher || issue.GetUser().GetType() != "Bot" {
		return nil, nil
	}
	body := strings.ReplaceAll(issue.GetBody(), "\r\n", "\n")
	start := strings.Index(body, "```dependency-audit-")
	if start < 0 {
		return nil, nil
	}
	if !strings.HasPrefix(body[start:], marker) || strings.Count(body, marker) != 1 {
		return nil, errors.New("unsupported or duplicate tracker checkpoint metadata")
	}
	encoded, _, ok := strings.Cut(strings.TrimPrefix(body[start:], marker), "\n```")
	var state checkpoint
	if !ok || json.Unmarshal([]byte(encoded), &state) != nil || state.Previous == issue.GetNumber() {
		return nil, fmt.Errorf("tracker #%d has corrupt checkpoint metadata", issue.GetNumber())
	}
	if err := validateCheckpoint(state); err != nil {
		return nil, fmt.Errorf("tracker #%d has corrupt checkpoint metadata: %w", issue.GetNumber(), err)
	}
	return &state, nil
}

func validateCheckpoint(state checkpoint) error {
	if !repoPattern.MatchString(state.Repository) || state.Branch == "" ||
		!shaPattern.MatchString(state.BaseSHA) || !shaPattern.MatchString(state.HeadSHA) ||
		state.Areas == nil || state.Previous < 0 || state.RunID < 0 || (state.RunID != 0 && state.AuditedAt.IsZero()) {

		return errors.New("invalid checkpoint fields")
	}
	seen := make(map[string]bool)
	for _, slug := range state.Areas {
		if !slugPattern.MatchString(slug) || seen[slug] {
			return errors.New("checkpoint contains invalid or duplicate area slugs")
		}
		seen[slug] = true
	}
	return nil
}

func latestIssue(ctx context.Context, client *github.Client, c config) (*github.Issue, *checkpoint, error) {
	opts := &github.IssueListByRepoOptions{State: "all", Creator: actionsPublisher, Sort: "created", Direction: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		issues, response, err := client.Issues.ListByRepo(ctx, c.Owner, c.Repo, opts)
		if err != nil {
			return nil, nil, err
		}
		for _, issue := range issues {
			state, err := readCheckpoint(issue)
			if err != nil {
				return nil, nil, err
			}
			if state != nil {
				if state.Repository != c.Repository {
					return nil, nil, errors.New("checkpoint repository does not match the workflow repository")
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
	if repo.GetDefaultBranch() == "" {
		return "", "", errors.New("repository has no default branch")
	}
	branch, _, err := client.Repositories.GetBranch(ctx, c.Owner, c.Repo, repo.GetDefaultBranch(), 0)
	if err != nil {
		return "", "", err
	}
	if !shaPattern.MatchString(branch.GetCommit().GetSHA()) {
		return "", "", errors.New("repository has no valid default branch head")
	}
	return repo.GetDefaultBranch(), branch.GetCommit().GetSHA(), nil
}

func prepare(ctx context.Context, client *github.Client, c config) (prepared, error) {
	var p prepared
	branch, head, err := currentHead(ctx, client, c)
	if err != nil {
		return p, err
	}
	p.State = checkpoint{Repository: c.Repository, Branch: branch, BaseSHA: c.Initial, HeadSHA: head, Areas: []string{}}
	issue, previous, report, err := latest(ctx, client, c)
	if err != nil {
		return p, err
	}
	if previous == nil {
		if c.Initial == "" {
			return p, errors.New("no checkpoint exists; run the first audit with an explicit initial-sha")
		}
		return p, nil
	}
	if previous.Branch != branch || c.Initial != "" {
		return p, errors.New("default branch changed or initial-sha would reset an existing checkpoint")
	}
	p.Issue, p.Previous = issue.GetNumber(), report
	p.State.BaseSHA, p.State.Previous, p.State.Areas = previous.HeadSHA, p.Issue, previous.Areas
	if previous.HeadSHA == head && len(previous.Areas) != 0 {
		p.State = *previous
	}
	return p, nil
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

// A nil previous slice checks a saved report without repeating its carry-forward checks.
func reportAreas(body string, previous []string) ([]string, error) {
	headings := make(map[string]bool)
	areas, covered := []string{}, make(map[string]bool)
	section := ""
	for _, line := range reportLines(body) {
		if trimmed := strings.TrimLeft(line, " "); len(line)-len(trimmed) < 4 {
			line = trimmed
		}
		if strings.HasPrefix(line, "## ") {
			section = line
			headings[line] = true
		}
		if match := areaHeading.FindStringSubmatch(line); section == "## Areas" && strings.HasPrefix(line, "###") {
			if len(match) != 2 {
				return nil, errors.New("area headings must start with a backtick-quoted stable slug")
			}
			if covered[match[1]] {
				return nil, errors.New("duplicate area slug")
			}
			areas, covered[match[1]] = append(areas, match[1]), true
		}
		if match := resolvedArea.FindStringSubmatch(line); section == "## Resolved areas" && len(match) == 3 {
			if covered[match[1]] || (previous != nil && !slices.Contains(previous, match[1])) || strings.TrimSpace(match[2]) == "" {
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
	if headings[applyHeading] {
		return nil, errors.New("the trusted report step supplies the application instructions")
	}
	for _, slug := range previous {
		if !covered[slug] {
			return nil, fmt.Errorf("previous area %q must be carried forward or explicitly resolved", slug)
		}
	}
	return areas, nil
}

func stamp(ctx context.Context, client *github.Client, c config, p prepared, output string) error {
	if p.State.Repository != c.Repository {
		return errors.New("prepared context does not match the workflow repository")
	}
	if err := validateCheckpoint(p.State); err != nil {
		return fmt.Errorf("invalid prepared checkpoint: %w", err)
	}
	branch, head, err := currentHead(ctx, client, c)
	if err != nil {
		return err
	}
	if branch != p.State.Branch {
		return errors.New("default branch changed during analysis")
	}
	if head != p.State.HeadSHA {
		comparison, _, err := client.Repositories.CompareCommits(ctx, c.Owner, c.Repo, p.State.HeadSHA, head, nil)
		if err != nil {
			return err
		}
		if comparison.GetStatus() != "ahead" {
			return errors.New("audited head is no longer on the default branch history")
		}
	}
	issue, state, _, err := latest(ctx, client, c)
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
	if len(items) != 1 || items[0].Type != "create_issue" || strings.Contains(items[0].Body, "```dependency-audit-") {
		return errors.New("audit must submit one report without model-generated checkpoint metadata")
	}
	item := items[0]
	areas, err := reportAreas(item.Body, p.State.Areas)
	if err != nil {
		return err
	}
	p.State.Areas = areas
	p.State.RunID = c.RunID
	p.State.AuditedAt = time.Now().UTC()
	if len(item.Body) > 60000 {
		return errors.New("report exceeds the 60000-byte limit")
	}
	var published *checkpoint
	if issue != nil {
		published, err = readCheckpoint(issue)
		if err != nil {
			return err
		}
	}
	updating := published != nil && published.HeadSHA == p.State.HeadSHA
	if len(areas) == 0 {
		if c.Summary == "" {
			return errors.New("GITHUB_STEP_SUMMARY is required for a clean audit")
		}
		summary := fmt.Sprintf("## Dependency audit\n\nNo actionable or unresolved findings in `%s..%s`.\n\n%s\n",
			p.State.BaseSHA, p.State.HeadSHA, strings.TrimSpace(item.Body))
		if err := appendSummary(c.Summary, summary); err != nil {
			return err
		}
		items = []nativeRequest{{Type: "noop", Message: "No actionable or unresolved findings. See the workflow summary."}}
		if issue != nil && issue.GetState() == "open" {
			items = append(items, nativeRequest{
				Type: "close_issue", IssueNumber: issue.GetNumber(),
				Body: fmt.Sprintf("No unresolved dependency audit findings remain at `%s`. See the audit workflow summary.", p.State.HeadSHA),
			})
		}
	} else {
		if updating {
			p.State.Previous = published.Previous
		}
		encoded, err := json.Marshal(p.State)
		if err != nil {
			return err
		}
		body := marker + string(encoded) + "\n```\n\n" + strings.TrimSpace(item.Body) + "\n\n" + applyInstructions
		if len(body) > 60000 {
			return errors.New("report exceeds the 60000-byte limit")
		}
		items = []nativeRequest{{Type: "create_issue", Title: "Code modernization tracker " + p.State.HeadSHA[:12], Body: body}}
		if updating {
			items[0] = nativeRequest{Type: "update_issue", Operation: "replace", IssueNumber: issue.GetNumber(), Body: body}
			if issue.GetState() == "closed" && len(state.Areas) == 0 {
				items[0].Status = "open"
			}
		}
	}
	if published != nil && published.Previous != 0 && (len(areas) == 0 || updating) {
		old, _, err := client.Issues.Get(ctx, c.Owner, c.Repo, published.Previous)
		if err != nil {
			return err
		}
		checkpoint, err := readCheckpoint(old)
		if err != nil {
			return err
		}
		if checkpoint == nil || checkpoint.Repository != c.Repository || checkpoint.Branch != p.State.Branch {
			return errors.New("previous issue is not the expected automation-owned checkpoint")
		}
		if checkpoint.HeadSHA != p.State.BaseSHA {
			comparison, _, err := client.Repositories.CompareCommits(ctx, c.Owner, c.Repo, checkpoint.HeadSHA, p.State.BaseSHA, nil)
			if err != nil {
				return err
			}
			if comparison.GetStatus() != "ahead" {
				return errors.New("previous issue is not on the audited checkpoint history")
			}
		}
		if old.GetState() == "open" {
			items = append(items, nativeRequest{Type: "close_issue", IssueNumber: old.GetNumber(), Body: fmt.Sprintf("Superseded by #%d.", issue.GetNumber())})
		}
	}
	for i := range items {
		items[i].Integrity, items[i].Secrecy = item.Integrity, item.Secrecy
	}
	envelope["items"], err = json.Marshal(items)
	if err != nil {
		return err
	}
	if err := writeJSON(output, envelope); err != nil {
		return err
	}
	return writeJSON(c.Output, runCheckpoint{Version: 1, State: p.State, Report: strings.TrimSpace(item.Body)})
}

func appendSummary(name, body string) error {
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, body); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
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
