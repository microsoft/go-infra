// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
)

func TestFixupPatch(t *testing.T) {
	for _, number := range []int{1, 2} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			config, git := newFixupTestRepo(t, "Same subject", "Same subject")
			t.Setenv("GIT_SEQUENCE_EDITOR", "false")
			t.Setenv("GIT_EDITOR", "false")
			_, goDir := config.FullProjectRoots()
			other := "HEAD"
			if number == 2 {
				other = "HEAD^"
			}

			otherPatch := git("show", "--format=", other)
			file := fmt.Sprintf("file%d.txt", number)
			writeFixupTestFile(t, goDir, file, "updated\n")
			writeFixupTestFile(t, goDir, "new file.txt", "new\n")
			if err := os.Remove(filepath.Join(goDir, "old.txt")); err != nil {
				t.Fatal(err)
			}

			git("add", "--all")
			wantTree := git("write-tree")
			if err := fixupPatch(config, number); err != nil {
				t.Fatal(err)
			}
			if got := git("rev-parse", "HEAD^{tree}"); got != wantTree {
				t.Errorf("final tree = %s, want staged tree %s", got, wantTree)
			}
			if got := git("show", "--format=", other); got != otherPatch {
				t.Errorf("fixup changed the other patch:\n%s", got)
			}
			if got := git("log", "-2", "--format=%s"); got != "Same subject\nSame subject" {
				t.Errorf("unexpected patch subjects:\n%s", got)
			}

			target := "HEAD^"
			if number == 2 {
				target = "HEAD"
			}
			if got := git("show", target+":"+file); got != "updated" {
				t.Errorf("target patch content = %q, want updated", got)
			}
			if got := git("status", "--porcelain"); got != "" {
				t.Errorf("fixup left a dirty submodule:\n%s", got)
			}
			if err := extractPatches(config, "", false, false); err != nil {
				t.Fatal(err)
			}

			files, err := filepath.Glob(filepath.Join(config.RootDir, "patches", "*.patch"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 2 {
				t.Fatalf("got %d extracted patches, want 2", len(files))
			}

			for i, path := range files {
				p, err := patch.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Contains(p.Content, "new file.txt"); got != (i+1 == number) {
					t.Errorf("new file is in patch %d: %v, target is %d", i+1, got, number)
				}
			}
		})
	}
}

