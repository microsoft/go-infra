// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"log"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-target-branch",
		Summary: "Calculate the target branch for a given release version.",
		Handle:  handleGetTargetBranch,
	})
}

func handleGetTargetBranch(p subcmd.ParseFunc) error {
	version := flag.String(
		"version", "",
		"[Required] A full microsoft/goversion number (major.minor.patch-revision[-suffix]).\n"+
			"The configuration file is filtered to a single entry and branch using this info.")

	setVariableBranchName := flag.String(
		"set-azdo-variable-branch-name", "",
		"An AzDO variable name to set to the name of the branch that the sync PR is based on.")

	if err := p(); err != nil {
		return err
	}

	if *version == "" {
		return errors.New("no version specified")
	}

	v := goversion.New(*version)
	versionUpstream := versionBranch(v)

	log.Printf("Target branch name: %v\n", versionUpstream)
	if *setVariableBranchName != "" {
		azdo.LogCmdSetVariable(*setVariableBranchName, versionUpstream)
	}

	return nil
}
