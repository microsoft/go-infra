// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
	"golang.org/x/mod/semver"
)

// version is the semver of this tool. Compared against the value in the config file (if any) to
// ensure that all users of the tool contributing to a given repo have a new enough version of the
// tool to support all patching features used in that repo.
//
// When adding a new feature to the git-go-patch tool, make sure it is backward compatible and
// increment the patch number here.
const version = "v1.0.1"

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

func readStatusFile(file string) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

// loadConfig returns the config file governing the repoRootFlag dir, if the flag is defined.
// Otherwise, attempts to find the configuration that applies to the current working directory. This
// function should only be called after flags have been parsed.
func loadConfig() (*patch.FoundConfig, error) {
	var dir string
	var err error
	if *repoRootFlag != "" {
		dir, err = filepath.Abs(*repoRootFlag)
	} else {
		dir, err = os.Getwd()
	}
	if err != nil {
		return nil, err
	}
	config, err := patch.FindAncestorConfig(dir)
	if err != nil {
		return nil, err
	}
	if config.MinimumToolVersion != "" {
		if semver.Compare(version, config.MinimumToolVersion) < 0 {
			fmt.Printf("Your copy of git-go-patch is too old for this repository. Use this command to upgrade:\n\n" +
				"  go install github.com/microsoft/go-infra/cmd/git-go-patch@latest\n")
			return nil, fmt.Errorf("tool version is lower than config file minimum version: %q < %q", version, config.MinimumToolVersion)
		}
	}
	return config, nil
}
