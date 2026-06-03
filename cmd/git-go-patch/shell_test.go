// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWriteTempRC(t *testing.T) {
	content := "echo hello\n"
	path, cleanup, err := writeTempRC("git-go-patch-test", content)
	if err != nil {
		t.Fatalf("writeTempRC returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unable to read temp file: %v", err)
	}
	if string(got) != content {
		t.Errorf("temp file content = %q, want %q", got, content)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected temp file %q to be removed after cleanup, stat err = %v", path, err)
	}
}

func TestUnixShellCmdBash(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/bash")

	cmd, cleanup, err := unixShellCmd()
	if err != nil {
		t.Fatalf("unixShellCmd returned error: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected a non-nil cleanup func for bash")
	}

	if !slices.Contains(cmd.Args, "-i") {
		t.Errorf("bash args = %v, want to contain -i", cmd.Args)
	}
	rc := argAfter(t, cmd.Args, "--rcfile")
	data, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("unable to read rcfile %q: %v", rc, err)
	}
	if !strings.Contains(string(data), promptPrefix) {
		t.Errorf("rcfile %q content = %q, want to contain prompt prefix %q", rc, data, promptPrefix)
	}
	if !strings.Contains(string(data), ".bashrc") {
		t.Errorf("rcfile %q should source the user's .bashrc", rc)
	}

	cleanup()
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Errorf("expected rcfile %q to be removed after cleanup, stat err = %v", rc, err)
	}
}

func TestUnixShellCmdZsh(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/zsh")
	t.Setenv("ZDOTDIR", "")
	t.Setenv("HOME", "/home/example")

	cmd, cleanup, err := unixShellCmd()
	if err != nil {
		t.Fatalf("unixShellCmd returned error: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected a non-nil cleanup func for zsh")
	}
	defer cleanup()

	zdot := envValue(cmd.Env, "ZDOTDIR")
	if zdot == "" {
		t.Fatal("expected ZDOTDIR to be set in the zsh command environment")
	}

	zshenv, err := os.ReadFile(filepath.Join(zdot, ".zshenv"))
	if err != nil {
		t.Fatalf("unable to read temp .zshenv: %v", err)
	}
	if !strings.Contains(string(zshenv), "/home/example/.zshenv") {
		t.Errorf(".zshenv = %q, want it to source the user's .zshenv", zshenv)
	}

	zshrc, err := os.ReadFile(filepath.Join(zdot, ".zshrc"))
	if err != nil {
		t.Fatalf("unable to read temp .zshrc: %v", err)
	}
	if !strings.Contains(string(zshrc), promptPrefix) {
		t.Errorf(".zshrc = %q, want to contain prompt prefix %q", zshrc, promptPrefix)
	}

	cleanup()
	if _, err := os.Stat(zdot); !os.IsNotExist(err) {
		t.Errorf("expected temp ZDOTDIR %q to be removed after cleanup, stat err = %v", zdot, err)
	}
}

func TestUnixShellCmdDefault(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "myprompt$ ")

	cmd, cleanup, err := unixShellCmd()
	if err != nil {
		t.Fatalf("unixShellCmd returned error: %v", err)
	}
	if cleanup != nil {
		t.Error("expected a nil cleanup func for the default shell path")
	}

	ps1 := envValue(cmd.Env, "PS1")
	if !strings.HasPrefix(ps1, promptPrefix) {
		t.Errorf("PS1 = %q, want it to start with prompt prefix %q", ps1, promptPrefix)
	}
	if !strings.Contains(ps1, "myprompt$ ") {
		t.Errorf("PS1 = %q, want it to preserve the user's existing prompt", ps1)
	}
}

func TestPowerShellCmd(t *testing.T) {
	cmd := powerShellCmd("pwsh")

	if !slices.Contains(cmd.Args, "-NoExit") || !slices.Contains(cmd.Args, "-Command") {
		t.Errorf("pwsh args = %v, want to contain -NoExit and -Command", cmd.Args)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, promptPrefix) {
		t.Errorf("pwsh command = %q, want to contain prompt prefix %q", joined, promptPrefix)
	}
	if !strings.Contains(joined, "$function:prompt") {
		t.Errorf("pwsh command = %q, want it to wrap the existing prompt function", joined)
	}
}

func TestCmdShellCmd(t *testing.T) {
	cmd := cmdShellCmd("cmd.exe")

	if len(cmd.Args) == 0 || cmd.Args[0] != "cmd.exe" {
		t.Errorf("cmd args = %v, want first element to be cmd.exe", cmd.Args)
	}
	prompt := envValue(cmd.Env, "PROMPT")
	if !strings.HasPrefix(prompt, promptPrefix) {
		t.Errorf("PROMPT = %q, want it to start with prompt prefix %q", prompt, promptPrefix)
	}
	if !strings.Contains(prompt, "$P$G") {
		t.Errorf("PROMPT = %q, want it to preserve the default $P$G prompt", prompt)
	}
}

func TestGitOperationInProgress(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not available: %v", err)
	}

	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		// Provide identity and disable global config so the test is hermetic.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")

	// A freshly initialized repo has no Git operation in progress.
	if inProgress, err := gitOperationInProgress(dir); err != nil {
		t.Fatalf("gitOperationInProgress returned error: %v", err)
	} else if inProgress {
		t.Error("expected no Git operation in progress in a clean repo")
	}

	// Simulate an in-progress rebase by creating the marker directory Git uses.
	if err := os.Mkdir(filepath.Join(dir, ".git", "rebase-merge"), 0o700); err != nil {
		t.Fatalf("unable to create fake rebase-merge dir: %v", err)
	}
	if inProgress, err := gitOperationInProgress(dir); err != nil {
		t.Fatalf("gitOperationInProgress returned error: %v", err)
	} else if !inProgress {
		t.Error("expected an in-progress rebase to be detected")
	}
	if err := os.Remove(filepath.Join(dir, ".git", "rebase-merge")); err != nil {
		t.Fatalf("unable to remove fake rebase-merge dir: %v", err)
	}

	// Simulate a stopped cherry-pick via its marker file.
	if err := os.WriteFile(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD"), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatalf("unable to create fake CHERRY_PICK_HEAD: %v", err)
	}
	if inProgress, err := gitOperationInProgress(dir); err != nil {
		t.Fatalf("gitOperationInProgress returned error: %v", err)
	} else if !inProgress {
		t.Error("expected an in-progress cherry-pick to be detected")
	}
}

// argAfter returns the element immediately following want in args, failing the test if want is not
// present or is the last element.
func argAfter(t *testing.T, args []string, want string) string {
	t.Helper()
	for i, a := range args {
		if a == want {
			if i+1 >= len(args) {
				t.Fatalf("no argument after %q in %v", want, args)
			}
			return args[i+1]
		}
	}
	t.Fatalf("argument %q not found in %v", want, args)
	return ""
}

// envValue returns the value of the last assignment of key in a KEY=VALUE environment slice, or the
// empty string if it isn't present.
func envValue(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			value = strings.TrimPrefix(e, prefix)
		}
	}
	return value
}
