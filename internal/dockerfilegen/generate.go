// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package dockerfilegen generates Dockerfiles in the Microsoft Go images repository.
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

// EnsurePrerequisites checks if Dockerfile generation prerequisites are satisfied.
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

// Run generates Dockerfiles in the given go-images repository root. Call this after updating
// versions.json to synchronize the Dockerfiles. It doesn't check for prerequisites: call
// EnsurePrerequisites before starting an update to detect missing tools early.
func Run(repoRoot string, forceSubmoduleReset, skipSubmoduleUpdate bool) error {
	fmt.Println("Generating Dockerfiles...")

	goDir := filepath.Join(repoRoot, "go")
	microsoftDockerfileRoot := filepath.Join(repoRoot, "src", "microsoft")

	// A fork keeps upstream Go Docker code in its root; the go-images repository uses a submodule.
	_, err := os.Stat(goDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Fork repository detected: no 'go' directory.")
			goDir = repoRoot
		} else {
			return err
		}
	} else if !skipSubmoduleUpdate {
		fmt.Println("---- Resetting submodule...")
		if err := submodule.Reset(repoRoot, goDir, forceSubmoduleReset); err != nil {
			return err
		}
		fmt.Println("---- Applying patches to submodule index...")
		patchConfig, err := patch.FindAncestorConfig(repoRoot)
		if err != nil {
			return err
		}
		if err := patch.Apply(patchConfig, patch.ApplyModeIndex); err != nil {
			return err
		}
	}

	if err := copyDockerfileTemplates(goDir, microsoftDockerfileRoot); err != nil {
		return err
	}

	cmd := exec.Command(filepath.Join(goDir, "apply-templates.sh"))
	cmd.Dir = microsoftDockerfileRoot
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

func run(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
