// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/microsoft/go-infra/subcmd"
)

// promptPrefix is prepended to the interactive shell prompt so it's obvious the shell is running
// inside the patched submodule and "extract" will run on exit.
const promptPrefix = "(git-go-patch) "

// envInteractive marks an interactive shell by holding the absolute path of the target submodule
// (a more reliable signal than the prompt prefix, which some prompt frameworks drop). Storing the
// path lets a nested invocation warn only on re-entry into the same submodule; since the variable is
// inherited by every descendant, a bare flag would false-positive for a shell opened on another repo.
const envInteractive = "GIT_GO_PATCH_INTERACTIVE"

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "shell",
		Summary: "Open an interactive shell in the submodule, then run 'extract' on exit.",
		Description: `

This command streamlines the common "apply, edit, extract" workflow by starting an interactive
shell with its working directory set to the submodule, so no manual "cd" is necessary.

When the shell exits successfully (when you execute "exit 0"), the "extract" subcommand runs
automatically. When the shell exits with a non-zero exit code (for example when you execute
"exit 1"), the "extract" subcommand is not run. This allows you to skip extraction.

The "extract" subcommand is also skipped in situations where it is very likely undesirable:
- If a rebase, merge, cherry-pick, or revert is still in progress.
- If the submodule has no commits on top of the recorded base.
` + repoRootSearchDescription,
		Handle: handleShell,
	})
}

func handleShell(p subcmd.ParseFunc) error {
	apply := flag.Bool("apply", false, "Run 'git go-patch apply' before opening the shell.")
	rebase := flag.Bool("rebase", false, "Run 'git go-patch rebase' (interactive rebase) before opening the shell.")
	noExtract := flag.Bool(
		"no-extract", false,
		"Don't automatically run 'git go-patch extract' when the shell exits.")
	shellFlag := flag.String(
		"shell", "",
		"Shell to launch. Defaults to $SHELL on macOS/Linux, or PowerShell (falling back to cmd.exe) on Windows.")
	allowSelfNest := flag.Bool(
		"allow-self-nest", false,
		"Open the shell even if it looks like you're already inside a 'git go-patch shell' for this submodule.")

	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	_, goDir := config.FullProjectRoots()

	// Refuse to nest a shell for the same submodule. The marker env var is inherited by every child
	// process, so this also catches re-running the command from, say, an editor terminal opened inside
	// the shell. Nesting has no real use (each exit would redundantly run 'extract'), so fail early
	// rather than warn; -allow-self-nest overrides for the rare case the user really wants it.
	if alreadyInShellFor(goDir) && !*allowSelfNest {
		return fmt.Errorf("already inside a 'git go-patch shell' for this submodule (%s is set); "+
			"exit it first, or pass -allow-self-nest to open a nested shell anyway", envInteractive)
	}

	if *apply {
		if err := applyPatches(config, false, false, ""); err != nil {
			return err
		}
	}
	if *rebase {
		if !*apply {
			// 'rebase' rebases the commits 'apply' creates and reads the HEAD it recorded. If no
			// apply state exists yet, the raw error from 'rebase' is cryptic, so hint at the cause.
			if _, err := os.Stat(config.FullPrePatchStatusFilePath()); errors.Is(err, os.ErrNotExist) {
				fmt.Println("\nNote: '-rebase' rebases the commits created by 'apply', but no apply state was found.")
				fmt.Println("Pass '-apply' as well (or run 'git go-patch apply' first) if the rebase fails to start.")
			}
		}
		if err := rebasePatches(config); err != nil {
			// An interactive rebase exits non-zero when it stops for a conflict or an "edit"/"break"
			// step. Warn, but still open the shell so the user can resolve it there.
			fmt.Printf("\nWARNING: 'git go-patch rebase' exited with an error: %v\n", err)
			fmt.Println("If it stopped for conflicts or an 'edit'/'break' step, resolve it in the shell and run 'git rebase --continue' (or 'git rebase --abort').")
		}
	}

	if alreadyInShellFor(goDir) && *allowSelfNest {
		fmt.Println("\nWARNING: opening a nested 'git go-patch shell' for this submodule because -allow-self-nest was passed; remember to 'exit' each one.")
	}

	fmt.Printf("\nStarting an interactive shell in %#q.\n", goDir)
	fmt.Println("Edit the commits in the submodule however you like; run 'code .' to open an editor scoped to its history.")
	if *noExtract {
		fmt.Println("When you're done with your changes, type 'exit' to leave the shell. 'git go-patch extract' will NOT run automatically; run it yourself when you're ready.")
	} else {
		fmt.Println("When you're done with your changes, use 'exit 0' to save them to the patch files, or 'exit 1' to discard them.")
	}
	fmt.Println()

	shellErr := runInteractiveShell(goDir, *shellFlag)

	extract, err := shouldExtractPatch(shellErr, *noExtract)
	if err != nil {
		// The shell never ran, so there's nothing to extract; surface the failure.
		return fmt.Errorf("failed to run the interactive shell: %w", err)
	}

	if !extract {
		if *noExtract {
			fmt.Println("\nSkipping 'extract' because -no-extract was specified.")
		} else {
			// The shell exited non-zero, which we treat as "discard": skip extract and leave the
			// patch files alone.
			var exitErr *exec.ExitError
			if errors.As(shellErr, &exitErr) {
				fmt.Printf("\nThe shell exited with status %d, so 'extract' was skipped and your patch files were left untouched.\n", exitErr.ExitCode())
			} else {
				fmt.Println("\nThe shell exited abnormally, so 'extract' was skipped and your patch files were left untouched.")
			}
		}
		fmt.Println("To manually save your changes, run 'git go-patch extract'.")
		return nil
	}

	// If the user left a rebase, merge, cherry-pick, or revert half-finished, the submodule's commit
	// history isn't in a meaningful state, so running 'extract' would rewrite the patch files from
	// incomplete work.
	if inProgress, err := gitOperationInProgress(goDir); err != nil {
		fmt.Printf("\nWARNING: unable to determine whether a Git operation is in progress: %v\n", err)
		fmt.Println("Skipping 'extract' to be safe. Run 'git go-patch extract' yourself once the submodule is in a clean state.")
		return nil
	} else if inProgress {
		fmt.Println("\nA rebase, merge, cherry-pick, or revert is still in progress in the submodule, so 'extract' was skipped.")
		fmt.Println("Finish or abort it, then run 'git go-patch extract' to update the patch files.")
		return nil
	}

	fmt.Println("\nShell exited cleanly. Running 'git go-patch extract'.")
	return extractPatches(config, "", false, false)
}

