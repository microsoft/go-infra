// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/patch"
)

const extractSummary = "Format each new commit in the submodule as a patch file."

const extractDescription = extractSummary + `

This command figures out which commits are new by checking for commits in HEAD since the given
commit. If no commit is given, the commit recorded by "apply" is used. If the given commit is not an
ancestor of the HEAD commit, "extract" formats patches for each commit until a common ancestor of HEAD
and the given commit. (See "git format-patch" documentation for "<since>".)

extract uses "git format-patch" internally, passing additional arguments to reduce the amount of
non-repeatable data in the resulting patch file.
` + repoRootSearchDescription

var extract = subcommand{
	Name:    "extract",
	Summary: extractSummary,
	Handle: func() error {
		sinceFlag := flag.String("since", "", "The commit or ref to begin formatting patches at. If nothing is specified, use the last commit recorded by 'apply'.")

		if err := parseFlagArgs(extractDescription); err != nil {
			return err
		}

		rootDir, err := findOuterRepoRoot()
		if err != nil {
			return err
		}

		goDir := filepath.Join(rootDir, "go")
		patchDir := filepath.Join(rootDir, "patches")

		since := *sinceFlag
		if since == "" {
			since, err = readStatusFile(rootDir)
			if err != nil {
				return err
			}
		}

		// Delete all old patches so if any commit descriptions have been changed, we don't end up
		// with two copies of those patch files with slightly different names.
		if err := patch.WalkPatches(rootDir, func(s string) error {
			return os.Remove(s)
		}); err != nil {
			return err
		}

		cmd := exec.Command(
			"git",
			"format-patch",

			// Remove default signature, which includes the Git version.
			"--signature=",
			// Use "From 0000000" instead of "From abc123f" in the patch file. A new commit hash is
			// generated each time the patches are applied, and including it in the patch text would
			// make the process less repeatable.
			"--zero-commit",
			// Remove "[PATCH 1/3]" to avoid depending on the total number of patch files.
			"--no-numbered",
			// Emit the patch files in the patches directory.
			"-o", patchDir,

			since,
		)
		cmd.Dir = goDir

		return executil.Run(cmd)
	},
}
