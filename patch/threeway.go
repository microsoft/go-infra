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

type patchReplacement struct {
	path    string
	content []byte
}

func writePatchReplacements(rootDir string, replacements []patchReplacement) ([]string, error) {
	type preparedReplacement struct {
		path         string
		tempPath     string
		relativePath string
	}
	prepared := make([]preparedReplacement, 0, len(replacements))
	defer func() {
		for _, replacement := range prepared {
			_ = os.Remove(replacement.tempPath)
		}
	}()

	for _, replacement := range replacements {
		relativePath, err := filepath.Rel(rootDir, replacement.path)
		if err != nil {
			return nil, fmt.Errorf("failed to determine regenerated patch path: %w", err)
		}
		info, err := os.Stat(replacement.path)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect patch %q: %w", replacement.path, err)
		}
		temp, err := os.CreateTemp(filepath.Dir(replacement.path), "."+filepath.Base(replacement.path)+"-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary patch for %q: %w", replacement.path, err)
		}
		tempPath := temp.Name()
		if _, err := temp.Write(replacement.content); err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write temporary patch for %q: %w", replacement.path, err)
		}
		if err := temp.Chmod(info.Mode().Perm()); err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to preserve permissions for patch %q: %w", replacement.path, err)
		}
		if err := temp.Close(); err != nil {
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to close temporary patch for %q: %w", replacement.path, err)
		}
		prepared = append(prepared, preparedReplacement{
			path:         replacement.path,
			tempPath:     tempPath,
			relativePath: relativePath,
		})
	}

	updatedFiles := make([]string, 0, len(prepared))
	for _, replacement := range prepared {
		if err := os.Rename(replacement.tempPath, replacement.path); err != nil {
			return nil, fmt.Errorf("failed to replace regenerated patch %q: %w", replacement.path, err)
		}
		updatedFiles = append(updatedFiles, replacement.relativePath)
	}
	return updatedFiles, nil
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

	var replacements []patchReplacement

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
		replacements = append(replacements, patchReplacement{
			path:    patchPath,
			content: []byte(p.String()),
		})
	}

	return writePatchReplacements(config.RootDir, replacements)
}
