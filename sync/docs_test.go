// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/msmajorversions"
)

func Test_MakeBranchPRs_Docs(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "microsoft", "go")
	if err := setupMockRepo(target, "microsoft/main"); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := gitcmd.CombinedOutput(target, args...)
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(target, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := addMockFile(target, name, content); err != nil {
			t.Fatal(err)
		}
	}
	write("eng/doc/deleted.md", "Deleted on main")
	write("eng/doc/renamed.md", "Renamed on main")
	write("eng/doc/file-to-dir", "Old file")
	write("eng/doc/dir-to-file/old.md", "Old directory")
	write("eng/docs/not-matched.md", "Branch-specific docs")
	write("src/code.go", "Branch-specific code")
	write("VERSION", "Branch-specific version")
	write("MICROSOFT_REVISION", "Branch-specific revision")
	// Simulate a submodule pointer that must not be updated by a documentation sync.
	submoduleCommit := git("rev-parse", "HEAD")
	git("update-index", "--add", "--cacheinfo", "160000,"+submoduleCommit+",go")
	git("commit", "-m", "Add submodule pointer")

	versions := []string{msmajorversions.Previous.Version, msmajorversions.Current.Version}
	for _, version := range append(append([]string{}, versions...), msmajorversions.Next.Version, "1.1") {
		git("branch", "microsoft/release-branch.go"+version)
	}
	original := git("rev-parse", "HEAD")
	git("rm", "-r", "--", "eng/doc")
	git("commit", "-m", "Remove old docs")
	docs := map[string]string{
		"README.md":                  "# Main README\n",
		"SUPPORT.md":                 "Support details\r\n",
		"SECURITY.md":                "",
		"eng/doc/guide.md":           "# Guide\n",
		"eng/doc/nested/deep.md":     "Nested documentation without final newline",
		"eng/doc/space and\nline.md": "Unusual filename\n",
		"eng/doc/new-name.md":        "Renamed on main",
		"eng/doc/file-to-dir/new.md": "Now a directory\n",
		"eng/doc/dir-to-file":        "Now a file\n",
	}
	for name, content := range docs {
		write(name, content)
	}
	write("eng/docs/not-matched.md", "Main-only docs")
	write("src/code.go", "Main-only code")
	write("VERSION", "Main-only version")
	write("MICROSOFT_REVISION", "Main-only revision")
	git("update-index", "--add", "--cacheinfo", "160000,"+original+",go")
	git("commit", "-m", "Update main submodule pointer")
	mainCommit := git("rev-parse", "HEAD")

	dryRun, createBranches, noAuth, empty := true, false, "none", ""
	flags := &Flags{
		DryRun:          &dryRun,
		CreateBranches:  &createBranches,
		GitAuthString:   &noAuth,
		InitialCloneDir: &empty,
	}
	config := &ConfigEntry{
		Upstream:   target,
		Target:     target,
		MainBranch: "microsoft/main",
		SyncDocs:   true,
	}
	work := filepath.Join(dir, "work")
	results, err := MakeBranchPRs(flags, work, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(versions) {
		t.Fatalf("got %d results, want %d", len(results), len(versions))
	}
	for i, result := range results {
		branch := "microsoft/release-branch.go" + versions[i]
		for name, content := range docs {
			got, err := gitcmd.Show(work, strings.TrimSpace(result.Commit)+":"+name)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf(
				"<!-- This document is maintained in https://github.com/microsoft/go/blob/microsoft/main/%s and automatically copied to %s, DO NOT EDIT. -->\n\n%s",
				strings.NewReplacer(" ", "%20", "\n", "%0A").Replace(name), branch, content)
			if got != want {
				t.Errorf("%s:%s = %q, want %q", branch, name, got, want)
			}
			// The source and target branches are unchanged in dry-run mode.
			ensureFileContent(t, filepath.Join(target, name), content)
		}
		for _, name := range []string{"eng/doc/deleted.md", "eng/doc/renamed.md"} {
			if _, err := gitcmd.Show(work, strings.TrimSpace(result.Commit)+":"+name); err == nil {
				t.Errorf("deleted doc %s still exists on %s", name, branch)
			}
		}
		changed, err := gitcmd.CombinedOutput(work, "diff", "--name-only", original, strings.TrimSpace(result.Commit),
			"--", "go", "src", "eng/docs", "VERSION", "MICROSOFT_REVISION")
		if err != nil {
			t.Fatal(err)
		}
		if changed != "" {
			t.Errorf("non-documentation files changed: %s", changed)
		}
		parents, err := gitcmd.CombinedOutput(work, "rev-list", "--parents", "-n", "1", strings.TrimSpace(result.Commit))
		if err != nil {
			t.Fatal(err)
		}
		if fields := strings.Fields(parents); len(fields) != 2 || fields[1] != original {
			t.Errorf("doc sync should be a single-parent commit based on the target branch, got %s", parents)
		}
		if got := git("rev-parse", branch); got != original {
			t.Errorf("target branch changed during dry run: %s", got)
		}
		// Simulate merging the generated commit, without contacting GitHub.
		git("fetch", work, "refs/heads/dev/auto-sync-docs/"+branch+":refs/heads/"+branch)
	}
	for _, branch := range []string{"microsoft/main", "microsoft/release-branch.go" + msmajorversions.Next.Version, "microsoft/release-branch.go1.1"} {
		want := original
		if branch == "microsoft/main" {
			want = mainCommit
		}
		if got := git("rev-parse", branch); got != want {
			t.Errorf("unexpected change to %s: %s", branch, got)
		}
	}
	repeated, err := MakeBranchPRs(flags, filepath.Join(dir, "repeat"), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated) != len(results) {
		t.Fatalf("repeat got %d results, want %d", len(repeated), len(results))
	}
	for i := range repeated {
		if repeated[i].Commit != results[i].Commit {
			t.Errorf("repeat sync created a new commit: %s != %s", repeated[i].Commit, results[i].Commit)
		}
	}
}

