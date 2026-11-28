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
	prs := mockCutPRs(t, &failPost)
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
	must(addMockFile(upstream, "first.txt", "First release-only change"))
	manualGoCommit := git(upstream, "rev-parse", "HEAD")
	must(addMockFile(upstream, "shared.txt", "Shared release work"))
	selected := git(upstream, "rev-parse", "HEAD")
	git(mirror, "fetch", upstream, "refs/heads/"+source+":refs/heads/"+source)
	must(addMockFile(upstream, "retry.txt", "Shared by the time sync retries"))
	retryGo := git(upstream, "rev-parse", "HEAD")
	must(addMockFile(upstream, "later.txt", "Later release work"))
	later := git(upstream, "rev-parse", "HEAD")
	// This repo advertises its checked-out branch as HEAD. Make the default and release tips diverge.
	git(upstream, "checkout", "-b", "development", "master")
	must(addMockFile(upstream, "development.txt", "Development after the cut"))
	offBranch := git(upstream, "rev-parse", "HEAD")
	// Release preparation must not mirror unrelated branches or stamp a release version.
	previewConfig := config
	previewConfig.AutoMirrorBranches = []string{"ordinary-sync-only"}
	previewConfig.SourceBranchLatestCommit = map[string]string{source: later}
	previewConfig.GoVersionFileContent, previewConfig.GoMicrosoftRevisionFileContent = "go"+version+"rc1", "1"
	must(prepareNextRelease(flags, preview, previewConfig))
	newNotes := "docs/go" + msmajorversions.AfterNextVersion + ".md"
	if got := git(preview, "rev-parse", subPR+"^"); got != mainTip {
		t.Fatalf("default fork = %s, want main %s", got, mainTip)
	}
	if got := git(preview, "rev-parse", subPR+":go"); got != selected {
		t.Fatalf("submodule = %s, want shared tip %s (not upstream-only %s)", got, selected, later)
	}
	if got := git(preview, "diff", "--name-only", subPR+"^", subPR); got != "go" {
		t.Fatalf("submodule PR changed %q, want only go", got)
	}
	if got := git(preview, "ls-tree", "--name-only", notesPR, "--", oldNotes, newNotes); got != newNotes {
		t.Fatalf("notes on main after default cut: %s", got)
	}
	if heads() != initialHeads || len(prs) != 0 {
		t.Fatal("dry run wrote remote state")
	}

	// A maintainer chose a release commit older than the mirror tip. Only the notes need a PR.
	dry = false
	git(target, "checkout", "-b", release, base)
	git(target, "update-index", "--cacheinfo", "160000,"+manualGoCommit+",go")
	git(target, "commit", "-m", "Update the submodule to an earlier Go release commit")
	manualBase := git(target, "rev-parse", "HEAD")
	git(target, "checkout", main)
	must(prepareNextRelease(flags, filepath.Join(dir, "precut"), config))
	if got := git(target, "rev-parse", release); got != manualBase {
		t.Fatalf("moved the existing release branch from %s to %s", manualBase, got)
	}
	if len(prs) != 1 || prs[notesPR] != main {
		t.Fatalf("expected only the notes PR after the manual submodule update, got %v", prs)
	}
	if got := git(target, "show", notesPR+":"+oldNotes); got != "Newer notes on main" {
		t.Fatalf("notes not on the release branch were dropped: %q", got)
	}
	git(target, "branch", "-D", release, notesPR)
	clear(prs)

	// Shared pre-fork history and off-branch commits don't prove a manual cut is complete.
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

	// Use the same entry point for both runs; only the repository configuration changes.
	writeConfig := func(prepare bool) {
		t.Helper()
		config.PrepareNextRelease = prepare
		data, err := json.Marshal([]ConfigEntry{config})
		must(err)
		must(os.WriteFile(configPath, data, 0o600))
	}
	writeConfig(false)
	must(MakePRs(flags))
	if heads() != initialHeads || len(prs) != 0 {
		t.Fatal("sync prepared a release with PrepareNextRelease disabled")
	}
	writeConfig(true)
	git(target, "branch", notesPR, base)
	orphaned := heads()
	if err := prepareNextRelease(flags, filepath.Join(dir, "orphaned-head"), config); err == nil ||
		!strings.Contains(err.Error(), release+" is missing") || heads() != orphaned || len(prs) != 0 {

		t.Fatalf("expected the leftover notes branch to be left for manual review, got %v", err)
	}
	git(target, "branch", "-D", notesPR)
	failPost = true
	if err := MakePRs(flags); err == nil || len(prs) != 0 {
		t.Fatalf("expected interrupted PR posting, got %v, PRs %v", err, prs)
	}
	if got := git(target, "rev-parse", subPR+":go"); got != selected {
		t.Fatalf("submodule after interrupted PR submission = %s, want %s", got, selected)
	}
	// Neither branch has an open PR. Ignore abandoned changes and use the
	// current main and shared Go commit, not the inputs from the interrupted run.
	git(target, "checkout", notesPR)
	git(target, "commit", "--allow-empty", "--author=Maintainer <maintainer@example.com>", "-m", "Abandoned notes draft")
	git(target, "checkout", main)
	must(addMockFile(target, oldNotes, "Notes edited on main before retry"))
	retryMain := git(target, "rev-parse", main)
	git(mirror, "fetch", upstream, retryGo+":refs/heads/"+source)
	failPost = false
	must(MakePRs(flags))
	if got := git(target, "rev-parse", subPR+":go"); got != retryGo {
		t.Fatalf("submodule on retry = %s, want latest shared commit %s", got, retryGo)
	}
	if got := git(target, "rev-parse", notesPR+"^"); got != retryMain {
		t.Fatalf("notes PR base on retry = %s, want current main %s", got, retryMain)
	}
	if got := git(target, "show", notesPR+":"+oldNotes); got != "Notes edited on main before retry" {
		t.Fatalf("retry did not preserve the revised notes on main: %q", got)
	}
	if len(prs) != 2 || prs[subPR] != release || prs[notesPR] != main {
		t.Fatalf("unexpected preparation PRs: %v", prs)
	}
	if got := git(target, "rev-parse", release); got != mainTip {
		t.Fatalf("created branch at %s, want original main tip %s", got, mainTip)
	}
	if got := git(target, "show", notesPR+":"+newNotes); !strings.Contains(got, "Go "+msmajorversions.AfterNextVersion+" release notes") ||
		!strings.Contains(got, msmajorversions.Current.Version+" is no longer supported") {

		t.Fatalf("unexpected new notes: %s", got)
	}
	git(target, "checkout", subPR)
	must(addMockFile(target, "repair.txt", "Maintainer's patch fix"))
	git(target, "checkout", main)
	prs[notesPR] = release // A maintainer retargeted this PR; its head is still in use.
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

