// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package patch manages patch files as stored in the Microsoft Go repository alongside a submodule.
package patch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/executil"
)

type ApplyMode int

const (
	// ApplyModeCommits applies patches as commits. This is useful for developing changes to the
	// patches, because the commits can later be automatically extracted back into patch files.
	// Creating these commits creates new commit hashes, so it is not desirable if the HEAD commit
	// should correspond to a "real" commit.
	ApplyModeCommits ApplyMode = iota
	// ApplyModeIndex applies patches as changes to the Git index and working tree. Doesn't change
	// HEAD: it will continue to point to the same commit--likely upstream.
	//
	// This makes it more difficult to develop and save changes, but it is still possible. Patch
	// changes show up as staged changes, and additional changes show up as unstaged changes, so
	// they can still be differentiated and preserved.
	ApplyModeIndex
)

// Apply runs a Git command to apply the patches in the repository onto the submodule. The exact Git
// command used ("am" or "apply") depends on the patch mode.
func Apply(rootDir string, mode ApplyMode) error {
	goDir := filepath.Join(rootDir, "go")

	cmd := exec.Command("git")
	cmd.Dir = goDir

	switch mode {
	case ApplyModeCommits:
		cmd.Args = append(cmd.Args, "am")
	case ApplyModeIndex:
		cmd.Args = append(cmd.Args, "apply", "--index")
	default:
		return fmt.Errorf("invalid patch mode '%v'", mode)
	}

	// Trailing whitespace may be present in the patch files. Don't emit warnings for it here. These
	// warnings should be avoided when authoring each patch file. If we made it to this point, it's
	// too late to cause noisy warnings because of them.
	cmd.Args = append(cmd.Args, "--whitespace=nowarn")

	err := WalkPatches(rootDir, func(file string) error {
		cmd.Args = append(cmd.Args, file)
		return nil
	})
	if err != nil {
		return err
	}

	if err := executil.Run(cmd); err != nil {
		return err
	}
	return nil
}

// WalkPatches finds patches in the given Microsoft Go repository root directory and runs fn once
// per patch file path. If fn returns an error, walking terminates and the error is returned.
func WalkPatches(rootDir string, fn func(string) error) error {
	// ReadDir returns alphabetical order for patches: we depend on it for the patch apply order.
	patchDir := filepath.Join(rootDir, "patches")

	entries, err := os.ReadDir(patchDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".patch" {
			continue
		}

		if err := fn(filepath.Join(patchDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}