// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/microsoft/go-infra/executil"
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
shell with its working directory set to the submodule, so no manual "cd" is necessary. The shell
prompt is prefixed with "` + strings.TrimSpace(promptPrefix) + `" to make it clear the shell is in
this special mode. When you exit the shell (for example by running "exit"), "git go-patch extract"
runs automatically to rewrite the patch files based on the commits in the submodule.

Inside the shell you can edit commits however you like, for example with "git rebase -i", and you
can run "code ." to open an editor scoped to the submodule's history.

Use "-apply" to run "git go-patch apply" before opening the shell, and "-rebase" to start an
interactive rebase. Both can be combined, in which case "apply" runs first. The "-rebase" rebase
runs to completion before the shell opens; if it stops (for example on a conflict or an "edit" or
"break" step) the shell still opens so you can resolve it and run "git rebase --continue". Use
"-no-extract" if you want to run "extract" yourself instead of automatically on exit.

When you exit, "extract" runs automatically only if the shell exits with status 0. Exit with a
non-zero status (for example "exit 1") to leave without rewriting the patch files, which is useful
if you've made a mess of the history and want to bail out. In PowerShell a plain "exit" always
reports status 0; in bash or zsh a plain "exit" inherits the status of the last command you ran.

If a rebase, merge, cherry-pick, or revert is still in progress when you exit the shell, "extract"
is skipped to avoid rewriting the patch files from an incomplete state.
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

	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	_, goDir := config.FullProjectRoots()

	if *apply {
		if err := runSelf(goDir, "apply"); err != nil {
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
		if err := runSelf(goDir, "rebase"); err != nil {
			// An interactive rebase exits non-zero when it stops for a conflict or an "edit"/"break"
			// step. Warn, but still open the shell so the user can resolve it there.
			fmt.Printf("\nWARNING: 'git go-patch rebase' exited with an error: %v\n", err)
			fmt.Println("If it stopped for conflicts or an 'edit'/'break' step, resolve it in the shell and run 'git rebase --continue' (or 'git rebase --abort').")
		}
	}

	if alreadyInShellFor(goDir) {
		fmt.Println("\nWARNING: it looks like you're already inside a 'git go-patch shell' for this submodule (" + envInteractive + " is set).")
		fmt.Println("Opening another one will nest shells; remember to 'exit' each one.")
	}

	fmt.Printf("\nStarting an interactive shell in %#q.\n", goDir)
	if *noExtract {
		fmt.Println("Type 'exit' to leave the shell. 'git go-patch extract' will NOT run automatically; run it yourself when you're ready.")
	} else {
		fmt.Println("Exit with status 0 ('exit 0', or just 'exit' in PowerShell) to save: 'git go-patch extract' runs automatically.")
		fmt.Println("Exit with a non-zero status ('exit 1') to leave without saving, so your patch files stay untouched.")
	}
	fmt.Println()

	shellErr := runInteractiveShell(goDir)

	extract, launchErr := shouldExtractPatch(shellErr, *noExtract)
	if launchErr != nil {
		// The shell never ran, so there's nothing to extract; surface the failure.
		return fmt.Errorf("failed to run the interactive shell: %w", launchErr)
	}

	if !extract {
		if *noExtract {
			fmt.Println("\nSkipping 'extract' because -no-extract was specified.")
		} else {
			// The shell exited non-zero, which we treat as "discard": skip extract and leave the
			// patch files alone. Tell the user how to extract manually in case they meant to save.
			var exitErr *exec.ExitError
			if errors.As(shellErr, &exitErr) {
				fmt.Printf("\nThe shell exited with status %d, so 'extract' was skipped and your patch files were left untouched.\n", exitErr.ExitCode())
			} else {
				fmt.Println("\nThe shell exited abnormally, so 'extract' was skipped and your patch files were left untouched.")
			}
			fmt.Println("If you meant to save your changes, run 'git go-patch extract'.")
		}
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
	return runSelf(goDir, "extract")
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
		switch _, err := os.Stat(p); {
		case err == nil:
			return true, nil
		case !os.IsNotExist(err):
			return false, fmt.Errorf("unable to stat %q: %w", p, err)
		}
	}
	return false, nil
}

// runSelf runs this same git-go-patch executable with the given subcommand, in the given working
// directory, inheriting the current stdio. It forwards the "-c" repo root flag if it was set, so the
// child command targets the same repository. The working directory is set to the submodule so
// subcommands like "rebase" don't warn that the shell is being run from outside the submodule.
func runSelf(dir, subcommand string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("unable to locate the git-go-patch executable: %w", err)
	}

	args := []string{subcommand}
	if *repoRootFlag != "" {
		// Resolve the repo root to an absolute path before forwarding it. The child runs with its
		// working directory set to the submodule (cmd.Dir below), so a relative "-c" would resolve
		// against the wrong directory. filepath.Abs resolves against this process's working
		// directory, which is where the user originally passed "-c".
		root := *repoRootFlag
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		args = append(args, "-c", root)
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	return executil.Run(cmd)
}

// runInteractiveShell starts an interactive shell with its working directory set to dir and the
// prompt prefixed with promptPrefix. It blocks until the shell exits.
func runInteractiveShell(dir string) error {
	cmd, cleanup, err := interactiveShellCmd()
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Mark the environment with the target submodule so scripts and a nested 'git go-patch shell' can
	// detect this mode. The shell builders leave cmd.Env nil when they don't need to customize it,
	// which means "inherit this process's environment"; start from os.Environ() in that case so
	// appending doesn't drop it.
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, envInteractive+"="+dir)

	// Unlike the rest of this package, run the shell directly instead of through executil.Run, which
	// would print a "Running command" banner that's noise right before an interactive prompt.
	return cmd.Run()
}