func Test_syncDocs_MissingFiles(t *testing.T) {
	for _, targetHasDocs := range []bool{false, true} {
		t.Run(fmt.Sprint(targetHasDocs), func(t *testing.T) {
			dir := t.TempDir()
			if err := setupMockRepo(dir, "main"); err != nil {
				t.Fatal(err)
			}
			if err := runGit(dir, "rm", "README.md"); err != nil {
				t.Fatal(err)
			}
			if err := runGit(dir, "commit", "--allow-empty", "-m", "No source docs"); err != nil {
				t.Fatal(err)
			}
			if err := runGit(dir, "branch", "source"); err != nil {
				t.Fatal(err)
			}
			if targetHasDocs {
				if err := addMockFile(dir, "README.md", "Target-only doc"); err != nil {
					t.Fatal(err)
				}
			}
			if err := syncDocs(dir, "https://github.com/microsoft/go", &gitpr.SyncPRRefSet{Commit: "source"}); err != nil {
				t.Fatal(err)
			}
			ensureMissing(t, filepath.Join(dir, "README.md"))
		})
	}
}

func Test_MakeBranchPRs_DocsInvalidConfig(t *testing.T) {
	for _, config := range []ConfigEntry{
		{SyncDocs: true},
		{SyncDocs: true, MainBranch: "main", SubmoduleTarget: "go"},
		{SyncDocs: true, MainBranch: "main", BranchMap: map[string]string{"main": "main"}},
		{SyncDocs: true, MainBranch: "main", AutoSyncBranches: []string{"main"}},
	} {
		if _, err := MakeBranchPRs(nil, "", &config); err == nil {
			t.Errorf("expected error for invalid config: %+v", config)
		}
	}
}

func Test_syncDocs_Symlinks(t *testing.T) {
	for _, sourceIsSymlink := range []bool{false, true} {
		t.Run(fmt.Sprint(sourceIsSymlink), func(t *testing.T) {
			dir := t.TempDir()
			repo := filepath.Join(dir, "repo")
			if err := setupMockRepo(repo, "main"); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(dir, "outside.md")
			if err := os.WriteFile(outside, []byte("Do not overwrite"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !sourceIsSymlink {
				if err := runGit(repo, "branch", "source"); err != nil {
					t.Fatal(err)
				}
			}
			if err := runGit(repo, "rm", "README.md"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(repo, "README.md")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := runGit(repo, "add", "README.md"); err != nil {
				t.Fatal(err)
			}
			if err := runGit(repo, "commit", "-m", "Add symlink"); err != nil {
				t.Fatal(err)
			}
			if sourceIsSymlink {
				if err := runGit(repo, "branch", "source"); err != nil {
					t.Fatal(err)
				}
			}
			err := syncDocs(repo, "https://github.com/microsoft/go", &gitpr.SyncPRRefSet{
				Commit: "source", UpstreamName: "microsoft/main",
			})
			if sourceIsSymlink {
				if err == nil || !strings.Contains(err.Error(), "non-regular") {
					t.Fatalf("expected non-regular file error, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(filepath.Join(repo, "README.md"))
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() {
					t.Error("copied README is not a regular file")
				}
			}
			ensureFileContent(t, outside, "Do not overwrite")
		})
	}
}
