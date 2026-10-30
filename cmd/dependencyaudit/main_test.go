// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
)

var (
	baseSHA = strings.Repeat("a", 40)
	headSHA = strings.Repeat("b", 40)
)

func testConfig(t *testing.T) config {
	t.Helper()
	return config{
		Repository: "o/r", Owner: "o", Repo: "r", RunID: 100,
		Output: filepath.Join(t.TempDir(), "checkpoint.json"), Summary: filepath.Join(t.TempDir(), "summary.md"),
	}
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
	Issues           []*github.Issue
	Head             string
	Branch           string
	Private          bool
	Comparison       string
	Runs             []*github.WorkflowRun
	Artifacts        map[int64][]*github.Artifact
	Archives         map[int64][]byte
	Failures         map[string]int
	DownloadLocation string
}

func testAPI(t *testing.T, f *fixture) *github.Client {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("audit helper must not publish directly: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if code := f.Failures[r.URL.Path]; code != 0 {
			http.Error(w, http.StatusText(code), code)
			return
		}
		var value any
		switch {
		case r.URL.Path == "/repos/o/r/actions/runs/100":
			value = &github.WorkflowRun{ID: new(int64(100)), WorkflowID: new(int64(11))}
		case r.URL.Path == "/repos/o/r/actions/artifacts":
			if r.URL.Query().Get("name") != checkpointArtifact {
				t.Error("checkpoint discovery must filter artifacts by name")
			}
			var artifacts []*github.Artifact
			for _, runArtifacts := range f.Artifacts {
				for _, artifact := range runArtifacts {
					if artifact.GetName() == checkpointArtifact {
						artifacts = append(artifacts, artifact)
					}
				}
			}
			slices.SortFunc(artifacts, func(a, b *github.Artifact) int {
				return b.GetCreatedAt().Compare(a.GetCreatedAt().Time)
			})
			page := 1
			if r.URL.Query().Get("page") != "" {
				var err error
				page, err = strconv.Atoi(r.URL.Query().Get("page"))
				if err != nil {
					t.Error(err)
				}
			}
			start, end := min((page-1)*100, len(artifacts)), min(page*100, len(artifacts))
			if end < len(artifacts) {
				w.Header().Set("Link", fmt.Sprintf("<%s%s?name=%s&page=%d>; rel=\"next\"", server.URL, r.URL.Path, checkpointArtifact, page+1))
			}
			value = &github.ArtifactList{Artifacts: artifacts[start:end]}
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/actions/runs/"):
			runID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/o/r/actions/runs/"), 10, 64)
			if err != nil {
				t.Error(err)
			}
			for _, run := range f.Runs {
				if run.GetID() == runID {
					value = run
				}
			}
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/actions/artifacts/") && strings.HasSuffix(r.URL.Path, "/zip"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/o/r/actions/artifacts/"), "/zip")
			location := f.DownloadLocation
			if location == "" {
				location = server.URL + "/download/" + id
			}
			http.Redirect(w, r, location, http.StatusFound)
			return
		case strings.HasPrefix(r.URL.Path, "/download/"):
			if r.Header.Get("Authorization") != "" {
				t.Error("signed artifact downloads must not receive the GitHub token")
			}
			id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/download/"), 10, 64)
			if err != nil {
				t.Error(err)
			}
			if _, err := w.Write(f.Archives[id]); err != nil {
				t.Error(err)
			}
			return
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
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithAuthToken("test-token"), github.WithURLs(new(server.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func checkpointArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func savedRun(t *testing.T, f *fixture, runID int64, state checkpoint, report string) {
	t.Helper()
	state.RunID = runID
	if state.AuditedAt.IsZero() {
		state.AuditedAt = time.Unix(runID, 0).UTC()
	}
	if f.Artifacts == nil {
		f.Artifacts = make(map[int64][]*github.Artifact)
		f.Archives = make(map[int64][]byte)
	}
	f.Runs = append(f.Runs, &github.WorkflowRun{
		ID: new(runID), WorkflowID: new(int64(11)), Status: new("completed"), Conclusion: new("success"),
	})
	id := runID + 1000
	f.Artifacts[runID] = []*github.Artifact{{
		ID: new(id), Name: new(checkpointArtifact), CreatedAt: &github.Timestamp{Time: state.AuditedAt},
		WorkflowRun: &github.ArtifactWorkflowRun{ID: new(runID)},
	}}
	data, err := json.Marshal(runCheckpoint{Version: 1, State: state, Report: report})
	if err != nil {
		t.Fatal(err)
	}
	f.Archives[id] = checkpointArchive(t, map[string]string{"checkpoint.json": string(data)})
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
	env := map[string]string{
		"GITHUB_REPOSITORY": "O/R", "GITHUB_TOKEN": "workflow-token", "GH_TOKEN": "local-token", "GITHUB_RUN_ID": "100",
	}
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
		{"stamp", "-input", "context.json"},
		{"prepare", "-output", "context.json", "extra"},
	} {
		if _, err := parseConfig(args, getenv, io.Discard); err == nil {
			t.Fatalf("invalid configuration accepted: %v", args)
		}
	}
	for _, invalid := range []string{"", "0", "-1", "not-a-run-id"} {
		env["GITHUB_RUN_ID"] = invalid
		if _, err := parseConfig(args, getenv, io.Discard); err == nil {
			t.Fatalf("invalid GITHUB_RUN_ID accepted: %q", invalid)
		}
	}
}

func TestBootstrap(t *testing.T) {
	c := testConfig(t)
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
	c := testConfig(t)
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
	c := testConfig(t)
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
			c := testConfig(t)
			client := testAPI(t, &fixture{Head: strings.Repeat("c", 40), Comparison: status})
			name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
			if err := stamp(t.Context(), client, c, prepared{State: testState()}, name); err == nil {
				t.Fatal("rewritten history advanced the checkpoint")
			}
		})
	}
}

