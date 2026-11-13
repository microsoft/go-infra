// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "fixup",
		Summary: "Fix up staged changes into an existing patch without opening an editor.",
		Description: `

Select the patch with -patch N, where N is its extracted filename's number. fixup numbers the
submodule's commits after the base recorded by 'apply', oldest first, using the same numbering rules
as 'extract': numbering starts at 1, and a numbering override in a commit's body sets that commit's
number and advances the numbering for subsequent commits. The commit that ends up assigned N is the
fixup target. fixup doesn't check if the commit message or contents match the patch being fixed up.

Stage all intended changes in the submodule first, including new and deleted files. Unstaged or
untracked changes, an in-progress Git operation, merge commits, and pending fixup/squash commits
are rejected. If nothing is staged, the command validates the patch selection but does not commit
or rebase.

The command creates a fixup commit and autosquashes it non-interactively. On conflicts it leaves
the rebase in progress: resolve them in the submodule and run 'git rebase --continue', or run
'git rebase --abort'. Patch files are not changed; run 'git go-patch extract' after success.
` + repoRootSearchDescription,
		Handle: handleFixup,
	})
}

func handleFixup(p subcmd.ParseFunc) error {
	number := flag.Int("patch", 0, "Number of the patch to fix up (required).")
	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}

	return fixupPatch(config, *number)
}

func fixupPatch(config *patch.FoundConfig, number int) error {
	if number < 1 || number > 9999 {
		return fmt.Errorf("-patch must be between 1 and 9999")
	}

	_, goDir := config.FullProjectRoots()
	inProgress, err := gitOperationInProgress(goDir)
	if err != nil {
		return err
	}
	if inProgress {
		return fmt.Errorf("a Git operation is already in progress in %#q; finish or abort it before fixing up a patch", goDir)
	}

	since, err := readStatusFile(config.FullPrePatchStatusFilePath())
	if err != nil {
		return fmt.Errorf("unable to read the patch base; run 'git go-patch apply' first: %w", err)
	}
	if since == "" {
		return fmt.Errorf("the recorded patch base is empty; run 'git go-patch apply' first")
	}

	out, err := executil.Output(executil.Dir(goDir, "git", "rev-parse", "--verify", "--end-of-options", since+"^{commit}"))
	if err != nil {
		return fmt.Errorf("unable to resolve the recorded patch base: %w", err)
	}

	since = strings.TrimSpace(out)
	if _, err := executil.Output(executil.Dir(goDir, "git", "merge-base", "--is-ancestor", since, "HEAD")); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("recorded patch base %s is not an ancestor of HEAD; apply the patches before fixing them up", since)
		}

		return fmt.Errorf("unable to validate the recorded patch base: %w", err)
	}

	target, err := findPatchCommit(goDir, since, number)
	if err != nil {
		return err
	}

	unstaged, err := executil.Output(executil.Dir(goDir, "git", "diff", "--name-only"))
	if err != nil {
		return fmt.Errorf("unable to check unstaged changes: %w", err)
	}

	untracked, err := executil.Output(executil.Dir(goDir, "git", "ls-files", "--others", "--exclude-standard"))
	if err != nil {
		return fmt.Errorf("unable to check untracked files: %w", err)
	}
	if unstaged != "" || untracked != "" {
		return fmt.Errorf("submodule has unstaged or untracked changes; stage or stash them before fixing up a patch")
	}

	staged, err := executil.Output(executil.Dir(goDir, "git", "diff", "--cached", "--name-only"))
	if err != nil {
		return fmt.Errorf("unable to check staged changes: %w", err)
	}
	if staged == "" {
		fmt.Println("No staged changes to fix up.")
		return nil
	}

	// A hash keeps autosquash unambiguous when multiple patches have the same subject.
	if err := executil.Run(executil.Dir(goDir, "git", "commit", "-m", "fixup! "+target)); err != nil {
		return fmt.Errorf("unable to commit staged changes: %w", err)
	}

	cmd := executil.Dir(goDir, "git", "rebase", "--interactive", "--autosquash", "--no-autostash", since)
	cmd.Env = append(cmd.Environ(), "GIT_SEQUENCE_EDITOR=true")
	if err := executil.Run(cmd); err != nil {
		return fmt.Errorf("fixup rebase failed in %#q; resolve conflicts and run 'git rebase --continue', or run 'git rebase --abort': %w", goDir, err)
	}

	return nil
}

func findPatchCommit(goDir, since string, number int) (string, error) {
	out, err := executil.Output(executil.Dir(goDir, "git", "log", "--no-show-signature", "--reverse", "--format=%H%x00%P%x00%s%x00%b%x00", since+"..HEAD"))
	if err != nil {
		return "", fmt.Errorf("unable to read the patch commits: %w", err)
	}

	fields := strings.Split(out, "\x00")
	if len(fields)%4 != 1 {
		return "", fmt.Errorf("unexpected patch commit log format")
	}

	n := 1
	var target string
	for i := 0; i+3 < len(fields); i += 4 {
		commit := strings.TrimSpace(fields[i])
		if len(strings.Fields(fields[i+1])) > 1 {
			return "", fmt.Errorf("cannot fix up a patch in a history containing merge commits (%s)", commit)
		}

		subject := fields[i+2]
		if strings.HasPrefix(subject, "fixup! ") || strings.HasPrefix(subject, "squash! ") || strings.HasPrefix(subject, "amend! ") {
			return "", fmt.Errorf("patch history contains a pending fixup/squash commit (%s); finish rebasing it first", commit)
		}

		// format-patch folds the subject into a prefixed header, so commands belong in the body.
		n, err = patchNumber(n, fields[i+3], commit)
		if err != nil {
			return "", err
		}
		if n == number {
			target = commit
		}

		n++
	}
	if target == "" {
		return "", fmt.Errorf("patch %d does not exist in %s..HEAD; apply the patches as commits first", number, since)
	}

	return target, nil
}
