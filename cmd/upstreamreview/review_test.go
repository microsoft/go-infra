// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/internal/importdiff"
)

func testPolicy() policy {
	return policy{
		upstream: "upstream/runtime", submodule: "deps/runtime [sdk]", label: "imports reviewed",
		rule: importdiff.Rule{
			SourceRoots: []string{"lib", "tools"}, ImportRoots: []string{"example.org/a", "example.org/b"},
			ExcludeDirs: []string{"lib/private", "tools/dev's files"},
		},
	}
}

func testClient(t *testing.T, handler http.HandlerFunc) *github.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	client, err := github.NewClient(github.WithHTTPClient(s.Client()), github.WithURLs(new(s.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newTestRepo(t *testing.T) (string, func(...string) string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		out, err := gitOutput(t.Context(), append([]string{"-C", dir}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	git("init", "--quiet")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	return dir, git
}

func TestFetchUpstream(t *testing.T) {
	dir, git := newTestRepo(t)
	p := testPolicy()
	commit := func(src string) string {
		t.Helper()
		for _, path := range []string{"tools/use.go", "lib/private/use.go", "tools/dev's files/use.go"} {
			path = filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", ".")
		git("commit", "-qm", "snapshot")
		return git("rev-parse", "HEAD")
	}
	old := commit("package p\nimport _ \"example.org/b/sub\"\n")
	updated := commit("package p\n")
	compare := compareUpstream(dir, p.rule)
	files, err := compare(t.Context(), old, updated)
	if err != nil || len(files) != 1 || files[0].Path != "tools/use.go" {
		t.Fatalf("fetch and compare: %v, %v", files, err)
	}
	if _, err := compare(t.Context(), old, strings.Repeat("1", 40)); err == nil {
		t.Error("expected an error fetching an unknown upstream revision")
	}
}

func TestGitlinkReportsStderr(t *testing.T) {
	dir, _ := newTestRepo(t)
	t.Chdir(dir)
	_, err := gitlink(t.Context(), "HEAD", testPolicy().submodule)
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || len(exit.Stderr) == 0 || !strings.Contains(err.Error(), strings.TrimSpace(string(exit.Stderr))) {
		t.Fatalf("expected Git's diagnostic in the error, got %v", err)
	}
}

func TestInvalidRepository(t *testing.T) {
	for _, repo := range []string{"", "go", "/go", "owner/", "owner/go/extra", "https://example.org/o/r", "owner/.."} {
		p := testPolicy()
		if _, err := newGate(nil, repo, p); err == nil {
			t.Errorf("accepted invalid -repo %q", repo)
		}
		p.upstream = repo
		if _, err := newGate(nil, "valid/repo", p); err == nil {
			t.Errorf("accepted invalid -upstream %q", repo)
		}
	}
}

func TestInvalidPolicy(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*policy)
	}{
		{"missing label", func(p *policy) { p.label = "" }},
		{"missing submodule", func(p *policy) { p.submodule = "" }},
		{"submodule outside checkout", func(p *policy) { p.submodule = "../other" }},
		{"missing imports", func(p *policy) { p.rule.ImportRoots = nil }},
		{"missing source roots", func(p *policy) { p.rule.SourceRoots = nil }},
	} {
		p := testPolicy()
		tt.set(&p)
		if _, err := newGate(nil, "valid/repo", p); err == nil {
			t.Errorf("accepted policy with %s", tt.name)
		}
	}
}

func TestCurrentLabelApplication(t *testing.T) {
	const label = "reviewed"
	event := func(id int64, action, name string) *github.IssueEvent {
		return &github.IssueEvent{ID: &id, Event: &action, Label: &github.Label{Name: name}, CreatedAt: &github.Timestamp{Time: time.Unix(1, 0).UTC()}}
	}
	last := []*github.IssueEvent{event(12, "labeled", "other"), event(13, "labeled", label)}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		events := last
		if r.URL.Query().Get("page") != "2" {
			events = []*github.IssueEvent{event(10, "labeled", label), event(11, "unlabeled", label)}
			w.Header().Set("Link", `<?page=2>; rel="next"`)
		}
		if err := json.NewEncoder(w).Encode(events); err != nil {
			t.Error(err)
		}
	})
	got, err := currentApplication(t.Context(), client, "org", "repo", 7, label)
	if err != nil || got.id != 13 || !got.ambiguous {
		t.Fatalf("same-second reapplication = %+v, %v; want ambiguous event 13", got, err)
	}
	last = append(last, event(14, "unlabeled", label))
	if got, err := currentApplication(t.Context(), client, "org", "repo", 7, label); err != nil || got.id != 0 {
		t.Fatalf("revoked application = %+v, %v", got, err)
	}
}

func TestPRCheck(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")
	p := testPolicy()
	origin, git := newTestRepo(t)
	dir, checkout := newTestRepo(t)
	pinTree := func(pin string) string {
		git("update-index", "--add", "--cacheinfo", "160000", pin, p.submodule)
		return git("write-tree")
	}
	checkoutMerge := func(tree, base, head string) string {
		merge := git("commit-tree", tree, "-p", base, "-p", head, "-m", "test merge")
		checkout("fetch", "--quiet", "--depth=2", "--no-tags", "--no-recurse-submodules", "--", origin, merge)
		checkout("checkout", "--quiet", "--detach", "FETCH_HEAD")
		return merge
	}
	old, updated := strings.Repeat("d", 40), strings.Repeat("e", 40)
	oldTree := pinTree(old)
	root := git("commit-tree", oldTree, "-m", "root")
	base := git("commit-tree", oldTree, "-p", root, "-m", "base")
	head := git("commit-tree", oldTree, "-p", root, "-m", "head")
	merge := checkoutMerge(pinTree(updated), base, head)
	if checkout("rev-parse", "--is-shallow-repository") != "true" {
		t.Fatal("expected a shallow checkout")
	}
	workdir := filepath.Join(dir, "eng", "_util")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workdir)

	var labels []*github.Label
	var event *github.PullRequestEvent
	labelEvent := int64(21)
	appliedAt := github.Timestamp{Time: time.Unix(100, 0).UTC()}
	labelWebhook := func() *github.PullRequestEvent {
		return &github.PullRequestEvent{
			Repo: &github.Repository{FullName: new("fork/go")}, Number: new(7),
			Action: new("labeled"), Label: &github.Label{Name: p.label}, Sender: &github.User{ID: new(int64(8))},
			PullRequest: &github.PullRequest{Base: &github.PullRequestBranch{SHA: new(base)}, Head: &github.PullRequestBranch{SHA: new(head)}, UpdatedAt: new(appliedAt)},
		}
	}
	var comment string
	commentAuthor := "github-actions[bot]"
	var historyStatus int
	commentCount := new(0)
	files := []importdiff.Change{{Path: "lib/example.go"}}
	var scanErr error
	var duringScan func()
	var scans int
	var prReads atomic.Int32
	var commentReads int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/fork/go/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		prReads.Add(1)
		pr := github.PullRequest{
			Number: new(7), State: new("open"), Labels: labels, Comments: commentCount, ReviewComments: new(3),
			Head: &github.PullRequestBranch{SHA: &head},
			Base: &github.PullRequestBranch{Ref: new("main"), SHA: &base},
		}
		if err := json.NewEncoder(w).Encode(pr); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /repos/fork/go/issues/7/events", func(w http.ResponseWriter, r *http.Request) {
		if historyStatus != 0 {
			http.Error(w, `{"message":"label history unavailable"}`, historyStatus)
			return
		}
		if err := json.NewEncoder(w).Encode([]*github.IssueEvent{{
			Event: new("labeled"), Label: &github.Label{Name: p.label}, ID: &labelEvent, Actor: &github.User{ID: new(int64(8))}, CreatedAt: &appliedAt,
		}}); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /repos/fork/go/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		commentReads++
		comments := []*github.IssueComment{{Body: &comment, User: &github.User{Login: &commentAuthor}}}
		if err := json.NewEncoder(w).Encode(comments); err != nil {
			t.Error(err)
		}
	})
	g, err := newGate(testClient(t, mux.ServeHTTP), "fork/go", p)
	if err != nil {
		t.Fatal(err)
	}
	g.compare = func(_ context.Context, a, b string) ([]importdiff.Change, error) {
		if a != old || b != updated {
			t.Fatalf("compared %q to %q; want base to proposed merge gitlinks", a, b)
		}
		scans++
		if duringScan != nil {
			duringScan()
		}
		return files, scanErr
	}
	t.Run("optional comment", func(t *testing.T) {
		t.Setenv("GITHUB_RUN_ID", "100")
		t.Setenv("GITHUB_RUN_ATTEMPT", "1")
		var posts int
		status := http.StatusCreated
		mux.HandleFunc("POST /repos/fork/go/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
			posts++
			w.WriteHeader(status)
			fmt.Fprint(w, `{}`)
		})
		mux.HandleFunc("DELETE /repos/fork/go/issues/7/labels/{name}", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"cannot remove label"}`, http.StatusForbidden)
		})
		found := files
		t.Cleanup(func() { files, labels, scanErr = found, nil, nil })
		check := func(enable bool, wantErr error, wantReads, wantPosts int, wantWarning string) {
			t.Helper()
			readsBefore, postsBefore := commentReads, posts
			var summary strings.Builder
			err := g.runCheck(t.Context(), &summary, 7, merge, labelWebhook(), enable)
			if !errors.Is(err, wantErr) || commentReads-readsBefore != wantReads || posts-postsBefore != wantPosts {
				t.Fatalf("comment=%t: got (%v, %d reads, %d posts); want (%v, %d, %d)", enable, err, commentReads-readsBefore, posts-postsBefore, wantErr, wantReads, wantPosts)
			}
			_, warning, ok := strings.Cut(summary.String(), "\n> [!WARNING]\n> ")
			if ok != (wantWarning != "") || !strings.Contains(warning, wantWarning) {
				t.Errorf("summary warning = %q; want %q", warning, wantWarning)
			}
		}
		check(false, errAckRequired, 0, 0, "") // Read-only, even for findings.
		check(true, errAckRequired, 1, 1, "")  // Post before returning the failed verdict.
		status = http.StatusForbidden
		check(true, errAckRequired, 1, 1, "See the step log")
		labels = []*github.Label{{Name: p.label}}
		check(true, nil, 1, 1, "remove and reapply the label")
		files = nil
		check(true, nil, 1, 0, "If the stale review label is still present, remove it")
		labels = nil
		check(true, nil, 0, 0, "") // Clean PR with no comment to update.
		scanErr = errors.New("comparison failed")
		check(true, scanErr, 0, 0, "")
	})
	run := func(wantErr error, wantVerdict verdict, wantSummary ...string) scanResult {
		t.Helper()
		before := prReads.Load()
		report, err := g.check(t.Context(), 7, merge, event)
		if !errors.Is(err, wantErr) {
			t.Fatalf("check = %v; want %v", err, wantErr)
		}
		var want scanResult
		if wantVerdict != "" {
			want = scanResult{Verdict: wantVerdict, Base: base, Head: head, Old: old, New: updated}
			if hasLabel(&github.PullRequest{Labels: labels}, p.label) && historyStatus == 0 {
				want.LabelEvent = labelEvent
			}
		}
		if report.scan != want {
			t.Errorf("scan = %+v; want %+v", report.scan, want)
		}
		wantUpdate := wantVerdict != "" && (wantVerdict != clean || want.LabelEvent != 0 || commentCount == nil || *commentCount > 0)
		if report.updateComment != wantUpdate {
			t.Errorf("%s, comments=%v, label event=%d: update-comment = %t; want %t", wantVerdict, commentCount, want.LabelEvent, report.updateComment, wantUpdate)
		}
		wantReads := int32(1)
		if scanErr != nil {
			wantReads = 0
		} else if hasLabel(&github.PullRequest{Labels: labels}, p.label) {
			wantReads = 2
		}
		if got := prReads.Load() - before; got != wantReads {
			t.Errorf("check made %d PR requests; want %d", got, wantReads)
		}
		for _, want := range wantSummary {
			if !strings.Contains(report.summary, want) {
				t.Errorf("job summary %q does not contain %q", report.summary, want)
			}
		}
		return report.scan
	}
	stale := func(want ...string) {
		t.Helper()
		r, err := g.check(t.Context(), 7, merge, event)
		if err == nil || r.scan != (scanResult{}) {
			t.Fatalf("stale merge produced scan %+v, error %v", r.scan, err)
		}
		for _, s := range want {
			if !strings.Contains(err.Error(), s) || !strings.Contains(r.summary, s) {
				t.Errorf("missing %q in stale error %q or summary %q", s, err, r.summary)
			}
		}
	}
	link := fmt.Sprintf("[`lib/example.go`](https://github.com/upstream/runtime/compare/%s..%s#diff-%x)", old, updated, sha256.Sum256([]byte("lib/example.go")))
	run(errAckRequired, reviewNeeded, "Acknowledgment required", p.label, link, head,
		`upstreamreview importdiff -repo PATH_TO_UPSTREAM -src 'lib' -src 'tools' -import 'example.org/a' -import 'example.org/b' -exclude 'lib/private' -exclude 'tools/dev'\''s files' `+old+" "+updated)
	labels = []*github.Label{{Name: "unrelated"}}
	run(errAckRequired, reviewNeeded)
	labels = append(labels, &github.Label{Name: p.label})
	run(errAckRequired, reviewNeeded, "remove it before reapplying") // A pre-existing label is not an acknowledgment.
	event = labelWebhook()
	ack := run(nil, acknowledged, "Acknowledged", p.label, "lib/example.go")
	event = nil
	run(errAckRequired, reviewNeeded) // A failed/missing comment write is not durable proof.
	comment, err = ack.summaryComment("fork/go", 100, 1, p.label)
	if err != nil {
		t.Fatal(err)
	}
	commentCount = new(1)
	commentAuthor = "someone-else"
	run(errAckRequired, reviewNeeded) // Copying the marker into a human comment doesn't acknowledge anything.
	commentAuthor = "github-actions[bot]"
	// Both outer commits can change without invalidating the approved upstream diff.
	head = git("commit-tree", pinTree(updated), "-p", head, "-m", "docs")
	base = git("commit-tree", oldTree, "-p", base, "-m", "patch")
	merge = checkoutMerge(pinTree(updated), base, head)
	run(nil, acknowledged)
	duringScan = func() { labels = labels[:1] }
	run(errAckRequired, reviewNeeded, "Acknowledgment required", "lib/example.go")
	labels = append(labels, &github.Label{Name: p.label})
	duringScan = nil
	labelEvent++
	appliedAt.Time = appliedAt.Add(time.Minute)
	event = labelWebhook()
	event.PullRequest.UpdatedAt = &github.Timestamp{Time: appliedAt.Add(-time.Minute)}
	run(errAckRequired, reviewNeeded) // An old labeled delivery can't bind a later application, even at the same head.
	event = labelWebhook()
	event.Label = &github.Label{Name: "other"}
	run(errAckRequired, reviewNeeded) // A missed unlabel/relabel can't revive the old record.
	event = labelWebhook()
	ack = run(nil, acknowledged)
	comment, err = ack.summaryComment("fork/go", 101, 1, p.label)
	if err != nil {
		t.Fatal(err)
	}
	oldEvent := event
	event = nil

	newPin := strings.Repeat("6", 40)
	newTree := pinTree(newPin)
	newHead := git("commit-tree", newTree, "-p", head, "-m", "push")
	files = nil // Even a clean scan can't pass for a head that moved during it.
	duringScan = func() { head = newHead }
	stale("PR head moved", newHead[:12], "https://github.com/fork/go/pull/7/checks")
	duringScan = nil
	updated = newPin
	merge = checkoutMerge(newTree, base, head)
	files = []importdiff.Change{{Path: "lib/another.go"}}
	run(errAckRequired, reviewNeeded, "lib/another.go", head)
	event = oldEvent // Re-running the old label event cannot acknowledge the new pin.
	run(errAckRequired, reviewNeeded)
	labelEvent++
	appliedAt.Time = appliedAt.Add(time.Minute)
	event = labelWebhook()
	ack = run(nil, acknowledged)
	comment, err = ack.summaryComment("fork/go", 102, 1, p.label)
	if err != nil {
		t.Fatal(err)
	}
	event = nil
	old = strings.Repeat("3", 40)
	base = git("commit-tree", pinTree(old), "-p", root, "-m", "different target")
	merge = checkoutMerge(newTree, base, head)
	run(errAckRequired, reviewNeeded, "lib/another.go") // Only the base pin changed.
	scanErr = errors.New("upstream comparison failed")
	run(scanErr, "")
	scanErr = nil
	files = nil
	commentCount = new(0)
	run(nil, clean, "No upstream importer changes") // Still schedule cleanup, even with no comments.
	historyStatus = http.StatusForbidden
	run(nil, clean, "Skipping label cleanup")
	files = []importdiff.Change{{Path: "lib/another.go"}}
	if r, err := g.check(t.Context(), 7, merge, nil); err == nil || r.scan != (scanResult{}) {
		t.Fatalf("unverifiable label produced scan %+v, error %v", r.scan, err)
	}
	files = nil
	historyStatus = 0
	labels = labels[:1]
	run(nil, clean, "No upstream importer changes") // The new pin only changed unrelated files.
	commentCount = new(1)
	run(nil, clean) // A reverted bump might have an existing sticky comment.
	commentCount = nil
	run(nil, clean) // Missing count is not evidence that there are no comments.
	// Target branch advances to the proposed pin. The PR no longer changes it,
	// even though its head was based on a different revision.
	base = git("commit-tree", newTree, "-p", base, "-m", "update base")
	stale("base branch moved", base[:12], "Update the PR branch", "re-running this job uses the original commit")
	old = updated
	merge = checkoutMerge(newTree, base, head)
	before := scans
	run(nil, clean, "No upstream importer changes")
	if scans != before {
		t.Fatal("scanned an unrelated PR")
	}

	// Replacing the submodule with a regular file must not pass as an unchanged pin.
	blob := git("hash-object", "-w", "--stdin")
	git("update-index", "--add", "--cacheinfo", "100644", blob, p.submodule)
	merge = checkoutMerge(git("write-tree"), base, head)
	if _, err := g.check(t.Context(), 7, merge, event); err == nil || !strings.Contains(err.Error(), "no valid gitlink") {
		t.Fatalf("unreadable gitlink: %v", err)
	}
}