func TestAuditAllowsForwardMovement(t *testing.T) {
	c := testConfig(t)
	client := testAPI(t, &fixture{Head: strings.Repeat("c", 40), Comparison: "ahead"})
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report("dep-api")})
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
			c := testConfig(t)
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
	c := testConfig(t)
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

func TestCleanAuditUsesSummaryAndAdvancesCheckpointWithoutAnIssue(t *testing.T) {
	c := testConfig(t)
	c.Initial = baseSHA
	f := &fixture{}
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if len(items) != 1 || items[0]["type"] != "noop" {
		t.Fatalf("clean audit attempted to publish an issue: %v", items)
	}
	summary, err := os.ReadFile(c.Summary)
	if err != nil || !strings.Contains(string(summary), report()) || !strings.Contains(string(summary), headSHA) ||
		strings.Contains(string(summary), applyHeading) {

		t.Fatalf("clean audit summary = %q, %v", summary, err)
	}
	var saved runCheckpoint
	if err := readJSON(c.Output, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.State.Areas == nil || len(saved.State.Areas) != 0 || saved.State.HeadSHA != headSHA {
		t.Fatalf("clean checkpoint = %+v", saved)
	}
	savedRun(t, f, 90, saved.State, saved.Report)
	f.Runs[0].HeadSHA = new(strings.Repeat("e", 40))
	c.Initial = ""
	unchanged, err := prepare(t.Context(), client, c)
	if err != nil || unchanged.State.BaseSHA != headSHA || unchanged.State.HeadSHA != headSHA {
		t.Fatalf("unchanged clean audit repeated already-reviewed history: %+v, %v", unchanged, err)
	}
	name = nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), client, c, unchanged, name); err != nil {
		t.Fatal(err)
	}
	if items := outputItems(t, name); len(items) != 1 || items[0]["type"] != "noop" {
		t.Fatalf("repeated clean audit attempted to create an issue: %v", items)
	}
	f.Head = strings.Repeat("c", 40)
	resumed, err := prepare(t.Context(), client, c)
	if err != nil || resumed.State.BaseSHA != headSHA || resumed.Issue != 0 || resumed.Previous != saved.Report {
		t.Fatalf("resumed clean checkpoint = %+v, %v", resumed, err)
	}
	c.Initial = baseSHA
	if _, err := prepare(t.Context(), client, c); err == nil {
		t.Fatal("an explicit starting SHA reset a saved checkpoint")
	}
}

