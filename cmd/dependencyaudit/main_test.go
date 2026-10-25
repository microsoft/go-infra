// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
)

var (
	baseSHA = strings.Repeat("a", 40)
	headSHA = strings.Repeat("b", 40)
)

func testConfig() config {
	return config{Repository: "o/r", Owner: "o", Repo: "r", Publisher: "audit[bot]"}
}

func testState() checkpoint {
	return checkpoint{Repository: "o/r", Branch: "main", BaseSHA: baseSHA, HeadSHA: headSHA, Areas: []string{}}
}

func tracker(t *testing.T, number int, state checkpoint) *github.Issue {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return &github.Issue{
		Number: new(number), State: new("open"), User: &github.User{Login: new("audit[bot]"), Type: new("Bot")},
		Body: new("Native header\n\n" + marker + string(data) + "\n-->\n\n" + report(state.Areas...)),
	}
}

func report(areas ...string) string {
	body := "## Summary\nAssessment\n## Inventory\nVersions\n## Areas\n"
	for _, slug := range areas {
		body += "### `" + slug + "` / dependency / deprecated\n- Module: `go.mod`\n- Why: removed API\n"
	}
	return body + "## Notes\nExplicit limitations\n"
}

type fixture struct {
	Issues     []*github.Issue
	Pull       *github.PullRequest
	Pulls      []*github.PullRequest
	Comments   []*github.IssueComment
	Head       string
	Private    bool
	Comparison string
	AllowLinks bool
	Created    int
	Updated    int
}

func testAPI(t *testing.T, f *fixture) *github.Client {
	t.Helper()
	if f.Head == "" {
		f.Head = headSHA
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			creating := r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/7/comments"
			updating := r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/issues/comments/12"
			if !f.AllowLinks || (!creating && !updating) {
				t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
				http.Error(w, "write rejected", http.StatusBadRequest)
				return
			}
			var request github.IssueCommentRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "invalid comment", http.StatusBadRequest)
				return
			}
			comment := &github.IssueComment{
				ID: new(int64(12)), Body: new(request.Body),
				User: &github.User{Login: new("audit[bot]"), Type: new("Bot")},
			}
			if creating {
				f.Created++
				f.Comments = append(f.Comments, comment)
			} else {
				f.Updated++
				f.Comments = []*github.IssueComment{comment}
			}
			if err := json.NewEncoder(w).Encode(comment); err != nil {
				t.Error(err)
			}
			return
		}
		var value any
		switch {
		case r.URL.Path == "/repos/o/r":
			value = map[string]any{"default_branch": "main", "private": f.Private}
		case r.URL.Path == "/repos/o/r/branches/main":
			value = map[string]any{"commit": map[string]string{"sha": f.Head}}
		case strings.Contains(r.URL.Path, "/compare/"):
			value = map[string]string{"status": f.Comparison}
		case strings.HasSuffix(r.URL.Path, "/comments"):
			value = f.Comments
		case r.URL.Path == "/repos/o/r/pulls":
			value = []*github.PullRequest{}
			if f.Pulls != nil {
				value = f.Pulls
			} else if f.Pull != nil {
				value = []*github.PullRequest{f.Pull}
			}
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/pulls/"):
			pulls := f.Pulls
			if f.Pull != nil {
				pulls = append(pulls, f.Pull)
			}
			for _, pull := range pulls {
				if r.URL.Path == "/repos/o/r/pulls/"+strconv.Itoa(pull.GetNumber()) {
					value = pull
				}
			}
			if value == nil {
				http.Error(w, "unknown PR", http.StatusNotFound)
				return
			}
		case r.URL.Path == "/repos/o/r/issues":
			value = f.Issues
		default:
			for _, issue := range f.Issues {
				if r.URL.Path == "/repos/o/r/issues/"+strconv.Itoa(issue.GetNumber()) {
					value = issue
				}
			}
			if value == nil {
				t.Errorf("unexpected request %s", r.URL.Path)
			}
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func nativeOutput(t *testing.T, item map[string]any) string {
	t.Helper()
	item["integrity"], item["secrecy"] = "unapproved", "public"
	name := filepath.Join(t.TempDir(), "output.json")
	if err := writeJSON(name, map[string]any{"items": []any{item}, "retain": "metadata"}); err != nil {
		t.Fatal(err)
	}
	return name
}

func outputItems(t *testing.T, name string) []map[string]any {
	t.Helper()
	var result struct {
		Items  []map[string]any `json:"items"`
		Retain string           `json:"retain"`
	}
	if err := readJSON(name, &result); err != nil {
		t.Fatal(err)
	}
	if result.Retain != "metadata" {
		t.Fatal("native output envelope metadata was lost")
	}
	for _, item := range result.Items {
		if item["integrity"] != "unapproved" || item["secrecy"] != "public" {
			t.Fatal("native output classifications were lost")
		}
	}
	return result.Items
}

func TestEnrollmentAndBootstrap(t *testing.T) {
	getenv := func(key string) string {
		if key == "DEPENDENCY_AUDIT_REPOSITORIES" {
			return `["o/r"]`
		}
		return ""
	}
	for _, args := range [][]string{
		{"targets", "-repo", "other/repo"},
		{"targets", "-initial-sha", headSHA},
		{"targets", "-repo", "o/r", "-area", "dep-api"},
		{"targets", "-repo", "o/r", "-replace-pr", "9"},
	} {
		if _, err := parseConfig(args, getenv, io.Discard); err == nil {
			t.Fatalf("invalid configuration accepted: %v", args)
		}
	}
	c := testConfig()
	client := testAPI(t, &fixture{})
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("first run requires an explicit starting commit")
	}
	c.Initial = baseSHA
	p, err := prepare(t.Context(), client, c)
	if err != nil || p.State.BaseSHA != baseSHA || p.State.HeadSHA != headSHA {
		t.Fatalf("prepared = %+v, %v", p, err)
	}
	private := testAPI(t, &fixture{Private: true})
	if _, err := prepare(t.Context(), private, c); err == nil {
		t.Fatal("private source must not enter public central artifacts")
	}
}