func TestPrepareNextReleasePRHead(t *testing.T) {
	dir := t.TempDir()
	upstream := filepath.ToSlash(filepath.Join(dir, "golang", "go"))
	target := filepath.ToSlash(filepath.Join(dir, "microsoft", "go"))
	fork := filepath.ToSlash(filepath.Join(dir, "test-bot", "go"))
	const main = "microsoft/main"
	source := "release-branch.go" + msmajorversions.Next.Version
	release := "microsoft/" + source
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(setupMockRepo(upstream, "master"))
	must(setupMockRepo(target, main))
	must(addMockSubmodule(target, upstream))
	must(addMockFile(upstream, "change.txt", "Go advances"))
	must(runGit(upstream, "branch", source))
	must(runGit(target, "branch", release))
	must(runGit(dir, "clone", target, fork))

	configPath, work := filepath.Join(dir, "sync.json"), filepath.Join(dir, "work")
	dry, create := true, false
	empty, none := "", "none"
	flags := &Flags{
		SyncConfig: &configPath, TempGitDir: &work, DryRun: &dry, CreateBranches: &create,
		GitAuthString: &none, InitialCloneDir: &empty,
	}
	config := ConfigEntry{
		Upstream: upstream, Target: target, MainBranch: main, SubmoduleTarget: "go",
		AutoSyncBranches: []string{"master"}, BranchMap: map[string]string{"master": main, source: release},
	}
	runSync := func(head string, prepare bool) error {
		t.Helper()
		config.Head, config.PrepareNextRelease = head, prepare
		data, err := json.Marshal([]ConfigEntry{config})
		must(err)
		must(os.WriteFile(configPath, data, 0o600))
		return MakePRs(flags)
	}
	if err := runSync(fork, true); err == nil || !strings.Contains(err.Error(), "PrepareNextRelease requires Head") {
		t.Fatalf("expected a config error for release PRs from a fork, got %v", err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("sync started before rejecting the config: stat %s: %v", work, err)
	}
	must(runSync(fork, false)) // Ordinary sync still supports a fork.
	must(runSync(target, true))
}

type cutTransport func(*http.Request) (*http.Response, error)

func (f cutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only finding and opening PRs are allowed; even a successful auto-approval would fail this test.
func mockCutPRs(t *testing.T, failPost *bool) map[string]string {
	t.Helper()
	open := map[string]string{}
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = cutTransport(func(r *http.Request) (*http.Response, error) {
		code := http.StatusOK
		var body string
		if r.URL.Path != "/repos/microsoft/go/pulls" {
			return nil, fmt.Errorf("unexpected GitHub request: %s", r.URL)
		}
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			head, ok := strings.CutPrefix(q.Get("head"), "microsoft:")
			if !ok || q.Get("state") != "open" {
				return nil, fmt.Errorf("unexpected PR query: %s", r.URL)
			}
			body = `[]`
			if base, ok := open[head]; ok && (q.Get("base") == "" || q.Get("base") == base) {
				body = `[{"number":1}]`
			}
		case http.MethodPost:
			if *failPost {
				code, body = http.StatusServiceUnavailable, `{"message":"simulated interruption"}`
				break
			}
			var pr struct{ Base, Head, Body string }
			if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
				return nil, err
			}
			if !strings.Contains(pr.Body, "automatically generated") || !strings.Contains(pr.Body, "docs/automation/sync.md") ||
				!strings.Contains(pr.Body, "review") || strings.Contains(pr.Body, "auto-merge") {

				return nil, fmt.Errorf("unexpected release preparation PR description for %s: %s", pr.Base, pr.Body)
			}
			head := strings.TrimPrefix(pr.Head, "microsoft:")
			if _, found := open[head]; found {
				return nil, fmt.Errorf("duplicate PR for %s", head)
			}
			open[head] = pr.Base
			code, body = http.StatusCreated, `{"number":1,"node_id":"test"}`
		default:
			return nil, fmt.Errorf("unexpected GitHub method: %s", r.Method)
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return open
}