func TestFixupPatchNumbering(t *testing.T) {
	command := commandPrefix + patchNumberCommand + "10"
	for _, tt := range []struct {
		name    string
		first   string
		second  string
		number  int
		missing int
	}{
		{name: "body", first: "First patch", second: "Second patch\n\n" + command, number: 10, missing: 2},
		{name: "subject", first: command, second: "Second patch", number: 2, missing: 10},
		{name: "subject continuation", first: "First patch\n" + command, second: "Second patch", number: 2, missing: 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, git := newFixupTestRepo(t, tt.first, tt.second)
			_, goDir := config.FullProjectRoots()
			writeFixupTestFile(t, goDir, "file2.txt", "updated\n")
			git("add", "--all")
			if err := fixupPatch(config, tt.missing); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("patch %d does not exist", tt.missing)) {
				t.Fatalf("fixupPatch(%d) error = %v, want missing patch", tt.missing, err)
			}
			if err := fixupPatch(config, tt.number); err != nil {
				t.Fatal(err)
			}
			if got := git("show", "HEAD:file2.txt"); got != "updated" {
				t.Errorf("patch %d content = %q, want updated", tt.number, got)
			}
			if err := extractPatches(config, "", false, false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(config.RootDir, "patches", fmt.Sprintf("%04d-Second-patch.patch", tt.number))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFixupPatchEmptyCommit(t *testing.T) {
	config, git := newFixupTestRepo(t)
	_, goDir := config.FullProjectRoots()
	git("commit", "--allow-empty", "-m", "Empty\n\n"+commandPrefix+patchNumberCommand+"10")
	writeFixupTestFile(t, goDir, "file3.txt", "original\n")
	git("add", "--all")
	git("commit", "-m", "Third patch")
	writeFixupTestFile(t, goDir, "new.txt", "updated\n")
	git("add", "--all")
	if err := fixupPatch(config, 3); err == nil || !strings.Contains(err.Error(), "patch 3 does not exist") {
		t.Fatalf("fixupPatch(3) error = %v, want missing patch", err)
	}
	if err := fixupPatch(config, 10); err != nil {
		t.Fatal(err)
	}
	if err := extractPatches(config, "", false, false); err != nil {
		t.Fatal(err)
	}

	p, err := patch.ReadFile(filepath.Join(config.RootDir, "patches", "0010-Empty.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Content, "+updated\n") {
		t.Error("formerly empty patch does not contain the fixup")
	}
	if _, err := os.Stat(filepath.Join(config.RootDir, "patches", "0011-Third-patch.patch")); err != nil {
		t.Fatal(err)
	}
}

func TestFixupPatchNoChanges(t *testing.T) {
	config, git := newFixupTestRepo(t)
	head := git("rev-parse", "HEAD")
	if err := fixupPatch(config, 1); err != nil {
		t.Fatal(err)
	}
	if got := git("rev-parse", "HEAD"); got != head {
		t.Errorf("no-op fixup changed HEAD from %s to %s", head, got)
	}
	if err := fixupPatch(config, 3); err == nil || !strings.Contains(err.Error(), "patch 3 does not exist") {
		t.Errorf("no-op fixup error = %v, want missing patch", err)
	}
}

func TestFixupPatchRejectsUnsafeState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		number int
		want   string
	}{
		{name: "zero patch", number: 0, want: "-patch must be"},
		{name: "negative patch", number: -1, want: "-patch must be"},
		{name: "large patch", number: 10000, want: "-patch must be"},
		{name: "missing patch", number: 3, want: "patch 3 does not exist"},
		{name: "missing base", number: 1, want: "unable to read the patch base"},
		{name: "empty base", number: 1, want: "recorded patch base is empty"},
		{name: "invalid base", number: 1, want: "unable to resolve the recorded patch base"},
		{name: "unrelated base", number: 1, want: "not an ancestor of HEAD"},
		{name: "no patches", number: 1, want: "patch 1 does not exist"},
		{name: "unstaged", number: 1, want: "unstaged or untracked changes"},
		{name: "untracked", number: 1, want: "unstaged or untracked changes"},
		{name: "in progress", number: 1, want: "Git operation is already in progress"},
		{name: "merge", number: 1, want: "history containing merge commits"},
		{name: "fixup", number: 1, want: "pending fixup/squash commit"},
		{name: "squash", number: 1, want: "pending fixup/squash commit"},
		{name: "amend", number: 1, want: "pending fixup/squash commit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, git := newFixupTestRepo(t)
			_, goDir := config.FullProjectRoots()
			statusPath := config.FullPrePatchStatusFilePath()
			base, err := readStatusFile(statusPath)
			if err != nil {
				t.Fatal(err)
			}

			switch tt.name {
			case "missing base":
				if err := os.Remove(statusPath); err != nil {
					t.Fatal(err)
				}
			case "empty base", "invalid base", "no patches":
				value := ""
				switch tt.name {
				case "invalid base":
					value = "not-a-commit"
				case "no patches":
					value = git("rev-parse", "HEAD")
				}

				if err := os.WriteFile(statusPath, []byte(value), 0o644); err != nil {
					t.Fatal(err)
				}
			case "unrelated base", "merge":
				git("checkout", "-b", "other", base)
				writeFixupTestFile(t, goDir, "other.txt", "other\n")
				git("add", "--all")
				git("commit", "-m", "Other history")
				other := git("rev-parse", "HEAD")
				git("checkout", "main")
				if tt.name == "merge" {
					git("merge", "--no-ff", "-m", "Merge other history", "other")
				} else if err := os.WriteFile(statusPath, []byte(other), 0o644); err != nil {
					t.Fatal(err)
				}
			case "fixup", "squash", "amend":
				git("commit", "--allow-empty", "-m", tt.name+"! First patch")
			}

			writeFixupTestFile(t, goDir, "file1.txt", "updated\n")
			git("add", "--all")
			switch tt.name {
			case "unstaged":
				writeFixupTestFile(t, goDir, "file2.txt", "not staged\n")
			case "untracked":
				writeFixupTestFile(t, goDir, "untracked.txt", "not staged\n")
			case "in progress":
				writeFixupTestFile(t, goDir, ".git/MERGE_HEAD", git("rev-parse", "HEAD"))
			}

			head := git("rev-parse", "HEAD")
			index := git("write-tree")
			state := git("status", "--porcelain", "--untracked-files=all")
			err = fixupPatch(config, tt.number)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("fixupPatch error = %v, want %q", err, tt.want)
			}
			if got := git("rev-parse", "HEAD"); got != head {
				t.Error("failed preflight changed HEAD")
			}
			if got := git("write-tree"); got != index {
				t.Error("failed preflight changed the index")
			}
			if got := git("status", "--porcelain", "--untracked-files=all"); got != state {
				t.Error("failed preflight changed the working tree")
			}
		})
	}
}

