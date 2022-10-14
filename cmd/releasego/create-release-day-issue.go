// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
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

// docPointerMarkdown is a pointer to the release process docs with some context, as markdown text.
const docPointerMarkdown = "For more information about the microsoft/go release process, see " +
	"[docs/release-process in microsoft/go-infra](https://github.com/microsoft/go-infra/tree/main/docs/release-process)."

var releaseIssueLabels = []string{"Area-Release"}

func handleCreateReleaseDayIssue(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	pat := githubutil.BindPATFlag()
	releasesFlag := flag.String(
		"releases", "",
		"[Required] The release numbers to track releasing during this day, separated by ','.")
	setVariableName := flag.String(
		"set-azdo-variable-name", "",
		"An AzDO variable name to set to the created issue number.")
	notify := flag.String(
		"notify", "",
		"A GitHub user to tag in the issue body so they are notified of future updates.")

	if err := p(); err != nil {
		return err
	}

	if *releasesFlag == "" {
		return errors.New("no releases specified")
	}

	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	releases := strings.Split(*releasesFlag, ",")
	sort.Strings(releases)

	title := time.Now().UTC().Format("2006-01-02") + " releases: " + strings.Join(releases, ", ")
	desc := "This issue tracks the status of ongoing microsoft/go releases and the image release " +
		"from [microsoft/go-images](https://github.com/microsoft/go-images). " +
		"I am a bot, and I'll keep the issue up to date and add a comment when I notice " +
		"something happen that likely requires the release runner to take some manual action." +
		"\n\n" + docPointerMarkdown

	if *notify != "" {
		desc += "\n\n/cc @" + *notify
	}

	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	log.Printf("Creating comment on %v/%v with title %#q and content:\n%v\n", owner, name, title, desc)

	var c *github.Issue
	if err = githubutil.Retry(func() error {
		var err error
		c, _, err = client.Issues.Create(ctx, owner, name, &github.IssueRequest{
			Title:  &title,
			Body:   &desc,
			Labels: &releaseIssueLabels,
		})
		if err != nil {
			return err
		}
		log.Printf("Link to issue: %v\n", *c.HTMLURL)
		return nil
	}); err != nil {
		return err
	}

	if *setVariableName != "" {
		azdo.LogCmdSetVariable(*setVariableName, strconv.Itoa(c.GetNumber()))
	}
	return nil
}
