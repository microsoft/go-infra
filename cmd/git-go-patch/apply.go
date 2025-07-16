// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
	"github.com/microsoft/go-infra/submodule"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "apply",
		Summary: "Apply patch files as one commit each using 'git apply' on top of HEAD.",
		Description: `

This command also records the state of the repository before applying patches, so "extract" can be used
later to create patch files after adding more commits, or altering the patch commits.

apply uses "git am", internally. If patches fail to apply, use "git am" inside the submodule to
resolve and continue the patch application process.

To review patch changes, use "-before" and "-after" flags to set up state within the submodule to
prepare it for the "git go-patch stage-diff" command.
` + repoRootSearchDescription,
		Handle: handleApply,
	})
}

func handleApply(p subcmd.ParseFunc) error {
	force := flag.Bool("f", false, "Force reapply: throw away changes in the submodule.")
	noRefresh := flag.Bool(
		"no-refresh",
		false,
		"Skip the submodule refresh (reset, clean, checkout) that happens before applying patches.\n"+
			"This may be useful for advanced workflows.")

	b := flag.String(
		"b", "",
		"After applying patches, create or reset the named branch in the submodule and check it out.")
	before := flag.Bool("before", false, "Applies '-b "+stageDiffBeforeBranch+"' for use with stage-diff subcommand.")
	after := flag.Bool("after", false, "Applies '-b "+stageDiffAfterBranch+"' for use with stage-diff subcommand.")

	if err := p(); err != nil {
		return err
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	rootDir, goDir := config.FullProjectRoots()

	// If we're being careful, abort if the submodule commit isn't what we expect.
	if !*force {
		if err := ensureSubmoduleCommitNotDirty(config); err != nil {
			return err
		}
	}

	if !*noRefresh {
		if err := submodule.Reset(rootDir, goDir, *force); err != nil {
			return err
		}
	}

	if *before {
		*b = stageDiffBeforeBranch
	}
	if *after {
		*b = stageDiffAfterBranch
	}

	prePatchHead, err := getCurrentCommit(goDir)
	if err != nil {
		return err
	}

	// Record the pre-patch commit. We must do this before applying the patch: if patching
	// fails, the user needs to be able to fix up the patches inside the submodule and then run
	// "git go-patch extract" to apply the fixes to the patch files. "extract" depends on the
	// pre-patch status file. Start by ensuring the dir exists, then write the file.
	if err := os.MkdirAll(config.FullStatusFileDir(), os.ModePerm); err != nil {
		return err
	}

	if err := writeStatusFiles(prePatchHead, config.FullPrePatchStatusFilePath()); err != nil {
		return err
	}

	if err := patch.Apply(config, patch.ApplyModeCommits); err != nil {
		return err
	}

	postPatchHead, err := getCurrentCommit(goDir)
	if err != nil {
		return err
	}

	if *b != "" {
		if err := createBranch(goDir, *b); err != nil {
			return err
		}
	}

	// Record the post-patch commit.
	return writeStatusFiles(postPatchHead, config.FullPostPatchStatusFilePath())
}

func writeStatusFiles(commit string, file string) error {
	// Point out where the status file is located. Don't use %q because it would turn Windows "\" to
	// "\\", making the path harder to paste and use elsewhere.
	fmt.Printf("Writing commit to '%v' for use by 'extract' later: %v\n", file, commit)
	return os.WriteFile(file, []byte(commit), os.ModePerm)
}

func createBranch(goDir, branch string) error {
	cmd := exec.Command("git", "checkout", "-B", branch)
	cmd.Dir = goDir
	return executil.Run(cmd)
}

func getCurrentCommit(goDir string) (string, error) {
	currentCmd := exec.Command("git", "rev-parse", "HEAD")
	currentCmd.Dir = goDir
	return executil.SpaceTrimmedCombinedOutput(currentCmd)
}

func getTargetSubmoduleCommit(rootDir, submoduleDir string) (string, error) {
	return gitcmd.GetSubmoduleCommitAtRev(rootDir, submoduleDir, "HEAD")
}

func ensureSubmoduleCommitNotDirty(config *patch.FoundConfig) error {
	rootDir, goDir := config.FullProjectRoots()
	// Get the submodule commit before running any operations. If the submodule isn't
	// initialized, Git finds the root repo and gives us its commit, instead.
	preResetCommit, err := getCurrentCommit(goDir)
	if err != nil {
		return err
	}
	outsideCommit, err := getCurrentCommit(rootDir)
	if err != nil {
		return err
	}

	// Submodule is not initialized: impossible to be dirty.
	if preResetCommit == outsideCommit {
		return nil
	}

	lastPostPatchCommit, err := readStatusFile(config.FullPostPatchStatusFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Either the "apply" command hasn't been run before, or it has, but the user
			// deleted our status-tracking file. Assume the former, that this is basically a
			// fresh repo.
			return nil
		}
		return err
	}
	// Submodule has not been changed since the last time "apply" was run: there's nothing to lose.
	if preResetCommit == lastPostPatchCommit {
		return nil
	}

	// The last pre-patch commit is ok, too. This could be the case if the user ran "git submodule
	// update" sometime after running "apply".
	lastPrePatchCommit, err := readStatusFile(config.FullPrePatchStatusFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if preResetCommit == lastPrePatchCommit {
		return nil
	}

	// Check if the submodule commit is the same as what the Git index of the outer repo expects. We
	// need to check this because the user could have checked out a different version of the outer
	// repo and run "git submodule update" without running "apply" again.
	currentTargetCommit, err := getTargetSubmoduleCommit(rootDir, goDir)
	if err != nil {
		return err
	}
	if preResetCommit == currentTargetCommit {
		return nil
	}

	// If we didn't detect a non-dirty case, the submodule is pointing at an unknown commit, and we
	// must assume it's a change the user has made.
	return fmt.Errorf(
		"the current submodule commit %v is unexpected. Known commit hashes:\n"+
			"  last post-patch commit: %v\n"+
			"  last pre-patch commit: %v\n"+
			"  current commit in outer repo index: %v\n"+
			"Aborting: reapplying patches would discard changes in submodule. Use '-f' to proceed anyway",
		preResetCommit,
		lastPostPatchCommit,
		lastPrePatchCommit,
		currentTargetCommit)
}
