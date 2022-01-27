// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package submodule manages submodules as used by the Microsoft Go repository.
package submodule

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/executil"
)

// Init initializes and updates the submodule, but does not clean it. This func offers more options
// for initialization than Reset. If origin is defined, fetch the submodule from there instead of
// the default defined in '.gitmodules'. If fetchBearerToken is nonempty, use it as a bearer token
// during the fetch. If shallow is true, clone the submodule with depth 1.
func Init(rootDir, origin, fetchBearerToken string, shallow bool) error {
	// Update the submodule commit, and initialize if it hasn't been done already.
	command := []string{"git"}
	if origin != "" {
		command = append(command, "-c", "submodule.go.url="+origin)
	}
	if fetchBearerToken != "" {
		command = append(command, "-c", "http.extraheader=AUTHORIZATION: bearer "+fetchBearerToken)
	}
	command = append(command, "submodule", "update", "--init")
	if shallow {
		command = append(command, "--depth", "1")
	}

	return executil.Run(dirCmd(rootDir, command...))
}

// Reset updates the submodule (with '--init'), aborts all in-progress Git operations like rebases,
// resets all changes, and cleans all untracked files.
func Reset(rootDir string) error {
	goDir := filepath.Join(rootDir, "go")

	// Update the submodule commit, and initialize if it hasn't been done already.
	if err := executil.Run(dirCmd(rootDir, "git", "submodule", "update", "--init")); err != nil {
		return err
	}

	// Find toplevel directories (Git working tree roots) for the outer repo and what we expect to
	// be the Go submodule. If the toplevel directory is the same for both, make sure not to clean!
	// The submodule likely wasn't set up properly, and cleaning could result in unexpectedly losing
	// work in the outer repo when the command spills over.
	rootToplevel, err := getToplevel(rootDir)
	if err != nil {
		return err
	}
	goToplevel, err := getToplevel(goDir)
	if err != nil {
		return err
	}

	if rootToplevel == goToplevel {
		return fmt.Errorf("go submodule (%v) toplevel is the same as root (%v) toplevel: %v", goDir, rootDir, goToplevel)
	}

	// Abort long-running Git operations--sequences that span multiple commands. These may be active
	// and can interfere with the reset process. Ignore errors and output: aborting returns non-zero
	// exit codes and emits alarming-seeming output when there is nothing to do.
	_ = executil.RunQuiet(dirCmd(goDir, "git", "am", "--abort"))
	_ = executil.RunQuiet(dirCmd(goDir, "git", "rebase", "--abort"))
	_ = executil.RunQuiet(dirCmd(goDir, "git", "merge", "--abort"))

	// Reset the index and working directory. This doesn't clean up new untracked files.
	if err := executil.Run(dirCmd(goDir, "git", "reset", "--hard")); err != nil {
		return err
	}
	// Delete untracked files detected by Git. Deliberately leave files that are ignored in
	// '.gitignore': these files shouldn't interfere with the build process and could be used for
	// incremental builds.
	return executil.Run(dirCmd(goDir, "git", "clean", "-df"))
}

func getToplevel(dir string) (string, error) {
	return executil.CombinedOutput(dirCmd(dir, "git", "rev-parse", "--show-toplevel"))
}

func dirCmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	return c
}
