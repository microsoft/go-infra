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

func TestReviewOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("other=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan := scanResult{Verdict: reviewNeeded, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	for _, r := range []report{{}, {summary: "stale scan"}, {summary: "findings", scan: scan}} {
		if err := r.writeOutput(path, "2"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := strings.CutPrefix(string(data), "other=value\nreview=")
	var got scanResult
	scan.Attempt = 2
	if !ok || json.Unmarshal([]byte(output), &got) != nil || got != scan {
		t.Fatalf("expected only the verified scan in the step outputs, got %q", data)
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
	files := []importdiff.Change{{Path: "lib/example.go"}}
	var scanErr error
	var duringScan func()
	var scans int
	var prReads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/fork/go/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		prReads.Add(1)
		pr := github.PullRequest{
			Number: new(7), State: new("open"), Labels: labels,
			Head: &github.PullRequestBranch{SHA: &head},
			Base: &github.PullRequestBranch{Ref: new("main"), SHA: &base},
		}
		if err := json.NewEncoder(w).Encode(pr); err != nil {
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
	run := func(wantErr error, wantVerdict verdict, wantSummary ...string) {
		t.Helper()
		before := prReads.Load()
		report, err := g.check(t.Context(), 7, merge)
		if !errors.Is(err, wantErr) {
			t.Fatalf("check = %v; want %v", err, wantErr)
		}
		var want scanResult
		if wantVerdict != "" {
			want = scanResult{Verdict: wantVerdict, Base: base, Head: head}
		}
		if report.scan != want {
			t.Errorf("scan = %+v; want %+v", report.scan, want)
		}
		wantReads := int32(1)
		if scanErr != nil {
			wantReads = 0
		}
		if got := prReads.Load() - before; got != wantReads {
			t.Errorf("check made %d PR requests; want %d", got, wantReads)
		}
		for _, want := range wantSummary {
			if !strings.Contains(report.summary, want) {
				t.Errorf("job summary %q does not contain %q", report.summary, want)
			}
		}
	}
	stale := func(want ...string) {
		t.Helper()
		r, err := g.check(t.Context(), 7, merge)
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
	run(nil, acknowledged, "Acknowledged", p.label, "lib/example.go")
	duringScan = func() { labels = labels[:1] }
	run(errAckRequired, reviewNeeded, "Acknowledgment required", "lib/example.go")
	labels = append(labels, &github.Label{Name: p.label})

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
	run(nil, acknowledged, "Acknowledged", "lib/another.go", head) // Same PR label, newly scanned changes.
	scanErr = errors.New("upstream comparison failed")
	run(scanErr, "")
	scanErr = nil
	labels = labels[:1]
	run(errAckRequired, reviewNeeded, "lib/another.go")
	files = nil
	run(nil, clean, "No upstream importer changes") // The new pin only changed unrelated files.
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
	if _, err := g.check(t.Context(), 7, merge); err == nil || !strings.Contains(err.Error(), "no valid gitlink") {
		t.Fatalf("unreadable gitlink: %v", err)
	}
}
