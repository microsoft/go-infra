// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/msmajorversions"
)

func TestPrepareNextRelease(t *testing.T) {
	dir := t.TempDir()
	upstream := filepath.ToSlash(filepath.Join(dir, "golang", "go"))
	target := filepath.ToSlash(filepath.Join(dir, "microsoft", "go"))
	const main = "microsoft/main"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	git := func(repo string, args ...string) string {
		t.Helper()
		out, err := gitcmd.CombinedOutput(repo, args...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	must(setupMockRepo(upstream, "master"))
	beforeCut := git(upstream, "rev-parse", "HEAD")
	must(addMockFile(upstream, "development.txt", "Development before branching"))
	must(setupMockRepo(target, main))
	must(addMockSubmodule(target, upstream))
	version := msmajorversions.Next.Version
	source := "release-branch.go" + version
	release := "microsoft/" + source
	oldNotes := "docs/go" + version + ".md"
	must(os.Mkdir(filepath.Join(target, "docs"), 0o755))
	must(addMockFile(target, oldNotes, "Notes at the fork point"))
	base := git(target, "rev-parse", "HEAD")
	must(addMockFile(target, oldNotes, "Newer notes on main"))
	mainTip := git(target, "rev-parse", "HEAD")

	mirror := filepath.ToSlash(filepath.Join(dir, "mirror"))
	git(dir, "clone", "--branch", "master", upstream, mirror)
	config := ConfigEntry{
		Upstream: upstream, UpstreamMirror: mirror, Target: target, MainBranch: main, SubmoduleTarget: "go",
		BranchMap:        map[string]string{"master": main, "release-branch.go*": "microsoft/?"},
		AutoSyncBranches: []string{"master"}, AutoMirrorBranches: []string{"release-branch.go*"},
		PrepareNextRelease: true,
	}
	dry, create := true, false
	empty, none, bot, pat := "", "none", "test-bot", "test-token"
	configPath, work := filepath.Join(dir, "sync.json"), filepath.Join(dir, "work")
	flags := &Flags{
		DryRun: &dry, CreateBranches: &create, GitAuthString: &none, InitialCloneDir: &empty,
		Auth: githubutil.GitHubAuthFlags{GitHubPat: &pat}, GitHubUser: &bot, SyncConfig: &configPath, TempGitDir: &work,
	}
	failPost := false
	prs := mockCutPRs(t, bot, "microsoft", &failPost)
	heads := func() string { return git(target, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") }
	initialHeads := heads()

	preview := filepath.Join(dir, "dry")
	waiting := func() {
		t.Helper()
		must(prepareNextRelease(flags, preview, config))
		if _, err := os.Stat(preview); !os.IsNotExist(err) {
			t.Fatalf("missing release branch should do nothing, stat: %v", err)
		}
	}
	waiting()
	git(upstream, "branch", source, "master")
	waiting() // The upstream branch exists, but the mirror hasn't picked it up yet.
	git(mirror, "branch", source, "master")
	subPR, notesPR := "dev/prepare-release/"+release, "dev/prepare-notes-go"+version+"/"+main
	atCut := filepath.Join(dir, "at-cut")
	must(prepareNextRelease(flags, atCut, config))
	if got := git(atCut, "rev-parse", notesPR+"^"); got != mainTip {
		t.Fatalf("notes PR base = %s, want %s as soon as the branch exists, without tags or release-only commits", got, mainTip)
	}

	git(upstream, "checkout", source)
	must(addMockFile(upstream, "shared.txt", "Shared release work"))
	selected := git(upstream, "rev-parse", "HEAD")
	git(mirror, "fetch", upstream, "refs/heads/"+source+":refs/heads/"+source)
	must(addMockFile(upstream, "later.txt", "Later release work"))
	later := git(upstream, "rev-parse", "HEAD")
	git(upstream, "checkout", "master")
	must(prepareNextRelease(flags, preview, config))
	n, err := strconv.Atoi(strings.TrimPrefix(version, "1."))
	must(err)
	newNotes := fmt.Sprintf("docs/go1.%d.md", n+1)
	if got := git(preview, "rev-parse", subPR+"^"); got != mainTip {
		t.Fatalf("default fork = %s, want main %s", got, mainTip)
	}
	if got := git(preview, "rev-parse", subPR+":go"); got != selected {
		t.Fatalf("submodule = %s, want shared tip %s (not upstream-only %s)", got, selected, later)
	}
	if got := git(preview, "ls-tree", "--name-only", notesPR, "--", oldNotes, newNotes); got != newNotes {
		t.Fatalf("notes on main after default cut: %s", got)
	}
	if heads() != initialHeads || len(prs) != 0 {
		t.Fatal("dry run wrote remote state")
	}

	// A maintainer can cut the branch before main advances. Use it without dropping newer notes on main.
	dry = false
	git(target, "branch", release, base)
	must(prepareNextRelease(flags, filepath.Join(dir, "precut"), config))
	if got := git(target, "rev-parse", release); got != base {
		t.Fatalf("moved the existing release branch from %s to %s", base, got)
	}
	if got := git(target, "show", notesPR+":"+oldNotes); got != "Newer notes on main" {
		t.Fatalf("notes not on the release branch were dropped: %q", got)
	}
	git(target, "branch", "-D", release, notesPR)
	clear(prs)

	// Shared pre-fork history and off-branch commits don't prove a manual cut is complete.
	git(upstream, "checkout", "-b", "other", "master")
	must(addMockFile(upstream, "other.txt", "Not on the release branch"))
	offBranch := git(upstream, "rev-parse", "HEAD")
	git(upstream, "checkout", "master")
	for _, link := range []string{beforeCut, offBranch} {
		git(target, "checkout", "-b", release, base)
		git(target, "update-index", "--cacheinfo", "160000,"+link+",go")
		git(target, "commit", "-m", "Manually advance the inherited gitlink")
		git(target, "checkout", main)
		manual := heads()
		if err := prepareNextRelease(flags, filepath.Join(dir, "manual-"+link), config); err == nil ||
			!strings.Contains(err.Error(), link) || !strings.Contains(err.Error(), "review") || heads() != manual || len(prs) != 0 {

			t.Fatalf("manual gitlink %s was not preserved and reported for review: %v", link, err)
		}
		git(target, "branch", "-D", release)
	}

	// Use the scheduled entry point so the cut must coexist with ordinary master sync.
	data, err := json.Marshal([]ConfigEntry{config})
	must(err)
	must(os.WriteFile(configPath, data, 0o600))
	must(MakePRs(flags))
	if heads() != initialHeads || len(prs) != 0 {
		t.Fatal("configured repo cut a release without the explicit opt-in")
	}
	flags.PrepareNextRelease = true
	failPost = true
	if err := MakePRs(flags); err == nil || len(prs) != 0 {
		t.Fatalf("expected interrupted PR posting, got %v, PRs %v", err, prs)
	}
	firstSub, firstNotes := git(target, "rev-parse", subPR), git(target, "rev-parse", notesPR)
	if got := git(target, "rev-parse", subPR+":go"); got != selected {
		t.Fatalf("first push selected %s, want %s", got, selected)
	}
	must(addMockFile(target, "unrelated.txt", "Unrelated change on main before retry"))
	git(mirror, "fetch", upstream, "refs/heads/"+source+":refs/heads/"+source)
	failPost = false
	// The author and original parent alone do not authorize a different patch.
	wrong := git(target, "rev-parse", main)
	git(target, "update-ref", "refs/heads/"+notesPR, wrong, firstNotes)
	if err := prepareNextRelease(flags, filepath.Join(dir, "unexpected-patch"), config); err == nil ||
		!strings.Contains(err.Error(), "retained patch differs") || len(prs) != 0 {

		t.Fatalf("unexpected retained patch was not rejected: %v, PRs %v", err, prs)
	}
	git(target, "update-ref", "refs/heads/"+notesPR, firstNotes, wrong)
	must(MakePRs(flags))
	if git(target, "rev-parse", subPR) != firstSub || git(target, "rev-parse", notesPR) != firstNotes {
		t.Fatal("retry changed a published head instead of posting its original patch")
	}
	if len(prs) != 2 || prs[subPR] != release || prs[notesPR] != main {
		t.Fatalf("unexpected preparation PRs: %v", prs)
	}
	if got := git(target, "rev-parse", release); got != mainTip {
		t.Fatalf("created branch at %s, want original main tip %s", got, mainTip)
	}
	if got := git(target, "show", notesPR+":"+newNotes); !strings.Contains(got, fmt.Sprintf("Go 1.%d release notes", n+1)) ||
		!strings.Contains(got, msmajorversions.Current.Version+" is no longer supported") {

		t.Fatalf("unexpected new notes: %s", got)
	}
	git(target, "checkout", subPR)
	must(addMockFile(target, "repair.txt", "Maintainer's patch fix"))
	git(target, "checkout", main)
	pending := heads()
	must(MakePRs(flags))
	if heads() != pending {
		t.Fatal("rerun rewrote open PRs or an existing release branch")
	}

	git(target, "checkout", release)
	git(target, "merge", "--ff-only", subPR)
	git(target, "update-index", "--cacheinfo", "160000,"+later+",go")
	git(target, "commit", "-m", "Already on a newer release")
	git(target, "checkout", main)
	git(target, "merge", "--no-edit", notesPR)
	must(addMockFile(target, newNotes, "Handwritten release notes"))
	clear(prs)
	completed := heads()
	must(MakePRs(flags))
	if len(prs) != 0 || heads() != completed {
		t.Fatal("completed preparation was repeated")
	}
}

func TestPrepareNextReleaseNotesOnlyForkRetry(t *testing.T) {
	dir := t.TempDir()
	upstream := filepath.ToSlash(filepath.Join(dir, "golang", "go"))
	target := filepath.ToSlash(filepath.Join(dir, "microsoft", "go"))
	fork := filepath.ToSlash(filepath.Join(dir, "test-bot", "go"))
	const main = "microsoft/main"
	version := msmajorversions.Next.Version
	source := "release-branch.go" + version
	release := "microsoft/" + source
	notes := "dev/prepare-notes-go" + version + "/" + main
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	git := func(repo string, args ...string) string {
		t.Helper()
		out, err := gitcmd.CombinedOutput(repo, args...)
		must(err)
		return strings.TrimSpace(out)
	}
	must(setupMockRepo(upstream, "master"))
	pin := git(upstream, "rev-parse", "HEAD")
	git(upstream, "branch", source)
	must(setupMockRepo(target, main))
	must(addMockSubmodule(target, upstream))
	base := git(target, "rev-parse", "HEAD")
	git(dir, "clone", "--branch", main, target, fork)
	config := ConfigEntry{
		Upstream: upstream, Target: target, Head: fork, MainBranch: main, SubmoduleTarget: "go",
		BranchMap: map[string]string{source: release}, PrepareNextRelease: true,
	}
	dry, create := false, false
	empty, none, bot, pat := "", "none", "test-bot", "test-token"
	flags := &Flags{
		DryRun: &dry, CreateBranches: &create, GitAuthString: &none, InitialCloneDir: &empty,
		Auth: githubutil.GitHubAuthFlags{GitHubPat: &pat}, GitHubUser: &bot,
	}
	failPost := false
	prs := mockCutPRs(t, bot, bot, &failPost)

	// Reject only the target push. The fork should already have the notes head, but no
	// submodule head: the inherited gitlink was exactly the shared release tip.
	git(target, "config", "receive.hideRefs", "refs/heads/"+release)
	if err := prepareNextRelease(flags, filepath.Join(dir, "first"), config); err == nil {
		t.Fatal("expected target to reject release branch creation")
	}
	retained := git(fork, "rev-parse", notes)
	subPR := "dev/prepare-release/" + release
	if git(target, "branch", "--list", release) != "" || git(fork, "branch", "--list", subPR) != "" || len(prs) != 0 {
		t.Fatal("expected only the notes head to have been published")
	}
	git(target, "config", "--unset", "receive.hideRefs")
	must(addMockFile(target, "after.txt", "Main advances before retry"))
	git(upstream, "checkout", source)
	must(addMockFile(upstream, "later.txt", "Go advances before retry"))
	git(upstream, "checkout", "master")
	// A notes head without its selection is not enough to guess a new fork point.
	git(fork, "update-ref", "refs/heads/"+notes, base, retained)
	if err := prepareNextRelease(flags, filepath.Join(dir, "missing-selection"), config); err == nil || !strings.Contains(err.Error(), "review before retrying") {
		t.Fatalf("expected a missing selection to require review, got %v", err)
	}
	if git(target, "branch", "--list", release) != "" || len(prs) != 0 {
		t.Fatal("published a release branch or PR without a retained selection")
	}
	git(fork, "update-ref", "refs/heads/"+notes, retained, base)

	must(prepareNextRelease(flags, filepath.Join(dir, "retry"), config))
	if got := git(target, "rev-parse", release); got != base {
		t.Fatalf("release branch = %s, want original fork point %s", got, base)
	}
	if got := git(target, "rev-parse", release+":go"); got != pin {
		t.Fatalf("gitlink = %s, want original Go commit %s", got, pin)
	}
	if got := git(fork, "rev-parse", notes); got != retained {
		t.Fatalf("notes head = %s, want retained %s", got, retained)
	}
	if len(prs) != 1 || prs[notes] != main || git(fork, "branch", "--list", subPR) != "" {
		t.Fatalf("retry should submit only the notes PR, got %v", prs)
	}
}

type cutTransport func(*http.Request) (*http.Response, error)

func (f cutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only finding and opening PRs are allowed; even a successful auto-approval would fail this test.
func mockCutPRs(t *testing.T, bot, headOwner string, failPost *bool) map[string]string {
	t.Helper()
	open := map[string]string{}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = cutTransport(func(r *http.Request) (*http.Response, error) {
		code := http.StatusOK
		var body string
		switch r.URL.Path {
		case "/graphql":
			var q struct {
				Query     string
				Variables map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				return nil, err
			}
			if !strings.HasPrefix(q.Query, "query ") {
				return nil, fmt.Errorf("unexpected GitHub mutation: %s", q.Query)
			}
			nodes := ""
			if base, ok := open[q.Variables["headRefName"]]; ok && base == q.Variables["baseRefName"] {
				nodes = fmt.Sprintf(`{"number":1,"id":"test","author":{"login":%q},"headRepositoryOwner":{"login":%q},"baseRepository":{"owner":{"login":"microsoft"},"nameWithOwner":"microsoft/go"}}`, bot, headOwner)
			}
			body = `{"data":{"repository":{"pullRequests":{"nodes":[` + nodes + `]}}}}`
		case "/repos/microsoft/go/pulls":
			if *failPost {
				code, body = http.StatusServiceUnavailable, `{"message":"simulated interruption"}`
				break
			}
			var pr struct{ Base, Head string }
			if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
				return nil, err
			}
			head := strings.TrimPrefix(pr.Head, headOwner+":")
			if _, found := open[head]; found {
				return nil, fmt.Errorf("duplicate PR for %s", head)
			}
			open[head] = pr.Base
			code, body = http.StatusCreated, `{"number":1,"node_id":"test"}`
		default:
			return nil, fmt.Errorf("unexpected GitHub request: %s", r.URL)
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return open
}
