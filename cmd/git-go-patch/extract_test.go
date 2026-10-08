// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/patch"
)

func TestExtractIndexThreeWay(t *testing.T) {
	tests := []struct {
		name             string
		verbatim         bool
		quotePathChanged bool
	}{
		{name: "equivalent patches"},
		{name: "verbatim", verbatim: true},
		{name: "changed filename quoting", quotePathChanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
			t.Setenv("GIT_AUTHOR_NAME", "test")
			t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
			t.Setenv("GIT_COMMITTER_NAME", "test")
			t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
			t.Setenv("GIT_GO_PATCH_SUBMODULE_REFERENCES", "")

			root := t.TempDir()
			goDir := filepath.Join(root, "go")
			patchDir := filepath.Join(root, "patches")
			for _, dir := range []string{goDir, patchDir} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			config := &patch.FoundConfig{
				RootDir: root,
				Config:  patch.Config{SubmoduleDir: "go", PatchesDir: "patches"},
			}
			filename := "file.txt"
			if tt.quotePathChanged {
				filename = "é.txt"
			}
			file := filepath.Join(goDir, filename)
			write := func(content string) {
				t.Helper()
				if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) []byte {
				t.Helper()
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return content
			}
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = goDir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v failed: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}

			const original = "old context\n01\n02\n03\n04\n05\n06\n07\n08\n09\n10\n"
			git("init", "-b", "old-base")
			git("config", "core.quotePath", "false")
			write(original)
			git("add", ".")
			git("commit", "-m", "old base")
			oldBase := git("rev-parse", "HEAD")
			oldBlob := git("rev-parse", "HEAD:"+filename)
			write(strings.Replace(original, "05\n", "PATCH1\n", 1))
			git("commit", "-am", "first", "-m", commandPrefix+patchNumberCommand+"1000")
			write(strings.NewReplacer("05\n", "PATCH1\n", "09\n", "PATCH2\n").Replace(original))
			git("commit", "-am", "second")
			if _, err := patch.FormatPatch(goDir, "-o", patchDir, oldBase); err != nil {
				t.Fatal(err)
			}
			oldPatches := make(map[string][]byte)
			if err := patch.WalkPatches(patchDir, func(path string) error {
				oldPatches[filepath.Base(path)] = read(path)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(oldPatches) != 2 {
				t.Fatalf("got %d original patches, want 2", len(oldPatches))
			}
			git("config", "core.quotePath", "true")

			// Start fresh upstream history so the obsolete patch preimages aren't reachable.
			git("checkout", "--orphan", "upstream")
			currentBase := strings.Replace(original, "old context", "new context", 1)
			write(currentBase)
			git("add", ".")
			git("commit", "-m", "current base")
			git("branch", "-D", "old-base")
			base := git("rev-parse", "HEAD")
			baseBlob := git("rev-parse", "HEAD:"+filename)
			// No sidecar yet: existing patch sets must still apply.
			if err := patch.Apply(config, patch.ApplyModeCommits); err != nil {
				t.Fatal(err)
			}
			// Regeneration must remove obsolete entries, including renamed patches.
			if err := os.WriteFile(filepath.Join(patchDir, patch.IndexFileName), []byte(`{"obsolete.patch":{}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := extractPatches(config, base, tt.verbatim, false); err != nil {
				t.Fatal(err)
			}
			index, err := patch.ReadIndex(patchDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(index) != 2 {
				t.Fatalf("got %d index entries, want 2", len(index))
			}
			first := filepath.Join(patchDir, "1000-first.patch")
			second := filepath.Join(patchDir, "1001-second.patch")
			for i, path := range []string{first, second} {
				saved := read(path)
				unchanged := bytes.Equal(saved, oldPatches[[]string{"0001-first.patch", "0002-second.patch"}[i]])
				if unchanged != (!tt.verbatim && !tt.quotePathChanged) {
					t.Errorf("patch %q unchanged = %v, want %v", path, unchanged, !tt.verbatim && !tt.quotePathChanged)
				}
				restored, err := index.Restore(filepath.Base(path), saved)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 && !strings.Contains(string(restored), "index "+baseBlob+"..") {
					t.Errorf("index does not restore the current base blob:\n%s", restored)
				}
			}

			git("reset", "--hard", base)
			if err := patch.Apply(config, patch.ApplyModeIndex); err != nil {
				t.Fatal(err)
			}
			want := strings.NewReplacer("05\n", "PATCH1\n", "09\n", "PATCH2\n").Replace(currentBase)
			if got := string(read(file)); got != want {
				t.Errorf("index apply content = %q, want %q", got, want)
			}
			if got := git("rev-parse", "HEAD"); got != base {
				t.Errorf("index apply moved HEAD to %q", got)
			}

			git("reset", "--hard", base)
			write(strings.Replace(currentBase, "05\n", "UPSTREAM\n", 1))
			git("commit", "-am", "conflicting upstream update")
			git("reflog", "expire", "--expire=now", "--all")
			git("gc", "--prune=now")
			cmd := exec.Command("git", "cat-file", "-e", oldBlob)
			cmd.Dir = goDir
			if err := cmd.Run(); err == nil {
				t.Fatal("obsolete preimage blob is still available; regression would be masked")
			}
			if !tt.verbatim && !tt.quotePathChanged {
				indexPath := filepath.Join(patchDir, patch.IndexFileName)
				indexContent := read(indexPath)
				if err := os.Remove(indexPath); err != nil {
					t.Fatal(err)
				}
				if err := patch.Apply(config, patch.ApplyModeCommits); err == nil {
					t.Fatal("unmodified patch unexpectedly applied")
				}
				cmd = exec.Command("git", "am", "--retry", "-3")
				cmd.Dir = goDir
				out, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(out), "could not build fake ancestor") {
					t.Fatalf("missing index did not reproduce fake ancestor failure: %v\n%s", err, out)
				}
				git("am", "--abort")
				if err := os.WriteFile(indexPath, indexContent, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			firstBefore, secondBefore := read(first), read(second)
			if err := patch.Apply(config, patch.ApplyModeCommits); err == nil {
				t.Fatal("apply unexpectedly succeeded on conflicting upstream content")
			}
			cmd = exec.Command("git", "am", "--retry", "-3")
			cmd.Dir = goDir
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "CONFLICT") {
				t.Fatalf("git am -3 did not produce an ordinary conflict: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "could not build fake ancestor") {
				t.Fatalf("git am -3 could not build its ancestor:\n%s", out)
			}
			if !strings.Contains(string(read(file)), "<<<<<<<") {
				t.Fatal("git am -3 did not leave conflict markers")
			}
			write(strings.Replace(currentBase, "05\n", "PATCH1\n", 1))
			git("add", ".")
			// The temporary patch copies have been deleted, but am must retain the whole queue.
			git("am", "--continue")
			if got := string(read(file)); got != want {
				t.Errorf("resolved patch series = %q, want %q", got, want)
			}
			if !bytes.Equal(read(first), firstBefore) || !bytes.Equal(read(second), secondBefore) {
				t.Error("apply modified tracked patch files")
			}
		})
	}
}

func TestSubmoduleHasPatchCommits(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not available: %v", err)
	}

	dir := t.TempDir()
	gitEnv := append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	headSHA := func() string {
		t.Helper()
		cmd := exec.Command(git, "rev-parse", "HEAD")
		cmd.Dir = dir
		cmd.Env = gitEnv
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git rev-parse HEAD failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("commit", "--allow-empty", "-m", "base")
	base := headSHA()

	// base == HEAD: no commits in base..HEAD, so extract would have nothing to format.
	if has, err := submoduleHasPatchCommits(dir, base); err != nil {
		t.Fatalf("submoduleHasPatchCommits returned error: %v", err)
	} else if has {
		t.Error("expected no patch commits when HEAD is the base")
	}

	// A commit on top of the base: now there is a commit to extract.
	runGit("commit", "--allow-empty", "-m", "patch 1")
	if has, err := submoduleHasPatchCommits(dir, base); err != nil {
		t.Fatalf("submoduleHasPatchCommits returned error: %v", err)
	} else if !has {
		t.Error("expected a patch commit to be detected after committing on top of the base")
	}

	// An unknown base ref should surface an error rather than silently reporting no commits.
	if _, err := submoduleHasPatchCommits(dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
		t.Error("expected an error for an unknown base ref")
	}
}