func TestCheckpointOwnershipAndCorruption(t *testing.T) {
	issue := tracker(t, 7, testState())
	if _, err := readCheckpoint(issue, "another[bot]"); err != nil {
		t.Fatal(err)
	}
	issue.User.Login = new("someone")
	if state, err := readCheckpoint(issue, "audit[bot]"); state != nil || err != nil {
		t.Fatal("spoofed metadata was accepted")
	}
	issue.User.Login = new("audit[bot]")
	issue.Body = new(marker + "{}\n-->")
	if _, err := readCheckpoint(issue, "audit[bot]"); err == nil {
		t.Fatal("corrupt state must fail instead of resetting progress")
	}
}

func TestCarryForwardAndExplicitResolution(t *testing.T) {
	if _, err := reportAreas(report(), []string{"old-area"}); err == nil {
		t.Fatal("unresolved findings disappeared")
	}
	body := report("new-area") + "## Resolved areas\n- `old-area`: call no longer exists at the captured revision.\n"
	areas, err := reportAreas(body, []string{"old-area"})
	if err != nil || len(areas) != 1 || areas[0] != "new-area" {
		t.Fatalf("areas = %v, %v", areas, err)
	}
	if _, err := reportAreas(report("old-area", "old-area"), []string{"old-area"}); err == nil {
		t.Fatal("duplicate slugs accepted")
	}
}

func TestNativeIssueCreationAndRetryRecovery(t *testing.T) {
	c := testConfig()
	state := testState()
	old := state
	old.HeadSHA, old.BaseSHA = baseSHA, strings.Repeat("c", 40)
	f := &fixture{Issues: []*github.Issue{tracker(t, 7, old)}}
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "title": "ignored", "body": report("dep-api")})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if len(items) != 1 || items[0]["type"] != "create_issue" || !strings.Contains(items[0]["title"].(string), headSHA[:12]) {
		t.Fatal("native report identity was not set deterministically")
	}
	created := tracker(t, 8, p.State)
	created.Body = new(items[0]["body"].(string))
	f.Issues = append([]*github.Issue{created}, f.Issues...)
	retry, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	name = nativeOutput(t, map[string]any{"type": "create_issue", "body": report("dep-api")})
	if err := stamp(t.Context(), client, c, retry, name); err != nil {
		t.Fatal(err)
	}
	items = outputItems(t, name)
	if len(items) != 2 || items[0]["type"] != "update_issue" || items[0]["issue_number"] != float64(8) ||
		items[1]["type"] != "close_issue" || items[1]["issue_number"] != float64(7) {

		t.Fatal("retry must update the existing report and finish closing its predecessor")
	}
}

func TestAuditRejectsRewrittenHistory(t *testing.T) {
	c := testConfig()
	client := testAPI(t, &fixture{Head: strings.Repeat("c", 40), Comparison: "diverged"})
	p := prepared{Publisher: c.Publisher, State: testState()}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), client, c, p, name); err == nil {
		t.Fatal("rewritten history advanced the checkpoint")
	}
}

func applyFixture(t *testing.T) (config, *fixture, *github.Client) {
	t.Helper()
	c := testConfig()
	c.Area = "dep-api"
	state := testState()
	state.Areas = []string{c.Area}
	f := &fixture{Issues: []*github.Issue{tracker(t, 7, state)}}
	return c, f, testAPI(t, f)
}

