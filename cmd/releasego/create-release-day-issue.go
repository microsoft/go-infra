// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "create-release-day-issue",
		Summary: "Create the tracking issue for a day's worth of releases",
		Description: `

Unpins old, closed release trackers before creating and pinning the new tracker. Open trackers and
unrelated pinned issues are left alone. If GitHub's three pin slots are still full, posts a warning on
the new tracker and continues the release. Retries and reruns avoid duplicate warnings; other API
failures remain errors.

Cleanup happens when the next release tracker is created, not immediately when an issue is closed.
Closed trackers can be unpinned manually on GitHub in the meantime, or with
releasego sync-release-issue-pin.

` + azdo.AzDOBuildDetectionDoc,
		Handle: handleCreateReleaseDayIssue,
	})
}

// docPointerMarkdown is a pointer to the release process docs with some context, as markdown text.
const docPointerMarkdown = "For more information about the microsoft/go release process, see " +
	"[docs/release-process in microsoft/go-infra](https://github.com/microsoft/go-infra/tree/main/docs/release-process)."

const releaseDayIssueBodyPrefix = "This issue tracks the status of ongoing microsoft/go releases"

var releaseIssueLabels = []string{"Area-Release"}

func handleCreateReleaseDayIssue(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
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
	desc := releaseDayIssueBodyPrefix + " and the image release " +
		"from [microsoft/go-images](https://github.com/microsoft/go-images). " +
		"I am a bot, and I'll keep the issue up to date and add a comment when I notice " +
		"something happen that likely requires the release runner to take some manual action." +
		"\n\n" + docPointerMarkdown

	if *notify != "" {
		desc += "\n\n/cc @" + *notify
	}

	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	if err := unpinClosedReleaseIssues(ctx, client, owner, name); err != nil {
		return fmt.Errorf("clean up closed release tracker pins: %w", err)
	}

	log.Printf("Creating release tracking issue on %v/%v with title %#q and content:\n%v\n", owner, name, title, desc)

	var c *github.Issue
	if err = githubutil.Retry(func() error {
		var err error
		c, _, err = client.Issues.Create(ctx, owner, name, github.CreateIssueRequest{
			Title:  title,
			Body:   &desc,
			Labels: releaseIssueLabels,
		})
		if err != nil {
			return err
		}
		log.Printf("Link to issue: %v\n", *c.HTMLURL)
		return nil
	}); err != nil {
		return err
	}

	if err := syncReleaseIssuePin(ctx, client, owner, name, c); err != nil {
		return fmt.Errorf("pin release tracking issue %s: %w", c.GetHTMLURL(), err)
	}

	if *setVariableName != "" {
		azdo.LogCmdSetVariable(*setVariableName, strconv.Itoa(c.GetNumber()))
	}
	return nil
}
