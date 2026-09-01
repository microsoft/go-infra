// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package patch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/gitcmd"
)

// TryThreeWayRebase checks whether patches configured at rootDir apply cleanly to baseCommit.
// rootDir must be a Git repository containing baseCommit and the objects referenced by the patches;
// submoduleDir is relative to rootDir.
//
// If a clean apply fails, TryThreeWayRebase retries with "git am --3way". When that succeeds, it
// regenerates only that patch, leaving clean patches untouched to avoid noisy diffs. If a three-way
// merge conflicts, patch files are left unchanged. The returned paths are relative to rootDir.
// If rootDir has no matching patch configuration, the function does nothing.
func TryThreeWayRebase(rootDir, submoduleDir, baseCommit string) ([]string, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repository path: %w", err)
	}
	config, err := findConfigInDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read git-go-patch configuration: %w", err)
	}
	if config == nil || filepath.Clean(config.SubmoduleDir) != filepath.Clean(submoduleDir) {
		return nil, nil
	}
	return tryThreeWayRebase(config, rootDir, baseCommit)
}

func tryThreeWayRebase(config *FoundConfig, sourceRepo, baseCommit string) ([]string, error) {
	var patchPaths []string
	if err := WalkGoPatches(config, func(path string) error {
		patchPaths = append(patchPaths, path)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to find patches to rebase: %w", err)
	}
	if len(patchPaths) == 0 {
		return nil, nil
	}

	workDir, err := gitcmd.NewTempCloneRepo(sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary repo for patch rebase: %w", err)
	}
	defer gitcmd.AttemptDelete(workDir)

	if err := gitcmd.Run(workDir, "checkout", "--detach", "--quiet", baseCommit); err != nil {
		return nil, fmt.Errorf("failed to check out patch base commit %q: %w", baseCommit, err)
	}

	type replacement struct {
		path    string
		content []byte
	}
	var replacements []replacement

	for _, patchPath := range patchPaths {
		if err := gitcmd.Am(workDir, "--quiet", patchPath); err == nil {
			continue
		} else {
			log.Printf("Patch %q did not apply cleanly; retrying with a three-way merge: %v\n", filepath.Base(patchPath), err)
		}

		if err := gitcmd.Run(workDir, "am", "--abort"); err != nil {
			return nil, fmt.Errorf("failed to abort clean patch apply before three-way retry: %w", err)
		}

		if err := gitcmd.Run(workDir, "am", "--3way", "--whitespace=nowarn", "--quiet", patchPath); err != nil {
			log.Printf("Three-way patch merge did not resolve %q; leaving patch files unchanged: %v\n", filepath.Base(patchPath), err)
			// The temporary repo is about to be deleted, so an abort is only best-effort cleanup.
			_ = gitcmd.Run(workDir, "am", "--abort")
			return nil, nil
		}

		formatted, err := FormatPatch(workDir, "--stdout", "-1", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to regenerate patch %q: %w\n%s", filepath.Base(patchPath), err, formatted)
		}
		p, err := Read(strings.NewReader(formatted))
		if err != nil {
			return nil, fmt.Errorf("failed to parse regenerated patch %q: %w", filepath.Base(patchPath), err)
		}
		if config.ExtractAsAuthor != "" {
			p.FromAuthor = config.ExtractAsAuthor
		}
		replacements = append(replacements, replacement{
			path:    patchPath,
			content: []byte(p.String()),
		})
	}

	updatedFiles := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		if err := os.WriteFile(replacement.path, replacement.content, 0o666); err != nil {
			return nil, fmt.Errorf("failed to write regenerated patch %q: %w", replacement.path, err)
		}
		relativePath, err := filepath.Rel(config.RootDir, replacement.path)
		if err != nil {
			return nil, fmt.Errorf("failed to determine regenerated patch path: %w", err)
		}
		updatedFiles = append(updatedFiles, relativePath)
	}
	return updatedFiles, nil
}
