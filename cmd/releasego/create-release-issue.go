// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"os"
	"strconv"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "create-release-issue",
		Summary:     "Create release tracking issue on a GitHub repo",
		Description: "\n\n" + azdo.AzDOBuildDetectionDoc,
		Handle:      handleCreateReleaseIssue,
	})
}

func handleCreateReleaseIssue(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	pat := githubutil.BindPATFlag()
	release := flag.String("release", "", "[Required] The release number to file an issue for.")
	appendVariableName := flag.String(
		"append-azdo-variable-name", "",
		"An env variable to read from, append the filed issue number separated by ',', then set as an AzDO variable.")
	setVariableName := flag.String(
		"set-azdo-variable-name", "",
		"An AzDO variable name to set to the created issue number.")

	if err := p(); err != nil {
		return err
	}

	if *release == "" {
		return errors.New("no release specified")
	}

	issueNumber, err := createReleaseIssue(
		*pat, *repo,
		"Release: "+*release,
		"This issue tracks the status of the Microsoft build of Go "+*release+".\n\n"+
			"I'm starting the release build now, and I'll post another comment when it succeeds or fails.")
	if err != nil {
		return err
	}

	if *appendVariableName != "" {
		v := strconv.Itoa(issueNumber)
		if issues, ok := os.LookupEnv(*appendVariableName); ok {
			v = issues + "," + v
		}
		azdo.SetPipelineVariable(*appendVariableName, v)
	}
	if *setVariableName != "" {
		azdo.SetPipelineVariable(*setVariableName, strconv.Itoa(issueNumber))
	}
	return nil
}
