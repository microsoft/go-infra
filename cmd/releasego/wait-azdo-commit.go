// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops/git"
	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "wait-azdo-commit",
		Summary: "Wait until the commit is available in the specified AzDO repo.",
		Handle:  handleWaitAzDOCommit,
	})
}

func handleWaitAzDOCommit(p subcmd.ParseFunc) error {
	commit := flag.String("commit", "", "[Required] The commit to check for.")
	name := flag.String("name", "", "[Required] The AzDO repo name to query.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")
	azdoFlags := azdo.BindClientFlags()

	if err := p(); err != nil {
		return err
	}

	if *commit == "" {
		flag.Usage()
		log.Fatalln("No commit specified.")
	}
	if *name == "" {
		flag.Usage()
		log.Fatalln("No repo name specified.")
	}
	if err := azdoFlags.EnsureAssigned(); err != nil {
		flag.Usage()
		return err
	}

	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	ctx := context.Background()

	c, err := git.NewClient(ctx, azdoFlags.NewConnection())
	if err != nil {
		return err
	}

	for {
		_, err := c.GetCommit(ctx, git.GetCommitArgs{
			CommitId:     commit,
			RepositoryId: name,
			Project:      azdoFlags.Proj,
		})
		if err == nil {
			log.Println("Found commit.")
			break
		}
		log.Printf("Unable to find commit: %v, next poll in %v...", err, pollDelay)
		time.Sleep(pollDelay)
	}

	return nil
}
