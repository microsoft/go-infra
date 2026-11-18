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
		PrepareNextRelease: map[string]string{},
	}
	dry, create := true, false
	empty, none, bot, pat := "", "none", "test-bot", "test-token"
	configPath, work := filepath.Join(dir, "sync.json"), filepath.Join(dir, "work")
	flags := &Flags{
		DryRun: &dry, CreateBranches: &create, GitAuthString: &none, InitialCloneDir: &empty,
		Auth: githubutil.GitHubAuthFlags{GitHubPat: &pat}, GitHubUser: &bot, SyncConfig: &configPath, TempGitDir: &work,
	}
	failPost := false
	prs := mockCutPRs(t, bot, &failPost)
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

	// Refuse a real, fetched commit outside target main, without creating any remote branch.
	config.PrepareNextRelease[version] = selected
	dry = false
	if err := prepareNextRelease(flags, filepath.Join(dir, "bad-base"), config); err == nil || heads() != initialHeads {
		t.Fatalf("invalid fork point not rejected before push: %v", err)
	}

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
	config.PrepareNextRelease[version] = base
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
	if got := git(target, "rev-parse", release); got != base {
		t.Fatalf("created branch at %s, want override %s", got, base)
	}
	if got := git(target, "show", notesPR+":"+oldNotes); got != "Newer notes on main" {
		t.Fatalf("dropped notes not preserved on release branch: %q", got)
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

type cutTransport func(*http.Request) (*http.Response, error)

func (f cutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only finding and opening PRs are allowed; even a successful auto-approval would fail this test.
func mockCutPRs(t *testing.T, bot string, failPost *bool) map[string]string {
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
				nodes = fmt.Sprintf(`{"number":1,"id":"test","author":{"login":%q},"headRepositoryOwner":{"login":"microsoft"},"baseRepository":{"owner":{"login":"microsoft"},"nameWithOwner":"microsoft/go"}}`, bot)
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
			head := strings.TrimPrefix(pr.Head, "microsoft:")
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
