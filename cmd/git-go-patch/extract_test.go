// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

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
