// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, new(tagCmd))
}

type tagCmd struct{}

func (t tagCmd) Name() string {
	return "tag"
}

func (t tagCmd) Summary() string {
	return "Create a tag on a GitHub repository."
}

func (t tagCmd) Description() string {
	return `

Using the GitHub API, create a tag on the GitHub repository on a given commit. If the tag already
exists, exit with code 1.
`
}

func (t tagCmd) Handle(p subcmd.ParseFunc) error {
	tag := tagFlag()
	repo := repoFlag()
	pat := githubPATFlag()
	commit := flag.String("commit", "", "The commit hash to tag.")

	if err := p(); err != nil {
		return err
	}

	if *tag == "" {
		return fmt.Errorf("no tag specified")
	}
	if *commit == "" {
		return fmt.Errorf("no commit specified")
	}
	owner, name, err := parseRepoFlag(*repo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, err := githubClient(ctx, *pat)
	if err != nil {
		return err
	}

	ref := "refs/tags/" + *tag
	log.Printf("Creating %q pointing at %q\n", ref, *commit)

	return retry(func() error {
		// The GitHub API returns an error code if the tag already exists. We don't need to
		// check it ourselves.
		_, _, err := client.Git.CreateRef(ctx, owner, name, &github.Reference{
			Ref:    &ref,
			Object: commitObject(*commit),
		})
		return err
	})
}

func commitObject(sha string) *github.GitObject {
	t := "commit"
	return &github.GitObject{
		Type: &t,
		SHA:  &sha,
	}
}