func migrationPull(number int, state, body string) *github.PullRequest {
	return &github.PullRequest{
		Number: new(number), State: new(state), Draft: new(true), Body: new(body),
		User: &github.User{Login: new("audit[bot]"), Type: new("Bot")},
		Base: &github.PullRequestBranch{Ref: new("main")},
		Head: &github.PullRequestBranch{SHA: new(baseSHA), Ref: new("old-fix-branch"), Repo: &github.Repository{FullName: new("o/r")}},
	}
}

func TestFreshDraftUsesAuditedHeadAndRejectsBranchUpdates(t *testing.T) {
	c, _, client := applyFixture(t)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.State.HeadSHA != headSHA || p.Pull != 0 || p.Module != "." {
		t.Fatalf("fresh context = %+v", p)
	}
	name := nativeOutput(t, map[string]any{"type": "push_to_pull_request_branch", "branch": p.WorkBranch})
	if err := stamp(t.Context(), client, c, p, name); err == nil {
		t.Fatal("automatic branch updates must not be accepted")
	}
	name = nativeOutput(t, map[string]any{"type": "create_pull_request", "branch": p.WorkBranch, "body": "Required migration."})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if len(items) != 2 || items[0]["draft"] != true || items[1]["item_number"] != float64(7) || !strings.Contains(items[0]["body"].(string), headSHA) {
		t.Fatal("new migration must be a draft linked to the current tracker")
	}
}

func TestExistingOpenPRRefreshesOnlyItsLink(t *testing.T) {
	c, f, client := applyFixture(t)
	f.Head = strings.Repeat("c", 40)
	f.Pull = migrationPull(9, "open", areaMarker(c.Area)+"Maintainer's description stays unchanged.")
	f.Pull.Draft = new(false)
	f.AllowLinks = true
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Pull != 9 || p.Module != "" || p.State.HeadSHA != headSHA {
		t.Fatalf("link-only context = %+v", p)
	}
	before := f.Pull.GetBody()
	for range 2 {
		link, err := linkExisting(t.Context(), client, c, p)
		if err != nil || link != "https://github.com/o/r/pull/9" {
			t.Fatalf("link = %q, %v", link, err)
		}
	}
	if f.Created != 1 || f.Updated != 0 || f.Pull.GetBody() != before {
		t.Fatal("link retries duplicated a comment or changed the existing PR")
	}
	f.Comments = []*github.IssueComment{{ID: new(int64(12)), User: f.Issues[0].User, Body: new(areaMarker(c.Area) + "previous link")}}
	if _, err := linkExisting(t.Context(), client, c, p); err != nil {
		t.Fatal(err)
	}
	if f.Updated != 1 || f.Created != 1 {
		t.Fatal("existing managed comment was not refreshed in place")
	}
}

func TestClosedPRRequiresExplicitReplacement(t *testing.T) {
	c, f, client := applyFixture(t)
	f.Pull = migrationPull(9, "closed", areaMarker(c.Area)+"Old attempt.")
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("a closed migration was implicitly replaced")
	}
	c.Replace = 9
	p, err := prepare(t.Context(), client, c)
	if err != nil || p.Pull != 0 || p.Replaces != 9 || p.State.HeadSHA != headSHA {
		t.Fatalf("replacement context = %+v, %v", p, err)
	}
	name := nativeOutput(t, map[string]any{"type": "create_pull_request", "branch": p.WorkBranch, "body": "Fresh migration."})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if !strings.Contains(items[0]["body"].(string), "Replaces #9") || !strings.Contains(items[1]["body"].(string), "Replaces #9") {
		t.Fatal("replacement draft and tracker comment must link the previous attempt")
	}
	f.Pulls = []*github.PullRequest{migrationPull(10, "open", items[0]["body"].(string)), f.Pull}
	f.Pull = nil
	f.AllowLinks = true
	retry, err := prepare(t.Context(), client, c)
	if err != nil || retry.Pull != 10 {
		t.Fatalf("replacement retry = %+v, %v", retry, err)
	}
	if _, err := linkExisting(t.Context(), client, c, retry); err != nil {
		t.Fatal(err)
	}
	if f.Created != 1 || !strings.Contains(f.Comments[0].GetBody(), "/pull/10") {
		t.Fatal("partial-publication retry did not reuse the replacement PR")
	}
}