// alreadyInShellFor reports whether this process is already running inside a 'git go-patch shell'
// that targets the same submodule dir. Because the marker env var is inherited by every descendant
// process, comparing the target dir avoids a spurious warning when a shell is opened for a different
// repo from something launched inside the original shell (such as an editor's integrated terminal).
func alreadyInShellFor(dir string) bool {
	prev := os.Getenv(envInteractive)
	if prev == "" {
		return false
	}
	return sameDir(prev, dir)
}

// sameDir reports whether two paths refer to the same directory, comparing absolute, cleaned forms.
func sameDir(a, b string) bool {
	if absA, err := filepath.Abs(a); err == nil {
		a = absA
	}
	if absB, err := filepath.Abs(b); err == nil {
		b = absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// shouldExtractPatch reports whether 'git go-patch extract' should run after the interactive shell
// exits, given the shell's result and whether -no-extract was passed.
//
// Extraction happens only on a clean (status 0) shell exit when -no-extract was not passed. A
// non-zero exit (shellErr is an *exec.ExitError) is treated as a deliberate request to discard the
// session without rewriting the patch files. A shellErr that is not an *exec.ExitError means the
// shell failed to launch at all; it is returned as launchErr so the caller can report the failure
// rather than silently skipping extraction.
func shouldExtractPatch(shellErr error, noExtract bool) (extract bool, launchErr error) {
	var exitErr *exec.ExitError
	if shellErr != nil && !errors.As(shellErr, &exitErr) {
		return false, shellErr
	}
	if noExtract {
		return false, nil
	}
	return shellErr == nil, nil
}

// gitOperationInProgress reports whether the Git repository at dir has an in-progress rebase, merge,
// cherry-pick, or revert, which would make the commit history unsuitable for 'extract'.
func gitOperationInProgress(dir string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		cmd := exec.Command("git", "rev-parse", "--git-path", name)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("unable to run 'git rev-parse --git-path %s': %w", name, err)
		}
		p := strings.TrimSpace(string(out))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		_, err = os.Stat(p)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("unable to stat %q: %w", p, err)
		}
	}
	return false, nil
}

