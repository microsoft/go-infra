// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package dockerfilegen generates Dockerfiles in the Microsoft build of Go images repository.
package dockerfilegen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/submodule"
)

// EnsurePrerequisites checks if Dockerfile generation prerequisites are satisfied and returns a
// descriptive error if not.
func EnsurePrerequisites() error {
	missingTools := false
	for _, requiredCmd := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(requiredCmd); err != nil {
			fmt.Printf("Unable to find '%s' in PATH. It is required to run 'eng/update-dockerfiles.sh'.\n", requiredCmd)
			fmt.Printf("Error: %s\n", err)
			missingTools = true
		}
	}
	if missingTools {
		return fmt.Errorf("missing required tools to generate Dockerfiles. Make sure the tools are in PATH and try again, or pass '-skip-dockerfiles' to the command")
	}
	return nil
}

// Run runs the Dockerfile generation script in the given go-images repo root.
// Call this after updating the versions.json file to synchronize the Dockerfiles. This function
// doesn't check for prerequisites: EnsurePrerequisites should be called before any auto-update
// code runs to detect problems before wasting time on an incompletable update.
// submoduleDir is relative to repoRoot.
func Run(repoRoot, submoduleDir string, forceSubmoduleReset, skipSubmoduleUpdate bool) error {
	fmt.Println("Generating Dockerfiles...")

	// The location of upstream Go Docker code. We start by assuming it's in the given submodule.
	goDir := filepath.Join(repoRoot, submoduleDir)
	// Location of our Dockerfiles: where the "1.16", "1.17" etc. directories are located.
	microsoftDockerfileRoot := filepath.Join(repoRoot, "src", "microsoft")

	// Detect whether this go-images repository is based on a Git fork or a submodule. A submodule
	// uses scripts from a slightly different location and requires patches to be applied first.
	_, err := os.Stat(goDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Fork repository detected: no '%s' directory.\n", submoduleDir)
			// We are in a Git fork (not a submodule) so we now know Go Docker source code is
			// directly in the repo root.
			goDir = repoRoot
		} else {
			// Could not determine if the repo is a fork vs. submodule for some unknown reason.
			return err
		}
	} else if !skipSubmoduleUpdate {
		// No err: the submodule directory exists. Now, unless the caller tells us otherwise, ensure
		// the submodule is set up correctly and patched, so we can use the patched templates inside
		// to generate our Dockerfiles.
		fmt.Println("---- Resetting submodule...")
		if err := submodule.Reset(repoRoot, goDir, forceSubmoduleReset); err != nil {
			return err
		}
		fmt.Println("---- Applying patches to submodule index...")
		// Apply patches to the index without changing the commit hash. This means that later, a
		// "git add ." or "git commit -a" won't try to change the submodule hash. ApplyModeCommits
		// creates fresh commits that aren't available anywhere and would break a fresh clone.
		patchConfig, err := patch.FindAncestorConfig(repoRoot)
		if err != nil {
			return err
		}
		if err := patch.Apply(patchConfig, patch.ApplyModeIndex); err != nil {
			return err
		}
	}

	// Copy templates into "our" directory. This puts them in the correct location for
	// "apply-templates.sh" to see them. We don't check in a copy: we want to keep it in sync with
	// upstream's copy and apply some small patches.
	if err := copyDockerfileTemplates(goDir, microsoftDockerfileRoot); err != nil {
		return err
	}

	// Run the upstream "apply-templates.sh", but in this directory. This causes the script to
	// update our Dockerfiles using the data in our "versions.json". Keeping our own version of the
	// checked-in evaluated templates prevents merge conflicts in generated code when we merge
	// changes from upstream.
	cmd := exec.Command(filepath.Join(goDir, "apply-templates.sh"))
	// Run this script from the "src/microsoft" directory, which contains the versions.json file and
	// the Dockerfiles. The upstream script relies on the current working directory to decide where
	// to generate the Dockerfiles.
	cmd.Dir = microsoftDockerfileRoot
	// Make sure "apply-templates.sh" uses our checked-in copy of "jq-template.awk" instead of
	// downloading it on the fly.
	cmd.Env = append(cmd.Env, "BASHBREW_SCRIPTS=.")

	return run(cmd)
}

func copyDockerfileTemplates(srcDir, dstDir string) error {
	templates, err := filepath.Glob(filepath.Join(srcDir, "*.template"))
	if err != nil {
		return err
	}

	for _, t := range templates {
		dst := filepath.Join(dstDir, filepath.Base(t))
		fmt.Printf("---- Copying template %q to %q...\n", t, dst)
		if err := copyFile(t, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(d, s); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// run sets up the command so it logs directly to our stdout/stderr streams, then runs it.
func run(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
