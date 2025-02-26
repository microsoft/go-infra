// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"log"

	"github.com/microsoft/go-infra/gitcmd"
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
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags()

	if err := p(); err != nil {
		return err
	}

	ctx := context.Background()
	client, err := gitcmd.NewClientFromFlags(gitHubAuthFlags, ctx)
	if err != nil {
		return err
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
