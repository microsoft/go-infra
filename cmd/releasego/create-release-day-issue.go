// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"errors"
	"flag"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "create-release-day-issue",
		Summary:     "Create the tracking issue for a day's worth of releases",
		Description: "\n\n" + azdo.AzDOBuildDetectionDoc,
		Handle:      handleCreateReleaseDayIssue,
	})
}

func handleCreateReleaseDayIssue(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	pat := githubutil.BindPATFlag()
	issueNumbersFlag := flag.String("issue-numbers", "", "[Required] The issue numbers that track the day's releases, separated by ','.")
	setVariableName := flag.String(
		"set-azdo-variable-name", "",
		"An AzDO variable name to set to the created issue number.")

	if err := p(); err != nil {
		return err
	}

	if *issueNumbersFlag == "" {
		return errors.New("no issue numbers specified")
	}

	issues := strings.Split(*issueNumbersFlag, ",")
	sort.Strings(issues)

	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	desc := "This issue tracks the status of multiple ongoing microsoft/go releases.\n"

	for _, issueNumber := range issues {
		desc += "\n* [ ] " + owner + "/" + name + "#" + issueNumber
	}

	desc += "\n\nThis issue also tracks producing Docker tags in [microsoft/go-images](https://github.com/microsoft/go-images)." +
		" I'll post a comment here when Docker automation succeeds or fails."

	releaseDayIssueNumber, err := createReleaseIssue(
		*pat, *repo,
		"Releases for "+time.Now().UTC().Format("2006-01-02"),
		desc)
	if err != nil {
		return err
	}

	if *setVariableName != "" {
		azdo.LogCmdSetVariable(*setVariableName, strconv.Itoa(releaseDayIssueNumber))
	}
	return nil
}