// interactiveShellCmd builds an *exec.Cmd that launches the user's interactive shell with a prompt
// that indicates git-go-patch mode. It returns an optional cleanup func to remove any temporary
// files created to customize the prompt.
func interactiveShellCmd() (cmd *exec.Cmd, cleanup func(), err error) {
	if runtime.GOOS == "windows" {
		return windowsShellCmd()
	}
	return unixShellCmd()
}

func windowsShellCmd() (*exec.Cmd, func(), error) {
	// Prefer PowerShell, which is the common shell for Microsoft build of Go development. Fall back
	// to cmd.exe if PowerShell isn't available.
	if pwsh, err := exec.LookPath("pwsh"); err == nil {
		return powerShellCmd(pwsh), nil, nil
	}
	if powershell, err := exec.LookPath("powershell"); err == nil {
		return powerShellCmd(powershell), nil, nil
	}

	comspec := os.Getenv("COMSPEC")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	return cmdShellCmd(comspec), nil, nil
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

func unixShellCmd() (*exec.Cmd, func(), error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	switch filepath.Base(shell) {
	case "bash":
		return bashShellCmd(shell)
	case "zsh":
		return zshShellCmd(shell)
	default:
		// For sh, dash, and other shells, the PS1 environment variable is honored by the
		// interactive shell, so prepend the indicator to whatever PS1 is already set.
		cmd := exec.Command(shell, "-i")
		ps1 := os.Getenv("PS1")
		if ps1 == "" {
			ps1 = "$ "
		}
		cmd.Env = append(os.Environ(), "PS1="+promptPrefix+ps1)
		return cmd, nil, nil
	}
}

func bashShellCmd(shell string) (*exec.Cmd, func(), error) {
	// bash interactive shells set PS1 from their startup files, overriding any inherited value. Use
	// a temporary init file that sources the user's normal config first, then prepends the
	// indicator to the resulting prompt.
	rcContent := `[ -f "$HOME/.bashrc" ] && source "$HOME/.bashrc"
PS1="` + promptPrefix + `$PS1"
`
	rcFile, cleanup, err := writeTempRC("git-go-patch-bashrc", rcContent)
	if err != nil {
		return nil, nil, err
	}
	return exec.Command(shell, "--rcfile", rcFile, "-i"), cleanup, nil
}

func zshShellCmd(shell string) (*exec.Cmd, func(), error) {
	// zsh reads .zshrc from ZDOTDIR. Point ZDOTDIR at a temporary directory whose .zshrc sources
	// the user's normal config and then prepends the indicator to the prompt.
	tmpDir, err := os.MkdirTemp("", "git-go-patch-zdotdir-*")
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create temp ZDOTDIR: %w", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			fmt.Printf("WARNING: unable to clean up temp dir %#q: %v\n", tmpDir, err)
		}
	}

	// Because ZDOTDIR is overridden below, zsh resolves all of its startup files from the temp dir.
	// Recreate the ones a non-login interactive shell uses so the user's environment still loads:
	// .zshenv (always sourced, commonly sets PATH and other critical env) and .zshrc.
	origZDotDir := os.Getenv("ZDOTDIR")
	if origZDotDir == "" {
		origZDotDir = os.Getenv("HOME")
	}

	files := map[string]string{
		// Source the user's .zshenv, then re-assert ZDOTDIR in case the user's config changed it, so
		// the temp .zshrc below is still picked up. Paths are single-quoted so a home or temp dir
		// containing spaces or shell metacharacters can't break sourcing.
		".zshenv": fmt.Sprintf(`[ -f %[1]s/.zshenv ] && source %[1]s/.zshenv
export ZDOTDIR=%[2]s
`, shellSingleQuote(origZDotDir), shellSingleQuote(tmpDir)),
		".zshrc": fmt.Sprintf(`[ -f %[1]s/.zshrc ] && source %[1]s/.zshrc
PS1="%[2]s$PS1"
`, shellSingleQuote(origZDotDir), promptPrefix),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o600); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("unable to write temp %s: %w", name, err)
		}
	}

	cmd := exec.Command(shell, "-i")
	cmd.Env = append(os.Environ(), "ZDOTDIR="+tmpDir)
	return cmd, cleanup, nil
}

// shellSingleQuote quotes s so a POSIX-compatible shell treats it as a single literal word, even if
// it contains spaces, quotes, "$", or backticks. It wraps the string in single quotes and escapes
// any embedded single quote as '\”.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeTempRC writes content to a uniquely named temporary file and returns its path along with a
// cleanup func that removes it.
func writeTempRC(pattern, content string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern+"-*")
	if err != nil {
		return "", nil, fmt.Errorf("unable to create temp shell init file: %w", err)
	}
	cleanup := func() {
		if err := os.Remove(f.Name()); err != nil {
			fmt.Printf("WARNING: unable to clean up temp file %#q: %v\n", f.Name(), err)
		}
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", nil, fmt.Errorf("unable to write temp shell init file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("unable to close temp shell init file: %w", err)
	}
	return f.Name(), cleanup, nil
}
