// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"log"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "report",
		Summary: "Add a comment to a GitHub issue to report status",
		Description: `

Creates a comment on the given GitHub issue. This should be used to report on release status.

If AzDO env variables SYSTEM_COLLECTIONURI, SYSTEM_TEAMPROJECT, and BUILD_BUILDID are set, includes
a link to the build as markdown before the message.
`,
		Handle: handleReport,
	})
}

func handleReport(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	pat := githubutil.BindPATFlag()
	issue := flag.Int("i", 0, "[Required] The issue number to add the comment to.")
	message := flag.String("m", "", "[Required] The message to post in the comment.")

	if err := p(); err != nil {
		return err
	}

	if *issue == 0 {
		return errors.New("no issue specified")
	}
	if *message == "" {
		return errors.New("no message specified")
	}
	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	if url := getEnvBuildURL(); url != "" {
		*message = "[" + getEnvBuildID() + "](" + url + "): " + *message
	}

	log.Printf("Creating comment on #%v with content:\n%v\n", *issue, *message)

	return githubutil.Retry(func() error {
		c, _, err := client.Issues.CreateComment(
			ctx, owner, name, *issue, &github.IssueComment{Body: message})
		if err != nil {
			return err
		}
		log.Printf("Comment: %v\n", *c.HTMLURL)
		return nil
	})
}