func TestCleanAuditResolvesAndClosesThePreviousTracker(t *testing.T) {
	c := testConfig(t)
	old := testState()
	old.HeadSHA, old.Areas = baseSHA, []string{"dep-api"}
	issue := tracker(t, 7, old)
	f := &fixture{Issues: []*github.Issue{issue}, Comparison: "behind"}
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	body := report() + "## Resolved areas\n- `dep-api`: the merged code uses the replacement API.\n"
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": body})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if len(items) != 2 || items[0]["type"] != "noop" || items[1]["type"] != "close_issue" ||
		items[1]["issue_number"] != float64(7) {

		t.Fatalf("resolved audit publication = %v", items)
	}
	var saved runCheckpoint
	if err := readJSON(c.Output, &saved); err != nil {
		t.Fatal(err)
	}
	issue.State = new("closed")
	savedRun(t, f, 90, saved.State, saved.Report)
	f.Head = strings.Repeat("c", 40)
	resumed, err := prepare(t.Context(), client, c)
	if err != nil || len(resumed.State.Areas) != 0 || resumed.State.BaseSHA != headSHA {
		t.Fatalf("resolved checkpoint = %+v, %v", resumed, err)
	}
}

func TestCleanAuditStillRequiresResolutionOfSavedFindings(t *testing.T) {
	c := testConfig(t)
	state := testState()
	state.HeadSHA, state.Areas = baseSHA, []string{"dep-api"}
	f := &fixture{Issues: []*github.Issue{tracker(t, 7, state)}}
	savedRun(t, f, 90, state, report("dep-api"))
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{report(), strings.Replace(report(), "## Areas\n", "## Areas\n### not-a-stable-area-heading\n", 1)} {
		name := nativeOutput(t, map[string]any{"type": "create_issue", "body": body})
		if err := stamp(t.Context(), client, c, p, name); err == nil {
			t.Fatal("unresolved or malformed findings were silently classified as a clean audit")
		}
	}
	if _, err := os.Stat(c.Output); !os.IsNotExist(err) {
		t.Fatal("invalid reports saved a checkpoint")
	}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report("dep-api")})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	if items := outputItems(t, name); len(items) != 1 || items[0]["type"] != "create_issue" {
		t.Fatalf("carried-forward findings were suppressed: %v", items)
	}
}

func TestCheckpointRecoveryIgnoresFailedCurrentAndOtherWorkflowRuns(t *testing.T) {
	for _, skip := range []string{"failed", "current", "other-workflow"} {
		t.Run(skip, func(t *testing.T) {
			c := testConfig(t)
			f := &fixture{Head: strings.Repeat("c", 40)}
			invalid := testState()
			invalid.HeadSHA = strings.Repeat("d", 40)
			savedRun(t, f, 95, invalid, report())
			switch skip {
			case "failed":
				f.Runs[0].Conclusion = new("failure")
			case "current":
				f.Artifacts[95][0].WorkflowRun.ID = new(int64(100))
			case "other-workflow":
				f.Runs[0].WorkflowID = new(int64(12))
			}
			savedRun(t, f, 90, testState(), report())
			p, err := prepare(t.Context(), testAPI(t, f), c)
			if err != nil || p.State.BaseSHA != headSHA {
				t.Fatalf("wrong prior run selected: %+v, %v", p, err)
			}
		})
	}
}

func TestCheckpointArtifactPagination(t *testing.T) {
	c := testConfig(t)
	f := &fixture{Head: strings.Repeat("c", 40)}
	savedRun(t, f, 90, testState(), report())
	for i := range int64(100) {
		runID := 200 + i
		savedRun(t, f, runID, testState(), report())
		f.Runs[len(f.Runs)-1].WorkflowID = new(int64(12))
	}
	p, err := prepare(t.Context(), testAPI(t, f), c)
	if err != nil || p.State.BaseSHA != headSHA {
		t.Fatalf("checkpoint on the second artifact page = %+v, %v", p, err)
	}
}