func TestFixupPatchConflict(t *testing.T) {
	config, git := newFixupTestRepo(t)
	_, goDir := config.FullProjectRoots()
	writeFixupTestFile(t, goDir, "file1.txt", "later change\n")
	git("add", "--all")
	git("commit", "-m", "Later change")
	writeFixupTestFile(t, goDir, "file1.txt", "upgrade\n")
	git("add", "--all")
	err := fixupPatch(config, 1)
	if err == nil || !strings.Contains(err.Error(), "git rebase --continue") {
		t.Fatalf("fixupPatch error = %v, want conflict recovery instructions", err)
	}
	if inProgress, err := gitOperationInProgress(goDir); err != nil || !inProgress {
		t.Errorf("rebase in progress = %v, %v; want true, nil", inProgress, err)
	}
	if got := git("diff", "--name-only", "--diff-filter=U"); got != "file1.txt" {
		t.Errorf("unmerged paths = %q, want file1.txt", got)
	}
}

func TestPatchNumber(t *testing.T) {
	for _, tt := range []struct {
		name    string
		next    int
		subject string
		want    int
		wantErr bool
	}{
		{name: "sequential", next: 2, subject: "Subject", want: 2},
		{name: "override", next: 2, subject: commandPrefix + patchNumberCommand + "10", want: 10},
		{name: "same number", next: 2, subject: commandPrefix + patchNumberCommand + "2", want: 2},
		{name: "backwards", next: 2, subject: commandPrefix + patchNumberCommand + "1", wantErr: true},
		{name: "invalid", next: 2, subject: commandPrefix + patchNumberCommand + "no", wantErr: true},
		{name: "unknown", next: 2, subject: commandPrefix + "unknown", wantErr: true},
		{name: "too large", next: 10000, wantErr: true},
		{name: "diff ignored", next: 2, subject: "Subject\n---\n" + commandPrefix + "unknown", want: 2},
		{name: "long line", next: 2, subject: strings.Repeat("x", 70000), wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := patchNumber(tt.next, tt.subject, "test")
			if (err != nil) != tt.wantErr {
				t.Fatalf("patchNumber error = %v, want error %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("patchNumber = %d, want %d", got, tt.want)
			}
		})
	}
}

func newFixupTestRepo(t *testing.T, messages ...string) (*patch.FoundConfig, func(...string) string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	config := &patch.FoundConfig{
		RootDir: t.TempDir(),
		Config: patch.Config{
			SubmoduleDir:  "go",
			PatchesDir:    "patches",
			StatusFileDir: "status",
			PreserveIndex: true,
		},
	}
	_, goDir := config.FullProjectRoots()
	for _, dir := range []string{goDir, filepath.Join(config.RootDir, "patches"), config.FullStatusFileDir()} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	git := func(args ...string) string {
		t.Helper()
		out, err := executil.Dir(goDir, "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("commit", "--allow-empty", "-m", "Base")
	if err := os.WriteFile(config.FullPrePatchStatusFilePath(), []byte(git("rev-parse", "HEAD")), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		messages = []string{"First patch", "Second patch"}
	}

	writeFixupTestFile(t, goDir, "old.txt", "old\n")
	for i, message := range messages {
		writeFixupTestFile(t, goDir, fmt.Sprintf("file%d.txt", i+1), "original\n")
		git("add", "--all")
		git("commit", "-m", message)
	}

	return config, git
}

func writeFixupTestFile(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
