// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"regexp"
	"strconv"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "create-go-infra-patch",
		Summary: "Create the next v0.0.x release for go-infra.",
		Description: `

Using the GitHub API, find the latest published release, calculate the next v0.0.x patch tag, and
create a release with generated release notes. If someone creates the same release first, GitHub's
create-release operation fails and this command exits with an error.
`,
		Handle: handleCreateGoInfraPatch,
	})
}

var goInfraPatchTagPattern = regexp.MustCompile(`^v0\.0\.(\d+)$`)

func handleCreateGoInfraPatch(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	dryRun := flag.Bool("dry-run", false, "Print the next release tag without creating it.")

	if err := p(); err != nil {
		return err
	}

	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	repository, err := githubutil.FetchRepository(ctx, client, owner, name)
	if err != nil {
		return err
	}
	defaultBranch := repository.GetDefaultBranch()
	if defaultBranch == "" {
		return errors.New("repository default branch is empty")
	}

	var latestRelease *github.RepositoryRelease
	if err := githubutil.Retry(func() error {
		var err error
		log.Printf("Fetching latest release from %v/%v...\n", owner, name)
		latestRelease, _, err = client.Repositories.GetLatestRelease(ctx, owner, name)
		return err
	}); err != nil {
		return err
	}

	latestTag := latestRelease.GetTagName()
	nextTag, err := nextGoInfraPatchTag(latestTag)
	if err != nil {
		return err
	}

	log.Printf("Latest release: %v\n", latestTag)
	log.Printf("Next release: %v\n", nextTag)
	if *dryRun {
		log.Println("Dry run: not creating release.")
		return nil
	}

	log.Printf("Creating release %v from %v with generated release notes...\n", nextTag, defaultBranch)
	generateReleaseNotes := true
	release := &github.RepositoryRelease{
		TagName:              github.String(nextTag),
		Name:                 github.String(nextTag),
		TargetCommitish:      github.String(defaultBranch),
		GenerateReleaseNotes: &generateReleaseNotes,
	}
	if err := githubutil.Retry(func() error {
		created, _, err := client.Repositories.CreateRelease(ctx, owner, name, release)
		if err != nil {
			return err
		}
		log.Printf("Created release: %v\n", created.GetHTMLURL())
		return nil
	}); err != nil {
		return fmt.Errorf("create release %v: %w", nextTag, err)
	}

	return nil
}

func nextGoInfraPatchTag(latestTag string) (string, error) {
	match := goInfraPatchTagPattern.FindStringSubmatch(latestTag)
	if match == nil {
		return "", fmt.Errorf("latest release tag %q is not a v0.0.x tag", latestTag)
	}
	patch, err := strconv.Atoi(match[1])
	if err != nil {
		return "", fmt.Errorf("parse patch number from %q: %w", latestTag, err)
	}
	return fmt.Sprintf("v0.0.%d", patch+1), nil
}