func TestUnavailableCheckpointRequiresAnExplicitBaseline(t *testing.T) {
	c := testConfig(t)
	f := &fixture{}
	savedRun(t, f, 90, testState(), report())
	f.Artifacts[90][0].Expired = new(true)
	savedRun(t, f, 80, testState(), report())
	client := testAPI(t, f)
	if _, err := prepare(t.Context(), client, c); err == nil || !strings.Contains(err.Error(), "initial-sha") {
		t.Fatalf("expired checkpoint must fail explicitly: %v", err)
	}
	c.Initial = baseSHA
	p, err := prepare(t.Context(), client, c)
	if err != nil || p.State.BaseSHA != baseSHA {
		t.Fatalf("explicit recovery = %+v, %v", p, err)
	}
	c.Initial = ""
	f.Issues = []*github.Issue{tracker(t, 7, testState())}
	if _, err := prepare(t.Context(), client, c); err != nil {
		t.Fatalf("an existing trusted issue checkpoint should remain usable: %v", err)
	}
}

func TestCheckpointErrorsAreNotTreatedAsFirstRuns(t *testing.T) {
	for _, failure := range []string{
		"/repos/o/r/actions/runs/100",
		"/repos/o/r/actions/artifacts",
		"/repos/o/r/actions/runs/90",
		"/download/1090",
	} {
		t.Run(failure, func(t *testing.T) {
			c := testConfig(t)
			c.Initial = baseSHA
			f := &fixture{Failures: map[string]int{failure: http.StatusForbidden}}
			savedRun(t, f, 90, testState(), report())
			if _, err := prepare(t.Context(), testAPI(t, f), c); err == nil {
				t.Fatal("checkpoint access error was silently treated as a first run")
			}
		})
	}
}

func TestCheckpointArtifactValidation(t *testing.T) {
	c := testConfig(t)
	for _, invalid := range []string{"archive", "repository", "branch", "areas", "duplicate", "run-identity"} {
		t.Run(invalid, func(t *testing.T) {
			state := testState()
			switch invalid {
			case "repository":
				state.Repository = "another/repo"
			case "branch":
				state.Branch = "release"
			case "areas":
				state.Areas = nil
			}
			f := &fixture{}
			savedRun(t, f, 90, state, report())
			if invalid == "archive" {
				f.Archives[1090] = []byte("not a ZIP archive")
			}
			if invalid == "duplicate" {
				f.Artifacts[90] = append(f.Artifacts[90], f.Artifacts[90][0])
			}
			if invalid == "run-identity" {
				state.RunID = 80
				state.AuditedAt = time.Unix(80, 0).UTC()
				data, err := json.Marshal(runCheckpoint{Version: 1, State: state, Report: report()})
				if err != nil {
					t.Fatal(err)
				}
				f.Archives[1090] = checkpointArchive(t, map[string]string{"checkpoint.json": string(data)})
			}
			if _, err := prepare(t.Context(), testAPI(t, f), c); err == nil {
				t.Fatal("invalid saved checkpoint was accepted")
			}
		})
	}
	for _, files := range []map[string]string{
		{"other.json": "{}"},
		{"checkpoint.json": "{}", "extra.json": "{}"},
		{"checkpoint.json": strings.Repeat("x", (2<<20)+1)},
		{"checkpoint.json": `{"version":2,"report":"unknown"}`},
	} {
		if _, err := decodeCheckpoint(checkpointArchive(t, files)); err == nil {
			t.Fatal("invalid checkpoint archive was accepted")
		}
	}
}

