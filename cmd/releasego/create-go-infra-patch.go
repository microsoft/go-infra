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
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
	"golang.org/x/mod/semver"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "create-go-infra-patch",
		Summary: "Create the next patch or minor release for a go-infra module.",
		Description: `

Using the GitHub API, find the highest published stable version for the selected module, calculate
the next patch tag (or minor tag with -minor), and create a release with generated release notes.
Nested module tags include the module directory prefix, for example goinstallscript/v1.3.1.
The module must already have a published stable release. If someone creates the same release first,
GitHub's create-release operation fails and this command exits with an error.
`,
		Handle: handleCreateGoInfraPatch,
	})
}

var modulePatchTagPattern = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

func handleCreateGoInfraPatch(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	module := flag.String("module", ".", "Module directory: ., assets, goinstallscript, telemetry, or telemetry/config.")
	minor := flag.Bool("minor", false, "Advance the minor version and reset the patch version to zero instead of advancing the patch version.")
	dryRun := flag.Bool("dry-run", false, "Print the next release tag without creating it.")

	if err := p(); err != nil {
		return err
	}

	switch *module {
	case ".", "assets", "goinstallscript", "telemetry", "telemetry/config":
	default:
		return fmt.Errorf("unsupported module %q", *module)
	}
	prefix := ""
	if *module != "." {
		prefix = *module + "/"
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

	var latestVersion string
	opts := &github.ListOptions{PerPage: 100}
	for {
		var releases []*github.RepositoryRelease
		var response *github.Response
		if err := githubutil.Retry(func() error {
			var err error
			log.Printf("Fetching releases from %v/%v (page %v)...\n", owner, name, opts.Page)
			releases, response, err = client.Repositories.ListReleases(ctx, owner, name, opts)
			return err
		}); err != nil {
			return err
		}
		for _, release := range releases {
			if release.GetDraft() || release.GetPrerelease() || !strings.HasPrefix(release.GetTagName(), prefix) {
				continue
			}
			version := strings.TrimPrefix(release.GetTagName(), prefix)
			if modulePatchTagPattern.MatchString(version) && (latestVersion == "" || semver.Compare(version, latestVersion) > 0) {
				latestVersion = version
			}
		}
		if response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	if latestVersion == "" {
		return fmt.Errorf("no published stable release found for module %q; create its initial release manually", *module)
	}

	latestTag := prefix + latestVersion
	nextVersion, err := nextModuleVersion(latestVersion, *minor)
	if err != nil {
		return err
	}
	nextTag := prefix + nextVersion

	log.Printf("Latest release: %v\n", latestTag)
	log.Printf("Next release: %v\n", nextTag)
	if *dryRun {
		log.Println("Dry run: not creating release.")
		return nil
	}

	log.Printf("Creating release %v from %v with generated release notes...\n", nextTag, defaultBranch)
	generateReleaseNotes := true
	release := github.CreateReleaseRequest{
		TagName:              nextTag,
		Name:                 new(nextTag),
		TargetCommitish:      new(defaultBranch),
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

func nextModuleVersion(latestTag string, minor bool) (string, error) {
	match := modulePatchTagPattern.FindStringSubmatch(latestTag)
	if match == nil {
		return "", fmt.Errorf("latest release tag %q is not a stable vMAJOR.MINOR.PATCH tag", latestTag)
	}
	component, index := "patch", 3
	if minor {
		component, index = "minor", 2
	}
	number, err := strconv.Atoi(match[index])
	if err != nil {
		return "", fmt.Errorf("parse %s number from %q: %w", component, latestTag, err)
	}
	if number+1 < 0 {
		return "", fmt.Errorf("%s number overflows for %q", component, latestTag)
	}
	if minor {
		return fmt.Sprintf("v%s.%d.0", match[1], number+1), nil
	}
	return fmt.Sprintf("v%s.%s.%d", match[1], match[2], number+1), nil
}
