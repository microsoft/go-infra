// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package patch manages patch files as stored in the Microsoft Go repository alongside a submodule.
package patch

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

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
func Apply(config *FoundConfig, mode ApplyMode) error {
	_, goDir := config.FullProjectRoots()

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

	err := WalkGoPatches(config, func(file string) error {
		cmd.Args = append(cmd.Args, file)
		return nil
	})
	if err != nil {
		return err
	}

	return executil.Run(cmd)
}

// WalkGoPatches finds patches in the given Microsoft Go repository root directory and runs fn once
// per patch file path. If fn returns an error, walking terminates and the error is returned.
func WalkGoPatches(config *FoundConfig, fn func(string) error) error {
	return WalkPatches(filepath.Join(config.RootDir, config.PatchesDir), fn)
}

// WalkPatches finds patches in the given directory and runs fn once per patch file path. If fn
// returns an error, walking terminates and the error is returned.
func WalkPatches(dir string, fn func(string) error) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.patch"))
	if err != nil {
		return err
	}

	// We depend on alphabetical patch apply order.
	sort.Strings(matches)

	for _, match := range matches {
		if err := fn(match); err != nil {
			return err
		}
	}
	return nil
}

// FindAncestorConfig finds and reads the config file governing dir. Searches dir and all ancestor
// directories of dir, similar to how ".git" files work.
//
// If no config file is found in any ancestor, and an ancestor dir appears to be like microsoft/go
// by convention (contains a "patches" directory and a "go" directory), creates a config struct to
// fit the conventional repo.
func FindAncestorConfig(dir string) (*FoundConfig, error) {
	originalDir := dir
	var byConvention *FoundConfig
	for {
		c, err := findConfigInDir(dir)
		if err != nil {
			return nil, err
		}
		if c != nil {
			// Found the config file in dir.
			return c, nil
		}
		// We didn't find a config file, but check if we found a conventional fork directory. Keep
		// track of only the first one (most nested one) that we find.
		if byConvention == nil {
			byConvention, err = dirConfig(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("Unable to determine if %v is a Go directory for a surprising reason: %v", dir, err)
			}
		}

		// We didn't find the config file yet. Find the parent dir for the next iteration.
		parent := filepath.Dir(dir)
		// When we've hit the filesystem root, Dir goes no further.
		if dir == parent {
			if byConvention != nil {
				return byConvention, nil
			}
			return nil, fmt.Errorf("no %q file or Microsoft Go root found in any ancestor of %v", ConfigFileName, originalDir)
		}
		dir = parent
	}
}

func dirConfig(dir string) (*FoundConfig, error) {
	if ok, err := isDir(filepath.Join(dir, conventionalConfig.SubmoduleDir)); !ok {
		return nil, err
	}
	if ok, err := isDir(filepath.Join(dir, conventionalConfig.PatchesDir)); !ok {
		return nil, err
	}
	return &FoundConfig{
		Config:  conventionalConfig,
		RootDir: dir,
	}, nil
}

func isDir(dir string) (ok bool, err error) {
	if info, err := os.Stat(dir); err != nil {
		return info != nil && info.IsDir(), err
	}
	return true, nil
}

func findConfigInDir(dir string) (*FoundConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	config := FoundConfig{
		RootDir: dir,
	}
	if err := json.Unmarshal(data, &config.Config); err != nil {
		return nil, err
	}
	return &config, nil
}