func TestActionableRetryAfterCleanAuditsClosesTheOlderTracker(t *testing.T) {
	c := testConfig(t)
	old := testState()
	old.HeadSHA = baseSHA
	f := &fixture{Head: strings.Repeat("c", 40), Issues: []*github.Issue{tracker(t, 7, old)}, Comparison: "behind"}
	savedRun(t, f, 90, testState(), report())
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report("new-api")})
	if err := stamp(t.Context(), client, c, p, name); err != nil {
		t.Fatal(err)
	}
	items := outputItems(t, name)
	if items[0]["type"] != "create_issue" {
		t.Fatal("new actionable finding did not create an issue")
	}
	created := tracker(t, 8, p.State)
	created.Body = new(items[0]["body"].(string))
	f.Issues = append([]*github.Issue{created}, f.Issues...)
	f.Comparison = "ahead"
	retry, err := prepare(t.Context(), client, c)
	if err != nil {
		t.Fatal(err)
	}
	name = nativeOutput(t, map[string]any{"type": "create_issue", "body": report("new-api")})
	if err := stamp(t.Context(), client, c, retry, name); err != nil {
		t.Fatal(err)
	}
	items = outputItems(t, name)
	if len(items) != 2 || items[0]["type"] != "update_issue" || items[1]["type"] != "close_issue" ||
		items[1]["issue_number"] != float64(7) {

		t.Fatalf("retry after intervening clean audits = %v", items)
	}
}

func TestCleanSummaryFailureDoesNotSaveProgress(t *testing.T) {
	c := testConfig(t)
	c.Summary = t.TempDir()
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), testAPI(t, &fixture{}), c, prepared{State: testState()}, name); err == nil {
		t.Fatal("summary write failure was ignored")
	}
	if _, err := os.Stat(c.Output); !os.IsNotExist(err) {
		t.Fatal("failed audit saved a checkpoint")
	}
}

func TestSameHeadFindingsAfterCleanCheckpointUseTheCorrectTracker(t *testing.T) {
	for _, previous := range []string{"no-issue", "older-issue", "same-head-issue"} {
		t.Run(previous, func(t *testing.T) {
			c := testConfig(t)
			f := &fixture{Comparison: "behind"}
			savedRun(t, f, 90, testState(), report())
			switch previous {
			case "older-issue":
				old := testState()
				old.HeadSHA = baseSHA
				f.Issues = []*github.Issue{tracker(t, 7, old)}
			case "same-head-issue":
				f.Issues = []*github.Issue{tracker(t, 7, testState())}
				f.Issues[0].State = new("closed")
			}

			client := testAPI(t, f)
			p, err := prepare(t.Context(), client, c)
			if err != nil {
				t.Fatal(err)
			}
			name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report("new-api")})
			if err := stamp(t.Context(), client, c, p, name); err != nil {
				t.Fatal(err)
			}
			items := outputItems(t, name)
			if previous != "same-head-issue" {
				if len(items) != 1 || items[0]["type"] != "create_issue" {
					t.Fatalf("clean checkpoint updated a missing or older tracker: %v", items)
				}
				return
			}
			if len(items) != 1 || items[0]["type"] != "update_issue" || items[0]["status"] != "open" {
				t.Fatalf("same-head actionable tracker was not reopened: %v", items)
			}
			updated := tracker(t, 7, testState())
			updated.Body = new(items[0]["body"].(string))
			state, err := readCheckpoint(updated)
			if err != nil || state.Previous == 7 {
				t.Fatalf("updated tracker has a self-referencing checkpoint: %+v, %v", state, err)
			}
		})
	}
}

func TestSameHeadPublishedFindingsSurviveAnUnfinishedRun(t *testing.T) {
	c := testConfig(t)
	f := &fixture{}
	savedRun(t, f, 90, testState(), report())
	published := testState()
	published.RunID, published.Areas = 95, []string{"new-api"}
	published.AuditedAt = time.Unix(95, 0).UTC()
	f.Issues = []*github.Issue{tracker(t, 7, published)}
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil || len(p.State.Areas) != 1 || p.State.Areas[0] != "new-api" {
		t.Fatalf("newer published findings were hidden by an older clean checkpoint: %+v, %v", p, err)
	}
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": report()})
	if err := stamp(t.Context(), client, c, p, name); err == nil {
		t.Fatal("newer unresolved findings disappeared without resolution")
	}
}

