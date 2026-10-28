// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
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
	return config{Repository: "o/r", Owner: "o", Repo: "r"}
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
		Number: new(number), State: new("open"), User: &github.User{Login: new(actionsPublisher), Type: new("Bot")},
		Body: new("Native header\n\n" + marker + string(data) + "\n```\n\n" + report(state.Areas...)),
	}
}

func report(areas ...string) string {
	body := "## Summary\nAssessment\n## Inventory\nVersions\n## Areas\n"
	for _, slug := range areas {
		body += "### `" + slug + "` / dependency / deprecated\n- Module: `go.mod`\n- Why: removed API\n" +
			"- Files: `client.go`, `newClient`\n- Upstream: https://example.com/v2/migration\n" +
			"- Suggested change: replace the removed API in newClient.\n- Validation: `go test ./...`\n"
	}
	return body + "## Notes\nExplicit limitations\n"
}

type fixture struct {
	Issues     []*github.Issue
	Head       string
	Branch     string
	Private    bool
	Comparison string
}

func testAPI(t *testing.T, f *fixture) *github.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("audit helper must not publish directly: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var value any
		switch {
		case r.URL.Path == "/repos/o/r":
			branch := f.Branch
			if branch == "" {
				branch = "main"
			}
			value = &github.Repository{DefaultBranch: new(branch), Private: new(f.Private)}
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/branches/"):
			head := f.Head
			if head == "" {
				head = headSHA
			}
			value = &github.Branch{Commit: &github.RepositoryCommit{SHA: new(head)}}
		case r.URL.Path == "/repos/o/r/issues":
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("creator") != actionsPublisher {
				t.Error("checkpoint discovery must include closed issues and require the Actions bot")
			}
			value = f.Issues
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/"):
			value = &github.CommitsComparison{Status: new(f.Comparison)}
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/"):
			number, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues/"))
			if err != nil {
				t.Error(err)
			}
			for _, issue := range f.Issues {
				if issue.GetNumber() == number {
					value = issue
				}
			}
		}
		if value == nil && r.URL.Path != "/repos/o/r/issues" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
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

func TestConfig(t *testing.T) {
	env := map[string]string{"GITHUB_REPOSITORY": "O/R", "GITHUB_TOKEN": "workflow-token", "GH_TOKEN": "local-token"}
	getenv := func(name string) string { return env[name] }
	args := []string{"prepare", "-output", "context.json"}
	c, err := parseConfig(args, getenv, io.Discard)
	if err != nil || c.Repository != "o/r" || c.Token != "workflow-token" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	delete(env, "GITHUB_TOKEN")
	c, err = parseConfig(args, getenv, io.Discard)
	if err != nil || c.Token != "local-token" {
		t.Fatalf("local token fallback = %q, %v", c.Token, err)
	}
	for _, args := range [][]string{
		{},
		{"targets"},
		{"link"},
		{"prepare"},
		{"prepare", "-repo", "other/repo", "-output", "context.json"},
		{"prepare", "-initial-sha", "short", "-output", "context.json"},
		{"prepare", "-initial-sha", strings.Repeat("A", 40), "-output", "context.json"},
		{"stamp"},
		{"stamp", "-input", "context.json", "-initial-sha", baseSHA},
		{"prepare", "-output", "context.json", "extra"},
	} {
		if _, err := parseConfig(args, getenv, io.Discard); err == nil {
			t.Fatalf("invalid configuration accepted: %v", args)
		}
	}
}

func TestBootstrap(t *testing.T) {
	c := testConfig()
	client := testAPI(t, &fixture{Private: true})
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("first run requires an explicit starting commit")
	}
	c.Initial = baseSHA
	p, err := prepare(t.Context(), client, c)
	if err != nil || p.State.BaseSHA != baseSHA || p.State.HeadSHA != headSHA || p.State.Areas == nil {
		t.Fatalf("prepared = %+v, %v", p, err)
	}
}

func TestCheckpointOwnershipAndCorruption(t *testing.T) {
	issue := tracker(t, 7, testState())
	if state, err := readCheckpoint(issue); state == nil || err != nil {
		t.Fatalf("Actions checkpoint = %+v, %v", state, err)
	}
	issue.User.Login = new("someone")
	if state, err := readCheckpoint(issue); state != nil || err != nil {
		t.Fatal("spoofed metadata was accepted")
	}
	issue.User.Login, issue.User.Type = new(actionsPublisher), new("User")
	if state, err := readCheckpoint(issue); state != nil || err != nil {
		t.Fatal("a non-bot author was accepted")
	}
	issue.User.Type = new("Bot")
	for _, body := range []string{marker + "{}\n```", strings.Replace(issue.GetBody(), marker, "```dependency-audit-v2\n", 1), issue.GetBody() + marker} {
		issue.Body = new(body)
		if _, err := readCheckpoint(issue); err == nil {
			t.Fatal("corrupt state must fail instead of resetting progress")
		}
	}
}

