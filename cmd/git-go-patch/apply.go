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
	"strings"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
	"github.com/microsoft/go-infra/submodule"
)

type applyCmd struct{}

func (a applyCmd) Name() string {
	return "apply"
}

func (a applyCmd) Summary() string {
	return "Apply patch files as one commit each using 'git apply' on top of HEAD."
}

func (a applyCmd) Description() string {
	return `

This command also records the state of the repository before applying patches, so "extract" can be used
later to create patch files after adding more commits, or altering the patch commits.

apply uses "git am", internally. If patches fail to apply, use "git am" inside the submodule to
resolve and continue the patch application process.
` + repoRootSearchDescription
}

func (a applyCmd) Handle(p subcmd.ParseFunc) error {
	force := flag.Bool("f", false, "Force reapply: throw away changes in the submodule.")
	noRefresh := flag.Bool(
		"no-refresh",
		false,
		"Skip the submodule refresh (reset, clean, checkout) that happens before applying patches.\n"+
			"This may be useful for advanced workflows.")

	if err := p(); err != nil {
		return err
	}

	rootDir, err := findOuterRepoRoot()
	if err != nil {
		return err
	}

	goDir := filepath.Join(rootDir, "go")

	// If we're being careful, abort if the submodule commit isn't what we expect.
	if !*force {
		if err := ensureSubmoduleCommitNotDirty(rootDir, goDir); err != nil {
			return err
		}
	}

	if !*noRefresh {
		if err := submodule.Reset(rootDir, *force); err != nil {
			return err
		}
	}

	prePatchHead, err := getCurrentCommit(goDir)
	if err != nil {
		return err
	}

	// Record the pre-patch commit. We must do this before applying the patch: if patching
	// fails, the user needs to be able to fix up the patches inside the submodule and then run
	// "git go-patch extract" to apply the fixes to the patch files. "extract" depends on the
	// pre-patch status file. Start by ensuring the dir exists, then write the file.
	if err := os.MkdirAll(getStatusFileDir(rootDir), os.ModePerm); err != nil {
		return err
	}

	if err := writeStatusFiles(prePatchHead, getPrePatchStatusFilePath(rootDir)); err != nil {
		return err
	}

	if err := patch.Apply(rootDir, patch.ApplyModeCommits); err != nil {
		return err
	}

	postPatchHead, err := getCurrentCommit(goDir)
	if err != nil {
		return err
	}

	// Record the post-patch commit.
	return writeStatusFiles(postPatchHead, getPostPatchStatusFilePath(rootDir))
}

func writeStatusFiles(commit string, file string) error {
	// Point out where the status file is located. Don't use %q because it would turn Windows "\" to
	// "\\", making the path harder to paste and use elsewhere.
	fmt.Printf("Writing commit to '%v' for use by 'extract' later: %v\n", file, commit)
	return os.WriteFile(file, []byte(commit), os.ModePerm)
}

func getCurrentCommit(goDir string) (string, error) {
	currentCmd := exec.Command("git", "rev-parse", "HEAD")
	currentCmd.Dir = goDir
	return executil.SpaceTrimmedCombinedOutput(currentCmd)
}

func getTargetSubmoduleCommit(rootDir string) (string, error) {
	cmd := exec.Command("git", "ls-tree", "HEAD", "go")
	cmd.Dir = rootDir
	// Format, from Git docs: "<mode> SP <type> SP <object> TAB <file>"
	lsOut, err := executil.SpaceTrimmedCombinedOutput(cmd)
	if err != nil {
		return "", err
	}
	treeData := strings.Fields(lsOut)
	if len(treeData) <= 2 {
		return "", fmt.Errorf("output from git ls-tree doesn't contain enough fields: %v", lsOut)
	}
	return treeData[2], nil
}

func ensureSubmoduleCommitNotDirty(rootDir, goDir string) error {
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

	lastPostPatchCommit, err := readStatusFile(getPostPatchStatusFilePath(rootDir))
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
	lastPrePatchCommit, err := readStatusFile(getPrePatchStatusFilePath(rootDir))
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
	currentTargetCommit, err := getTargetSubmoduleCommit(rootDir)
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
