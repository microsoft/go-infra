// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"log"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "check-limits",
		Summary:     "Check the GitHub API rate limit status.",
		Description: "",
		Handle:      handleCheckLimits,
	})
}

func handleCheckLimits(p subcmd.ParseFunc) error {
	pat := githubutil.BindPATFlag()
	ghClientId := githubutil.BindClientIDFlag()
	ghAppInstallation := githubutil.BindAppInstallationFlag()
	ghAppPrivateKey := githubutil.BindAppPrivateKeyFlag()

	if err := p(); err != nil {
		return err
	}

	ctx := context.Background()
	var err error
	var client *github.Client

	if *ghClientId != "" {
		client, err = githubutil.NewInstallationClient(ctx, *ghClientId, *ghAppInstallation, *ghAppPrivateKey)
		if err != nil {
			return err
		}
	} else {
		client, err = githubutil.NewClient(ctx, *pat)
		if err != nil {
			return err
		}
	}

	return githubutil.Retry(func() error {
		limits, _, err := client.RateLimit.Get(ctx)
		if err != nil {
			return err
		}
		log.Printf("Core: %v\n", limits.Core)
		log.Printf("Search: %v\n", limits.Search)
		return nil
	})
}
