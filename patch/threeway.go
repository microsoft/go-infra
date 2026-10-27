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
// These conditions are met by automatic sync, where upstream commits are
// fetched into the root Git repository instead of setting up a proper
// submodule. They aren't met by "git go-patch ..." workflows, where upstream
// commits are kept separately.
//
// If a clean apply fails, TryThreeWayRebase retries with "git am --3way". When that succeeds, it
// regenerates only that patch, leaving clean patches untouched to avoid noisy diffs. If index
// preservation is enabled, it restores index lines before applying and updates index.json for
// regenerated patches. If a three-way merge conflicts, patch files are left unchanged. The
// returned paths are relative to rootDir. If rootDir has no matching patch configuration, the
// function does nothing.
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

type fileReplacement struct {
	path        string
	content     []byte
	allowCreate bool
}

func writeFileReplacements(rootDir string, replacements []fileReplacement) ([]string, error) {
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
			return nil, fmt.Errorf("failed to determine generated file path: %w", err)
		}
		// Preserve permissions when replacing a file. A generated file may be absent only when the
		// caller explicitly allows creating it, in which case it gets standard file permissions.
		info, err := os.Stat(replacement.path)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = info.Mode().Perm()
		} else if !os.IsNotExist(err) || !replacement.allowCreate {
			return nil, fmt.Errorf("failed to inspect generated file %q: %w", replacement.path, err)
		}
		temp, err := os.CreateTemp(filepath.Dir(replacement.path), "."+filepath.Base(replacement.path)+"-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary file for %q: %w", replacement.path, err)
		}
		tempPath := temp.Name()
		if _, err := temp.Write(replacement.content); err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write temporary file for %q: %w", replacement.path, err)
		}
		if err := temp.Chmod(mode); err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to preserve permissions for generated file %q: %w", replacement.path, err)
		}
		if err := temp.Close(); err != nil {
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("failed to close temporary file for %q: %w", replacement.path, err)
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
			return nil, fmt.Errorf("failed to replace generated file %q: %w", replacement.path, err)
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

	var index Index
	var indexedPatchDir string
	if config.PreserveIndex {
		var err error
		index, err = ReadIndex(filepath.Join(config.RootDir, config.PatchesDir))
		if err != nil {
			return nil, err
		}
		if index == nil {
			index = make(Index)
		}
		if len(index) != 0 {
			indexedPatchDir, err = os.MkdirTemp("", "indexed-patches-*")
			if err != nil {
				return nil, err
			}
			defer func() {
				if err := os.RemoveAll(indexedPatchDir); err != nil {
					log.Printf("Unable to clean up indexed patches %q: %v", indexedPatchDir, err)
				}
			}()
		}
	}

	workDir, err := gitcmd.NewTempCloneRepo(sourceRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary repo for patch rebase: %w", err)
	}
	defer gitcmd.AttemptDelete(workDir)

	if err := gitcmd.Run(workDir, "checkout", "--detach", "--quiet", baseCommit); err != nil {
		return nil, fmt.Errorf("failed to check out patch base commit %q: %w", baseCommit, err)
	}

	var replacements []fileReplacement
	var obsoletePatches []string
	indexChanged := false

	for _, patchPath := range patchPaths {
		baseTree, err := gitcmd.RevParse(workDir, "HEAD^{tree}")
		if err != nil {
			return nil, fmt.Errorf("failed to find patch base tree for %q: %w", patchPath, err)
		}
		applyPatchPath, err := index.restoreFile(patchPath, indexedPatchDir)
		if err != nil {
			return nil, fmt.Errorf("failed to restore index lines for patch %q: %w", patchPath, err)
		}
		if err := gitcmd.Am(workDir, "--quiet", applyPatchPath); err == nil {
			if _, err := recordObsoletePatch(workDir, patchPath, baseTree, &obsoletePatches, index, &indexChanged); err != nil {
				return nil, err
			}
			continue
		} else {
			log.Printf("Patch %q did not apply cleanly; retrying with a three-way merge: %v\n", filepath.Base(patchPath), err)
		}

		if err := gitcmd.Run(workDir, "am", "--abort"); err != nil {
			return nil, fmt.Errorf("failed to abort clean patch apply before three-way retry: %w", err)
		}

		if err := gitcmd.Run(workDir, "am", "--3way", "--whitespace=nowarn", "--quiet", applyPatchPath); err != nil {
			log.Printf("Three-way patch merge did not resolve %q; leaving patch files unchanged: %v\n", filepath.Base(patchPath), err)
			// The temporary repo is about to be deleted, so an abort is only best-effort cleanup.
			_ = gitcmd.Run(workDir, "am", "--abort")
			return nil, nil
		}

		obsolete, err := recordObsoletePatch(workDir, patchPath, baseTree, &obsoletePatches, index, &indexChanged)
		if err != nil {
			return nil, err
		}
		if obsolete {
			continue
		}

		formatArgs := []string{"--stdout", "-1", "HEAD"}
		if config.PreserveIndex {
			formatArgs = append([]string{"--full-index"}, formatArgs...)
		}
		formatted, err := FormatPatch(workDir, formatArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to regenerate patch %q: %w", filepath.Base(patchPath), err)
		}
		p, err := Read(strings.NewReader(formatted))
		if err != nil {
			return nil, fmt.Errorf("failed to parse regenerated patch %q: %w", filepath.Base(patchPath), err)
		}
		if config.ExtractAsAuthor != "" {
			p.SetAuthor(config.ExtractAsAuthor)
		}
		replacement := fileReplacement{
			path:    patchPath,
			content: []byte(p.String()),
		}
		replacements = append(replacements, replacement)
		if config.PreserveIndex && !index.Record(filepath.Base(patchPath), replacement.content, p) {
			return nil, fmt.Errorf("failed to record index lines for regenerated patch %q", patchPath)
		}
	}

	if config.PreserveIndex && (len(replacements) > 0 || indexChanged) {
		content, err := index.marshal()
		if err != nil {
			return nil, fmt.Errorf("failed to generate patch index: %w", err)
		}
		replacements = append(replacements, fileReplacement{
			path:        filepath.Join(config.RootDir, config.PatchesDir, IndexFileName),
			content:     content,
			allowCreate: true,
		})
	}

	updatedFiles, err := writeFileReplacements(config.RootDir, replacements)
	if err != nil {
		return nil, err
	}
	for _, patchPath := range obsoletePatches {
		if err := os.Remove(patchPath); err != nil {
			return nil, fmt.Errorf("failed to remove obsolete patch %q: %w", patchPath, err)
		}
		relativePath, err := filepath.Rel(config.RootDir, patchPath)
		if err != nil {
			return nil, fmt.Errorf("failed to determine obsolete patch path: %w", err)
		}
		updatedFiles = append(updatedFiles, relativePath)
	}
	return updatedFiles, nil
}

func recordObsoletePatch(workDir, patchPath, baseTree string, obsoletePatches *[]string, index Index, indexChanged *bool) (bool, error) {
	obsolete, err := patchTreeUnchanged(workDir, patchPath, baseTree)
	if err != nil {
		return false, err
	}
	if !obsolete {
		return false, nil
	}
	*obsoletePatches = append(*obsoletePatches, patchPath)
	if _, ok := index[filepath.Base(patchPath)]; ok {
		delete(index, filepath.Base(patchPath))
		*indexChanged = true
	}
	log.Printf("Patch %q is already present upstream; removing it from the patch set.\n", filepath.Base(patchPath))
	return true, nil
}

func patchTreeUnchanged(workDir, patchPath, baseTree string) (bool, error) {
	tree, err := gitcmd.RevParse(workDir, "HEAD^{tree}")
	if err != nil {
		return false, fmt.Errorf("failed to check whether patch %q is obsolete: %w", patchPath, err)
	}
	return tree == baseTree, nil
}