func TestRerunningAnOlderWorkflowResumesTheNewerSuccessfulCheckpoint(t *testing.T) {
	c := testConfig(t)
	f := &fixture{Head: strings.Repeat("c", 40)}
	savedRun(t, f, 101, testState(), report())
	p, err := prepare(t.Context(), testAPI(t, f), c)
	if err != nil || p.State.BaseSHA != headSHA {
		t.Fatalf("older workflow rerun ignored newer successful progress: %+v, %v", p, err)
	}
}

func TestIndentedFindingsCannotBecomeACleanAudit(t *testing.T) {
	body := strings.ReplaceAll(report("dep-api"), "### `", "   ### `")
	areas, err := reportAreas(body, []string{})
	if err != nil || len(areas) != 1 || areas[0] != "dep-api" {
		t.Fatalf("indented Markdown finding was suppressed: %v, %v", areas, err)
	}
	for _, heading := range []string{"#### `dep-api`", "   ### missing-slug"} {
		body := strings.Replace(report("dep-api"), "### `dep-api`", heading, 1)
		if _, err := reportAreas(body, []string{}); err == nil {
			t.Fatalf("malformed area heading was silently accepted: %q", heading)
		}
	}
}

func TestSavedReportMustMatchItsAreaCheckpoint(t *testing.T) {
	state := testState()
	state.RunID = 90
	state.AuditedAt = time.Unix(90, 0).UTC()
	data, err := json.Marshal(runCheckpoint{Version: 1, State: state, Report: report("dep-api")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCheckpoint(checkpointArchive(t, map[string]string{"checkpoint.json": string(data)})); err == nil ||
		!strings.Contains(err.Error(), "does not match") {

		t.Fatalf("saved findings disagreed with the clean checkpoint: %v", err)
	}
}

func TestCheckpointDownloadErrorsDoNotExposeSignedURLs(t *testing.T) {
	c := testConfig(t)
	f := &fixture{DownloadLocation: "unsupported://artifact.test/checkpoint?signature=do-not-log"}
	savedRun(t, f, 90, testState(), report())
	_, err := prepare(t.Context(), testAPI(t, f), c)
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol") || strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("download error lost its cause or exposed the signed URL: %v", err)
	}
}

func TestRerunCheckpointsAreOrderedByAuditTimeNotRunID(t *testing.T) {
	c := testConfig(t)
	f := &fixture{}
	old := testState()
	old.RunID, old.AuditedAt, old.Areas = 95, time.Unix(95, 0).UTC(), []string{"dep-api"}
	savedRun(t, f, 95, old, report("dep-api"))
	resolved := testState()
	resolved.AuditedAt = time.Unix(200, 0).UTC()
	savedRun(t, f, 90, resolved, report())
	client := testAPI(t, f)
	p, err := prepare(t.Context(), client, c)
	if err != nil || len(p.State.Areas) != 0 || p.State.BaseSHA != headSHA {
		t.Fatalf("later rerun artifact was not selected: %+v, %v", p, err)
	}
	f.Issues = []*github.Issue{tracker(t, 7, old)}
	p, err = prepare(t.Context(), client, c)
	if err != nil || len(p.State.Areas) != 0 {
		t.Fatalf("higher run ID resurrected findings resolved by a later rerun: %+v, %v", p, err)
	}
}

func TestInvalidPreparedCheckpointCannotDisableCarryForwardValidation(t *testing.T) {
	c := testConfig(t)
	state := testState()
	state.Areas = nil
	body := report() + "## Resolved areas\n- `unknown-area`: explanation.\n"
	name := nativeOutput(t, map[string]any{"type": "create_issue", "body": body})
	if err := stamp(t.Context(), testAPI(t, &fixture{}), c, prepared{State: state}, name); err == nil {
		t.Fatal("invalid prepared context disabled carry-forward validation")
	}
}