func TestPreparationResumesClosedCheckpoint(t *testing.T) {
	c := testConfig()
	state := testState()
	state.BaseSHA, state.HeadSHA, state.Areas = strings.Repeat("c", 40), baseSHA, []string{"dep-api"}
	issue := tracker(t, 7, state)
	issue.State = new("closed")
	f := &fixture{Issues: []*github.Issue{issue}}
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil || p.State.BaseSHA != baseSHA || p.State.Previous != 7 || !strings.Contains(p.Previous, "dep-api") {
		t.Fatalf("resumed = %+v, %v", p, err)
	}
	c.Initial = baseSHA
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("initial-sha reset an existing checkpoint")
	}
	c.Initial, f.Branch = "", "release"
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("a changed default branch was accepted")
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
	old := testState()
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
	body := items[0]["body"].(string)
	if strings.Count(body, applyHeading) != 1 || !strings.Contains(body, applyInstructions) {
		t.Fatal("published issue is missing trusted Copilot and maintainer instructions")
	}
	created := tracker(t, 8, p.State)
	created.Body = new(body)
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
	if len(items) != 2 || items[0]["type"] != "update_issue" || items[0]["operation"] != "replace" || items[0]["issue_number"] != float64(8) ||
		items[1]["type"] != "close_issue" || items[1]["issue_number"] != float64(7) {

		t.Fatal("retry must replace the report and finish closing its predecessor")
	}
	if strings.Count(items[0]["body"].(string), applyHeading) != 1 {
		t.Fatal("retry duplicated application instructions")
	}
}

func TestAuditRejectsRewrittenHistory(t *testing.T) {
	for _, status := range []string{"diverged", "behind"} {
		t.Run(status, func(t *testing.T) {
			c := testConfig()
			client := testAPI(t, &fixture{Head: strings.Repeat("c", 40), Comparison: status})
			name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
			if err := stamp(t.Context(), client, c, prepared{State: testState()}, name); err == nil {
				t.Fatal("rewritten history advanced the checkpoint")
			}
		})
	}
}

func TestAuditAllowsForwardMovement(t *testing.T) {
	c := testConfig()
	client := testAPI(t, &fixture{Head: strings.Repeat("c", 40), Comparison: "ahead"})
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), client, c, prepared{State: testState()}, name); err != nil {
		t.Fatal(err)
	}
	issue := tracker(t, 7, testState())
	issue.Body = new(outputItems(t, name)[0]["body"].(string))
	state, err := readCheckpoint(issue)
	if err != nil || state.HeadSHA != headSHA || state.Areas == nil {
		t.Fatalf("captured checkpoint = %+v, %v", state, err)
	}
}

func TestAuditRejectsChangedCheckpoint(t *testing.T) {
	for _, change := range []string{"disappeared", "advanced", "branch"} {
		t.Run(change, func(t *testing.T) {
			state := testState()
			state.HeadSHA = baseSHA
			f := &fixture{Issues: []*github.Issue{tracker(t, 7, state)}}
			c := testConfig()
			client := testAPI(t, f)
			p, err := prepare(t.Context(), client, c)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "disappeared":
				f.Issues = nil
			case "advanced":
				state.HeadSHA = strings.Repeat("c", 40)
				f.Issues = []*github.Issue{tracker(t, 8, state)}
			case "branch":
				f.Branch = "release"
			}
			name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
			if err := stamp(t.Context(), client, c, p, name); err == nil {
				t.Fatal("changed checkpoint was accepted")
			}
		})
	}
}

func TestNativeRequestRejectsModelMetadataAndOtherOperations(t *testing.T) {
	c := testConfig()
	client := testAPI(t, &fixture{})
	for _, item := range []map[string]any{
		{"type": "update_issue", "body": report()},
		{"type": "create_issue", "body": report() + marker + "{}\n```"},
		{"type": "create_issue", "body": report() + applyInstructions},
		{"type": "create_issue", "body": report() + strings.Repeat("x", 60000)},
	} {
		name := nativeOutput(t, item)
		if err := stamp(t.Context(), client, c, prepared{State: testState()}, name); err == nil {
			t.Fatalf("invalid native request accepted: %v", item["type"])
		}
	}
}

func TestReportParsingIgnoresExamplesAndNormalizesLineEndings(t *testing.T) {
	body := report("real-area") + "\n```markdown\n## Areas\n### `example-area`\n```\n"
	body = strings.ReplaceAll(body, "\n", "\r\n")
	areas, err := reportAreas(body, []string{"real-area"})
	if err != nil || len(areas) != 1 || areas[0] != "real-area" {
		t.Fatalf("areas = %v, %v", areas, err)
	}
	for _, body := range []string{
		"```markdown\n" + report() + "```\n",
		report() + "## Resolved areas\n- `old-area`:   \n",
		fmt.Sprintf("%s## Resolved areas\n- `unknown-area`: explanation.\n", report()),
	} {
		if _, err := reportAreas(body, []string{"old-area"}); err == nil {
			t.Fatal("invalid report was accepted")
		}
	}
	issue := tracker(t, 7, testState())
	issue.Body = new(strings.ReplaceAll(issue.GetBody(), "\n", "\r\n"))
	if _, err := readCheckpoint(issue); err != nil {
		t.Fatal(err)
	}
}
