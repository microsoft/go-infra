// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/msmajorversions"
)

type releaseCut struct {
	version, notesVersion string
	branchPoint           string // Where the shared upstream release history diverges from the default branch.
	releaseBase           string // Actual target SHA, before creating either PR.
	newBranch             bool
	submoduleHead         string // A published preparation head records the selected upstream commit.
	notesHead             string
	notes                 gitpr.SyncPRRefSet
}

// For a new release, prepareNextRelease picks the latest Go commit available on
// the release branch in both upstream and its mirror (if configured). It creates
// the Microsoft release branch from main if needed, then opens separate PRs to
// pin Go on that branch and start the following release's notes on main.
func prepareNextRelease(f *Flags, dir string, entry ConfigEntry) error {
	version := msmajorversions.Next.Version
	upstream := "release-branch.go" + version
	target, err := entry.TargetBranch(upstream)
	if err != nil {
		return err
	}
	if entry.MainBranch == "" || target == "" || target == entry.MainBranch || entry.SubmoduleTarget == "" || entry.SyncDocs {
		return fmt.Errorf("PrepareNextRelease requires a submodule and a mapping for %s distinct from MainBranch", upstream)
	}
	auth, err := f.ParseAuth()
	if err != nil {
		return err
	}
	sources := []string{entry.Upstream}
	if entry.UpstreamMirror != "" {
		sources = append(sources, entry.UpstreamMirror)
	}
	for _, source := range sources {
		exists, err := remoteBranchExists("", auth.InsertAuth(source), upstream)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Printf("Waiting for upstream branch %s to be available in all configured sources.\n", upstream)
			return nil
		}
	}
	if !*f.DryRun {
		if _, err := f.Auth.NewAuther(); err != nil {
			return fmt.Errorf("release branch preparation needs PR submission credentials: %w", err)
		}
	}
	n, err := strconv.Atoi(strings.TrimPrefix(version, "1."))
	if err != nil {
		return fmt.Errorf("invalid next Go major %q: %w", version, err)
	}
	cut := &releaseCut{
		version:      version,
		notesVersion: fmt.Sprintf("1.%d", n+1),
		notes: gitpr.SyncPRRefSet{
			UpstreamName: target,
			PRRefSet:     gitpr.PRRefSet{Name: entry.MainBranch, Purpose: "prepare-notes-go" + version},
		},
	}
	entry.AutoSyncBranches = []string{upstream}
	entry.AutoMirrorBranches = nil
	entry.SourceBranchLatestCommit = nil
	entry.GoVersionFileContent, entry.GoMicrosoftRevisionFileContent = "", ""
	_, err = makeBranchPRs(f, dir, &entry, cut)
	return err
}

func (c *releaseCut) selectGoCommit(dir, upstream string, b *gitpr.SyncPRRefSet, hasMirror bool) error {
	var err error
	if hasMirror {
		b.Commit, err = executil.SpaceTrimmedCombinedOutput(executil.Dir(dir, "git", "merge-base", b.UpstreamLocalBranch(), b.UpstreamMirrorLocalBranch()))
	} else {
		b.Commit, err = gitcmd.RevParse(dir, b.UpstreamLocalBranch())
	}
	if err != nil {
		return err
	}
	// Commits from before the cut are ancestors of the release tip too. Find where
	// the branches split so we can tell whether a maintainer already pinned a release commit.
	defaultTip, err := gitcmd.FetchRefCommit(dir, upstream, "HEAD")
	if err != nil {
		return fmt.Errorf("fetching upstream HEAD: %w", err)
	}
	c.branchPoint, err = executil.SpaceTrimmedCombinedOutput(executil.Dir(dir, "git", "merge-base", defaultTip, b.Commit))
	if err != nil {
		return fmt.Errorf("finding the upstream branch point for %s: %w", b.UpstreamName, err)
	}
	return nil
}

func (c *releaseCut) preparePRBranches(dir, target, mainBranch string, b *gitpr.SyncPRRefSet) error {
	var err error
	c.releaseBase, err = fetchRemoteBranch(dir, target, b.Name)
	if err != nil {
		return err
	}
	c.newBranch = c.releaseBase == ""
	mainTip, err := gitcmd.FetchRefCommit(dir, target, "refs/heads/"+mainBranch)
	if err != nil {
		return err
	}
	c.submoduleHead, err = fetchRemoteBranch(dir, target, b.PRBranch())
	if err != nil {
		return err
	}
	c.notesHead, err = fetchRemoteBranch(dir, target, c.notes.PRBranch())
	if err != nil {
		return err
	}
	if c.newBranch {
		if c.submoduleHead != "" || c.notesHead != "" {
			return fmt.Errorf("%s is missing but a preparation PR branch already exists; check the branches before retrying", b.Name)
		}
		c.releaseBase = mainTip
		fmt.Printf("Preparing to create %s at %s.\n", b.Name, c.releaseBase)
	}
	if err := gitcmd.Run(dir, "branch", c.notes.PRBranch(), mainTip); err != nil {
		return err
	}
	return gitcmd.Run(dir, "branch", b.PRBranch(), c.releaseBase)
}

func (c *releaseCut) onReleaseLine(dir, commit, tip string) (bool, error) {
	if commit == c.branchPoint {
		return true, nil
	}
	// A gitlink inherited from upstream master may not be in our object database.
	out, err := gitcmd.CombinedOutput(dir, "rev-list", "--ancestry-path", c.branchPoint+".."+tip)
	return slices.Contains(strings.Fields(out), commit), err
}