func TestInvalidReplacementAndStaleCreationFail(t *testing.T) {
	for _, status := range []string{"open", "merged", "stale", "wrong-area"} {
		t.Run(status, func(t *testing.T) {
			c, f, client := applyFixture(t)
			c.Replace = 9
			f.Pull = migrationPull(9, "closed", areaMarker(c.Area)+"Previous attempt.")
			switch status {
			case "open":
				f.Pull.State = new("open")
			case "merged":
				f.Pull.Merged = new(true)
			case "stale":
				f.Head = strings.Repeat("c", 40)
			case "wrong-area":
				f.Pull.Body = new(areaMarker("another-area"))
			}
			if _, err := prepare(t.Context(), client, c); err == nil {
				t.Fatal("invalid replacement was accepted")
			}
		})
	}
}

func TestPublicationStopsWhenAnotherPRAppears(t *testing.T) {
	c, f, client := applyFixture(t)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	f.Pull = migrationPull(9, "open", areaMarker(c.Area)+"Another attempt already published.")
	name := nativeOutput(t, map[string]any{"type": "create_pull_request", "branch": p.WorkBranch, "body": "Migration."})
	if err := stamp(t.Context(), client, c, p, name); err == nil {
		t.Fatal("a racing publication created a duplicate PR")
	}
	retry, err := prepare(t.Context(), client, c)
	if err != nil || retry.Pull != 9 {
		t.Fatalf("retry did not find the existing PR: %+v, %v", retry, err)
	}
}

func TestExistingLinkRejectsChangedTrackerAndClosedPR(t *testing.T) {
	for _, change := range []string{"tracker", "closed"} {
		t.Run(change, func(t *testing.T) {
			c, f, client := applyFixture(t)
			f.Pull = migrationPull(9, "open", areaMarker(c.Area)+"Existing draft.")
			f.AllowLinks = true
			p, err := prepare(t.Context(), client, c)
			if err != nil {
				t.Fatal(err)
			}
			if change == "tracker" {
				f.Issues[0].State = new("closed")
			} else {
				f.Pull.State = new("closed")
			}
			if _, err := linkExisting(t.Context(), client, c, p); err == nil || f.Created != 0 || f.Updated != 0 {
				t.Fatal("stale link context wrote a tracker comment")
			}
		})
	}
}

func TestReportParsingIgnoresExamplesAndNormalizesLineEndings(t *testing.T) {
	body := report("real-area") + "\n```markdown\n## Areas\n### `example-area`\n```\n"
	body = strings.ReplaceAll(body, "\n", "\r\n")
	areas, err := reportAreas(body, []string{"real-area"})
	if err != nil || len(areas) != 1 || areas[0] != "real-area" {
		t.Fatalf("areas = %v, %v", areas, err)
	}
	section := report("real-area")
	section = strings.Replace(section, "- Module: `go.mod`", "```markdown\n- Module: `wrong/go.mod`\n```\n- Module: `go.mod`", 1)
	module := moduleLine.FindStringSubmatch(areaSection(section, "real-area"))
	if len(module) != 2 || module[1] != "go.mod" {
		t.Fatalf("code example was mistaken for the owning module: %v", module)
	}
	if _, err := reportAreas("```markdown\n"+report()+"```\n", nil); err == nil {
		t.Fatal("a code example must not be accepted as a completed report")
	}
	if _, err := reportAreas(report()+"## Resolved areas\n- `old-area`:   \n", []string{"old-area"}); err == nil {
		t.Fatal("an area resolution must have an explanation")
	}
	issue := tracker(t, 7, testState())
	issue.Body = new(strings.ReplaceAll(issue.GetBody(), "\n", "\r\n"))
	if _, err := readCheckpoint(issue, "audit[bot]"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAcceptsStandardGitHubTokenFallback(t *testing.T) {
	env := map[string]string{
		"DEPENDENCY_AUDIT_REPOSITORIES": `["o/r"]`,
		"GITHUB_TOKEN":                  "fallback-token",
	}
	args := []string{"prepare", "-repo", "o/r", "-publisher-login", "audit[bot]", "-output", "context.json"}
	cfg, err := parseConfig(args, func(name string) string { return env[name] }, io.Discard)
	if err != nil || cfg.Token != "fallback-token" {
		t.Fatalf("token fallback = %q, %v", cfg.Token, err)
	}
	env["GH_TOKEN"] = "scoped-token"
	cfg, err = parseConfig(args, func(name string) string { return env[name] }, io.Discard)
	if err != nil || cfg.Token != "scoped-token" {
		t.Fatalf("explicit scoped token = %q, %v", cfg.Token, err)
	}
}

func TestMultipleOpenPRsAreNotChosenArbitrarily(t *testing.T) {
	c, f, client := applyFixture(t)
	f.Pulls = []*github.PullRequest{
		migrationPull(10, "open", areaMarker(c.Area)),
		migrationPull(9, "open", areaMarker(c.Area)),
	}
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("ambiguous open PRs were silently selected")
	}
}
