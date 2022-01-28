// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/submodule"
)

const applySummary = "Apply patch files as one commit each using 'git apply' on top of HEAD."

const applyDescription = applySummary + `

This command also records the state of the repository before applying patches, so "extract" can be used
later to create patch files after adding more commits, or altering the patch commits.

apply uses "git am", internally. If patches fail to apply, use "git am" inside the submodule to
resolve and continue the patch application process.
` + repoRootSearchDescription

var apply = subcommand{
	Name:    "apply",
	Summary: applySummary,
	Handle: func() error {
		noRefresh := flag.Bool(
			"no-refresh",
			false,
			"Skip the submodule refresh (reset, clean, checkout) that happens before applying patches.\n"+
				"This may be useful for advanced workflows.")

		if err := parseFlagArgs(applyDescription); err != nil {
			return err
		}

		rootDir, err := findOuterRepoRoot()
		if err != nil {
			return err
		}

		if !*noRefresh {
			if err := submodule.Reset(rootDir); err != nil {
				return err
			}
		}

		if err := writeStatusFile(rootDir); err != nil {
			return err
		}

		return patch.Apply(rootDir, patch.ApplyModeCommits)
	},
}

func writeStatusFile(rootDir string) error {
	currentHead, err := getCurrentCommit(rootDir)
	if err != nil {
		return err
	}

	// Point out where the status file is located. Don't use %q because it would turn Windows "\" to
	// "\\", making the path harder to paste and use elsewhere.
	fmt.Printf("Writing HEAD commit to '%v' for use by 'extract' later: %v\n", getStatusFilePath(rootDir), currentHead)

	if err := os.MkdirAll(getStatusFileDir(rootDir), os.ModePerm); err != nil {
		return err
	}
	return os.WriteFile(getStatusFilePath(rootDir), []byte(currentHead), os.ModePerm)
}

func getCurrentCommit(rootDir string) (string, error) {
	goDir := filepath.Join(rootDir, "go")

	currentCmd := exec.Command("git", "rev-parse", "HEAD")
	currentCmd.Dir = goDir
	return executil.SpaceTrimmedCombinedOutput(currentCmd)
}