// Inspect the target first: a maintainer may already have finished the cut, regardless of
// what remains on the PR head.
func (c *releaseCut) resumePreparation(f *Flags, dir, submodule string, target *gitpr.Remote, branch *changedBranch) error {
	b := branch.Refs
	if b == &c.notes {
		branch.RemoteCommit = c.notesHead
	} else {
		prepared, err := c.submodulePrepared(dir, b, submodule)
		if err != nil {
			return err
		}
		if prepared {
			branch.SkipReason = "Release submodule already prepared"
			branch.Result.Commit = c.releaseBase
			return nil
		}
		branch.RemoteCommit = c.submoduleHead
	}
	if branch.RemoteCommit == "" {
		return nil
	}
	if !*f.DryRun {
		auther, err := f.Auth.NewAuther()
		if err != nil {
			return err
		}
		existing, err := gitpr.FindExistingPR(&gitpr.GitHubRequest{Base: b.Name}, target, target, b.PRBranch(), *f.GitHubUser, auther)
		if err != nil {
			return err
		}
		if existing != nil {
			branch.SkipReason = fmt.Sprintf("Release preparation PR #%d is already open for review", existing.Number)
			return nil
		}
	}
	if b != &c.notes {
		// The first push records the selected release tip in this gitlink, even if posting
		// its PR failed. Never replace that selection with a more recent upstream tip.
		pinned, err := gitcmd.GetSubmoduleCommitAtRev(dir, submodule, branch.RemoteCommit)
		if err != nil {
			return err
		}
		if err := gitcmd.Run(dir, "merge-base", "--is-ancestor", pinned, b.Commit); err != nil {
			return fmt.Errorf("cannot verify retained %s gitlink %s in the shared release history; review before retrying: %w", b.PRBranch(), pinned, err)
		}
		b.Commit = pinned
	}
	return nil
}

func (c *releaseCut) submodulePrepared(dir string, b *gitpr.SyncPRRefSet, submodule string) (bool, error) {
	current, err := gitcmd.GetSubmoduleCommitAtRev(dir, submodule, c.releaseBase)
	if err != nil {
		return false, err
	}
	if current == b.Commit {
		return true, nil
	}
	onLine, err := c.onReleaseLine(dir, current, b.UpstreamLocalBranch())
	if err != nil {
		return false, err
	}
	if !c.newBranch && onLine && c.submoduleHead == "" {
		return true, nil // A maintainer already prepared the release branch.
	}
	fork, err := gitcmd.CombinedOutput(dir, "merge-base", c.releaseBase, c.notes.PRBranch())
	if err != nil {
		return false, err
	}
	inherited, err := gitcmd.GetSubmoduleCommitAtRev(dir, submodule, strings.TrimSpace(fork))
	if err != nil {
		return false, err
	}
	if current != inherited {
		if onLine {
			return true, nil
		}
		return false, fmt.Errorf("%s: %s changed manually from %s to %s, which is not on %s at or after the upstream branch point %s; review the gitlink before retrying",
			b.Name, submodule, inherited, current, b.UpstreamName, c.branchPoint)
	}
	if onLine && c.submoduleHead != "" {
		// Don't rewind a branch that has already gone beyond the retained selection.
		selected, err := gitcmd.GetSubmoduleCommitAtRev(dir, submodule, c.submoduleHead)
		if err != nil {
			return false, err
		}
		return c.onReleaseLine(dir, selected, current)
	}
	return false, nil
}

func preparationParent(dir, commit string) (string, error) {
	out, err := gitcmd.CombinedOutput(dir, "rev-list", "--parents", "-n1", strings.TrimSpace(commit))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return "", fmt.Errorf("expected a single-parent preparation commit, got %q", strings.TrimSpace(out))
	}
	return fields[1], nil
}

func validateRetainedPreparation(dir, retained, generated string) error {
	delta := func(parent, commit string) (string, error) {
		return gitcmd.CombinedOutput(dir, "diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--ignore-submodules=none",
			parent, strings.TrimSpace(commit), "--")
	}
	oldBase, err := preparationParent(dir, retained)
	if err != nil {
		return err
	}
	newBase, err := preparationParent(dir, generated)
	if err != nil {
		return err
	}
	if err := gitcmd.Run(dir, "merge-base", "--is-ancestor", oldBase, newBase); err != nil {
		return fmt.Errorf("original base %s is not an ancestor of %s: %w", oldBase, newBase, err)
	}
	want, err := delta(newBase, generated)
	if err != nil {
		return err
	}
	got, err := delta(oldBase, retained)
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("retained patch differs from the intended change")
	}
	return nil
}

func (c *releaseCut) stageNotes(dir string) error {
	docs := filepath.Join(dir, "docs")
	if err := os.Mkdir(docs, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(docs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected a directory at %s", docs)
	}
	name := "docs/go" + c.notesVersion + ".md"
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("expected a regular notes file at %s", name)
		}
		return nil // Never replace notes someone has already started.
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "# Microsoft build of Go %s release notes\n\nAfter the release of %s, %s is no longer supported, per the [Go release policy](https://go.dev/doc/devel/release).\n",
		c.notesVersion, c.notesVersion, msmajorversions.Current.Version)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := gitcmd.Run(dir, "add", "--", name); err != nil {
		return err
	}
	old := "docs/go" + c.version + ".md"
	if err := gitcmd.Run(dir, "diff", "--quiet", c.releaseBase, "HEAD", "--", old); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			fmt.Printf("Keeping %s on main: the release branch does not contain the same notes.\n", old)
			return nil
		}
		return err
	}
	return gitcmd.Run(dir, "rm", "--ignore-unmatch", "--", old)
}
