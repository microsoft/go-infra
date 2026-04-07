// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAmIgnoresThreeWayConfig verifies that Am applies patches without three-way merge, even when
// the user has am.threeWay=true configured. This prevents a mismatch between local behavior and CI,
// where am.threeWay is not set. See https://github.com/microsoft/go/issues/1233.
func TestAmIgnoresThreeWayConfig(t *testing.T) {
	t.Parallel()

	// Set up a repo with a base commit containing a file.
	repoDir := t.TempDir()
	mustRun(t, repoDir, "git", "init")
	mustRun(t, repoDir, "git", "config", "user.email", "test@test.com")
	mustRun(t, repoDir, "git", "config", "user.name", "Test")

	// Create a file with distinctive content so the patch has specific context lines.
	original := "aaa\nbbb\nccc\nddd\neee\nfff\nggg\nhhh\niii\njjj\n"
	writeFile(t, filepath.Join(repoDir, "file.txt"), original)
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "initial")

	// Make a change in the middle of the file and generate a patch from it.
	modified := "aaa\nbbb\nccc\nddd\nEEE\nfff\nggg\nhhh\niii\njjj\n"
	writeFile(t, filepath.Join(repoDir, "file.txt"), modified)
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "modify middle")

	patchDir := t.TempDir()
	mustRun(t, repoDir, "git", "format-patch", "-1", "--zero-commit", "-o", patchDir)

	// Create a diverged branch from the base commit that changes the context lines around the
	// patched area but doesn't touch the actual changed line. This means:
	// - Without three-way: the patch context lines ("aaa", "bbb", etc.) don't match the new
	//   content, so git am fails.
	// - With three-way: git reconstructs the original file using blob hashes from the patch index
	//   line, does a three-way merge, and auto-resolves because the changes don't conflict.
	mustRun(t, repoDir, "git", "checkout", "-b", "diverged", "HEAD~1")
	diverged := "AAA\nBBB\nccc\nddd\neee\nfff\nggg\nHHH\nIII\nJJJ\n"
	writeFile(t, filepath.Join(repoDir, "file.txt"), diverged)
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "change context lines")

	// Enable am.threeWay in the repo config to simulate a user who has this set.
	mustRun(t, repoDir, "git", "config", "am.threeWay", "true")

	// Find the patch file.
	entries, err := os.ReadDir(patchDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 patch file, got %d", len(entries))
	}
	patchFile, err := filepath.Abs(filepath.Join(patchDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	// Verify that Am rejects the patch (because it disables three-way merge).
	err = Am(repoDir, "-q", patchFile)
	if err == nil {
		t.Fatal("Am should have failed to apply the patch without three-way merge, but it succeeded")
	}
	// Clean up the failed am state.
	_ = Run(repoDir, "am", "--abort")

	// Verify that raw "git am" with the repo's am.threeWay=true would succeed.
	// This confirms the patch is the kind that only works with three-way merge.
	cmd := exec.Command("git", "am", "-q", patchFile)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("raw 'git am' with am.threeWay=true should have succeeded, but got: %v", err)
	}
}

// TestAmAppliesCleanPatch verifies that Am works normally for patches that apply cleanly.
func TestAmAppliesCleanPatch(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	mustRun(t, repoDir, "git", "init")
	mustRun(t, repoDir, "git", "config", "user.email", "test@test.com")
	mustRun(t, repoDir, "git", "config", "user.name", "Test")

	writeFile(t, filepath.Join(repoDir, "file.txt"), "hello\n")
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "initial")

	writeFile(t, filepath.Join(repoDir, "file.txt"), "hello\nworld\n")
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "add world")

	patchDir := t.TempDir()
	mustRun(t, repoDir, "git", "format-patch", "-1", "--zero-commit", "-o", patchDir)

	mustRun(t, repoDir, "git", "reset", "--hard", "HEAD~1")

	entries, err := os.ReadDir(patchDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("git format-patch produced no patch files")
	}
	patchFile, err := filepath.Abs(filepath.Join(patchDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	if err := Am(repoDir, "-q", patchFile); err != nil {
		t.Fatalf("Am should apply a clean patch successfully, but got: %v", err)
	}
}

// TestAmAppliesCleanPatchWithThreeWayEnabled verifies that Am works for clean patches even when the
// user has am.threeWay=true configured — it shouldn't interfere with clean applies.
func TestAmAppliesCleanPatchWithThreeWayEnabled(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	mustRun(t, repoDir, "git", "init")
	mustRun(t, repoDir, "git", "config", "user.email", "test@test.com")
	mustRun(t, repoDir, "git", "config", "user.name", "Test")
	mustRun(t, repoDir, "git", "config", "am.threeWay", "true")

	writeFile(t, filepath.Join(repoDir, "file.txt"), "hello\n")
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "initial")

	writeFile(t, filepath.Join(repoDir, "file.txt"), "hello\nworld\n")
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "-m", "add world")

	patchDir := t.TempDir()
	mustRun(t, repoDir, "git", "format-patch", "-1", "--zero-commit", "-o", patchDir)

	mustRun(t, repoDir, "git", "reset", "--hard", "HEAD~1")

	entries, err := os.ReadDir(patchDir)
	if err != nil {
		t.Fatal(err)
	}
	patchFile, err := filepath.Abs(filepath.Join(patchDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}

	if err := Am(repoDir, "-q", patchFile); err != nil {
		t.Fatalf("Am should apply a clean patch successfully even with am.threeWay=true configured, but got: %v", err)
	}
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", cmd.Args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
