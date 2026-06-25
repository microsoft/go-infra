// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestSelectShell(t *testing.T) {
	// An explicit override always wins, regardless of platform.
	if got := selectShell("/custom/shell"); got != "/custom/shell" {
		t.Errorf("selectShell(override) = %q, want /custom/shell", got)
	}

	if runtime.GOOS != "windows" {
		// On non-Windows, the default honors $SHELL, falling back to /bin/sh when it's unset.
		t.Setenv("SHELL", "/usr/bin/fish")
		if got := selectShell(""); got != "/usr/bin/fish" {
			t.Errorf("selectShell(\"\") = %q, want /usr/bin/fish from $SHELL", got)
		}
		t.Setenv("SHELL", "")
		if got := selectShell(""); got != "/bin/sh" {
			t.Errorf("selectShell(\"\") = %q, want /bin/sh fallback", got)
		}
	}
}

func TestShellKind(t *testing.T) {
	tests := []struct {
		shell string
		want  shellKind
	}{
		{"pwsh", shellKindPowerShell},
		{"/usr/bin/pwsh", shellKindPowerShell},
		{"pwsh.exe", shellKindPowerShell},
		{"powershell", shellKindPowerShell},
		{"powershell.exe", shellKindPowerShell},
		{"cmd", shellKindCmd},
		{"cmd.exe", shellKindCmd},
		{"/bin/bash", shellKindOther},
		{"/usr/bin/zsh", shellKindOther},
		{"/bin/sh", shellKindOther},
	}
	for _, tt := range tests {
		if got := parseShellBaseName(tt.shell); got != tt.want {
			t.Errorf("parseShellBaseName(%q) = %v, want %v", tt.shell, got, tt.want)
		}
	}
}

func TestInteractiveShellCmdOther(t *testing.T) {
	// A non-PowerShell, non-cmd shell is launched interactively with no prompt munging: the command
	// is just the shell with -i, and the builder injects no prompt-related environment.
	cmd := interactiveShellCmd("/usr/bin/bash")
	if len(cmd.Args) != 2 || cmd.Args[0] != "/usr/bin/bash" || cmd.Args[1] != "-i" {
		t.Errorf("args = %v, want [/usr/bin/bash -i]", cmd.Args)
	}
	if cmd.Env != nil {
		t.Errorf("expected no custom env for a plain shell, got %v", cmd.Env)
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

func TestAlreadyInShellFor(t *testing.T) {
	dir := t.TempDir()

	// No marker set: not in a shell.
	t.Setenv(envInteractive, "")
	if alreadyInShellFor(dir) {
		t.Error("alreadyInShellFor = true with no marker set, want false")
	}

	// Marker set to the same dir: in a shell for this submodule.
	t.Setenv(envInteractive, dir)
	if !alreadyInShellFor(dir) {
		t.Error("alreadyInShellFor = false for the same dir, want true")
	}

	// A relative path to the same dir should still match, since paths are compared in absolute form.
	if rel, err := filepath.Rel(mustGetwd(t), dir); err == nil {
		if !alreadyInShellFor(rel) {
			t.Errorf("alreadyInShellFor(%q) = false, want true for a relative path to the same dir", rel)
		}
	}

	// Marker set to a different dir: not in a shell for this submodule (avoids the inherited-env
	// false positive when a shell is opened for another repo).
	t.Setenv(envInteractive, filepath.Join(dir, "other"))
	if alreadyInShellFor(dir) {
		t.Error("alreadyInShellFor = true for a different dir, want false")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd returned error: %v", err)
	}
	return wd
}

func TestShouldExtractPatch(t *testing.T) {
	// shouldExtractPatch only inspects the error's type (via errors.As), not its exit code, so a
	// zero-value *exec.ExitError stands in for "the shell ran and exited non-zero".
	nonZeroExit := &exec.ExitError{ProcessState: &os.ProcessState{}}
	launchFailure := errors.New(`exec: "sh": executable file not found in $PATH`)

	tests := []struct {
		name          string
		shellErr      error
		noExtract     bool
		wantExtract   bool
		wantLaunchErr bool
	}{
		{"clean exit saves", nil, false, true, false},
		{"clean exit with -no-extract skips", nil, true, false, false},
		{"non-zero exit discards", nonZeroExit, false, false, false},
		{"non-zero exit with -no-extract skips", nonZeroExit, true, false, false},
		{"launch failure surfaces error", launchFailure, false, false, true},
		{"launch failure surfaces error even with -no-extract", launchFailure, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotExtract, gotLaunchErr := shouldExtractPatch(tt.shellErr, tt.noExtract)
			if gotExtract != tt.wantExtract {
				t.Errorf("extract = %v, want %v", gotExtract, tt.wantExtract)
			}
			if (gotLaunchErr != nil) != tt.wantLaunchErr {
				t.Errorf("launchErr = %v, want error present = %v", gotLaunchErr, tt.wantLaunchErr)
			}
			// When a launch error is reported, it must be the original error so the caller can wrap it.
			if tt.wantLaunchErr && !errors.Is(gotLaunchErr, tt.shellErr) {
				t.Errorf("launchErr = %v, want it to be %v", gotLaunchErr, tt.shellErr)
			}
		})
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