// runInteractiveShell starts an interactive shell with its working directory set to dir, marks the
// session via the GIT_GO_PATCH_INTERACTIVE environment variable, and blocks until the shell exits.
// shellOverride, if non-empty, names the shell to launch instead of the default.
func runInteractiveShell(dir, shellOverride string) error {
	cmd := interactiveShellCmd(shellOverride)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Mark the environment with the target submodule so scripts and a nested 'git go-patch shell'
	// can detect this mode. interactiveShellCmd may leave cmd.Env nil (meaning "inherit this
	// process's environment"); start from os.Environ() in that case so appending doesn't drop it.
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, envInteractive+"="+dir)

	// Unlike the rest of this package, run the shell directly instead of through executil.Run, which
	// would print a "Running command" banner that's noise right before an interactive prompt.
	return cmd.Run()
}

// interactiveShellCmd builds an *exec.Cmd that launches an interactive shell. If shellOverride is
// non-empty it names the shell to launch; otherwise the shell is chosen by selectShell. The prompt
// is prefixed with promptPrefix only for shells where that is non-invasive (PowerShell and cmd.exe).
// For any other shell, the GIT_GO_PATCH_INTERACTIVE environment variable and the printed banner are
// the indicators that you're in shell mode.
func interactiveShellCmd(shellOverride string) *exec.Cmd {
	shell := selectShell(shellOverride)
	switch shellKind(shell) {
	case shellKindPowerShell:
		return powerShellCmd(shell)
	case shellKindCmd:
		return cmdShellCmd(shell)
	default:
		return exec.Command(shell, "-i")
	}
}

// selectShell returns the shell to launch. An explicit override wins. Otherwise, on Windows it
// prefers PowerShell and falls back to cmd.exe; on other platforms it honors $SHELL (the user's
// configured shell) and falls back to /bin/sh.
func selectShell(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		if pwsh, err := exec.LookPath("pwsh"); err == nil {
			return pwsh
		}
		if powershell, err := exec.LookPath("powershell"); err == nil {
			return powershell
		}
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return comspec
		}
		return "cmd.exe"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

type shellKindValue int

const (
	shellKindOther shellKindValue = iota
	shellKindPowerShell
	shellKindCmd
)

// shellKind classifies shell by its base name so the launcher knows whether it can prefix the prompt
// non-invasively.
func shellKind(shell string) shellKindValue {
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(shell), ".exe")) {
	case "pwsh", "powershell":
		return shellKindPowerShell
	case "cmd":
		return shellKindCmd
	default:
		return shellKindOther
	}
}

func cmdShellCmd(comspec string) *exec.Cmd {
	cmd := exec.Command(comspec)
	// cmd.exe builds its prompt from the PROMPT environment variable. "$P$G" is the default
	// "current-path>" prompt, so prepend the indicator to it.
	cmd.Env = append(os.Environ(), "PROMPT="+promptPrefix+"$P$G")
	return cmd
}

func powerShellCmd(shell string) *exec.Cmd {
	// Wrap the existing prompt function (which loads from the user's profile) so custom prompts like
	// oh-my-posh or posh-git are preserved, just with the indicator prepended. Capturing it after the
	// profile loads means the indicator wins. Then drop into an interactive session with -NoExit.
	promptCommand := fmt.Sprintf(
		"$global:__goPatchPrompt = $function:prompt; "+
			"function global:prompt { '%s' + (& $global:__goPatchPrompt) }",
		promptPrefix)
	return exec.Command(shell, "-NoLogo", "-NoExit", "-Command", promptCommand)
}
