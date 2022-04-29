// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/subcmd"
)

const description = `
git-go-patch is a tool that helps work with the submodule and Git patch files used by the Microsoft
Go repository. The subcommands implement common workflows for patch creation and maintenance.
`

const repoRootSearchDescription = `
This command searches the current working directory and any ancestor for "go" and "patches"
directories. If both are present in a directory, the command assumes is the microsoft/go repository,
where "go" is the submodule, and "patches" contains the list of patch files. The search can be
overridden using the "-c" argument.
`

// repoRootFlag can be passed to any subcommand to specify a particular Microsoft Go repository
// directory.
var repoRootFlag = flag.String("c", "", "Disable Go repository discovery and use this path as the target.")

var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("git go-patch", description, subcommands); err != nil {
		log.Fatal(err)
	}
}

func getStatusFileDir(rootDir string) string {
	return filepath.Join(rootDir, "eng", "artifacts", "go-patch")
}

func getPrePatchStatusFilePath(rootDir string) string {
	return filepath.Join(getStatusFileDir(rootDir), "HEAD_BEFORE_APPLY")
}

func getPostPatchStatusFilePath(rootDir string) string {
	return filepath.Join(getStatusFileDir(rootDir), "HEAD_AFTER_APPLY")
}

func readStatusFile(file string) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

// findOuterRepoRoot returns the repoRootFlag value, if defined. Otherwise, searches the current
// working directory and its ancestors for a directory that appears to be a Microsoft Go repository:
// one that contains directories "patches" and "go". This function should only be called after flags
// have been parsed.
func findOuterRepoRoot() (string, error) {
	if *repoRootFlag != "" {
		return *repoRootFlag, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	var dir = cwd
	for {
		goInfo, err := os.Stat(filepath.Join(dir, "go"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		patchesInfo, err := os.Stat(filepath.Join(dir, "patches"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}

		if goInfo != nil && goInfo.IsDir() && patchesInfo != nil && patchesInfo.IsDir() {
			fmt.Printf("Found Microsoft Go directory at %v\n", dir)
			return dir, nil
		}

		parent := filepath.Dir(dir)
		// When we've hit the filesystem root, Dir goes no further.
		if dir == parent {
			return "", fmt.Errorf("no Microsoft Go root found in any ancestor of %v", cwd)
		}
		dir = parent
	}
}

// findProjectRoots finds the project and submodule dir based on the current working directory or
// the repoRootFlag value, using findOuterRepoRoot.
func findProjectRoots() (projectDir, submoduleDir string, err error) {
	projectDir, err = findOuterRepoRoot()
	if err != nil {
		return
	}
	submoduleDir = filepath.Join(projectDir, "go")
	return
}
