// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "repo-release",
		Summary: "Create a release on a GitHub repository.",
		Description: `

Using the GitHub API, create a release on the GitHub repository using a given tag name. Attach the
given build asset JSON file and the artifacts it lists that are found in the specified directory.
`,
		Handle: handleRepoRelease,
	})
}

func handleRepoRelease(p subcmd.ParseFunc) error {
	tag := tagFlag()
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	buildAssetJSON := flag.String("build-asset-json", "", "[Required] The build asset JSON file to release.")
	buildDir := flag.String("build-dir", "", "[Required] The directory containing build artifacts to attach.")

	if err := p(); err != nil {
		return err
	}

	if *tag == "" {
		return fmt.Errorf("no tag specified")
	}
	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}
	if *buildAssetJSON == "" {
		return errors.New("no build asset json specified")
	}
	if *buildDir == "" {
		return errors.New("no build dir specified")
	}

	var assets buildassets.BuildAssets
	if err := stringutil.ReadJSONFile(*buildAssetJSON, &assets); err != nil {
		return err
	}
	uploadPaths := assetPaths(*buildDir, assets.GoSrcURL)
	uploadPaths = append(uploadPaths, *buildAssetJSON)
	log.Println("First, creating draft release. Then, attaching these files before marking release ready:")
	for _, p := range uploadPaths {
		log.Println(p)
	}

	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	log.Printf("Creating draft release %v...\n", *tag)

	var release *github.RepositoryRelease
	if err := githubutil.Retry(func() error {
		release, _, err = client.Repositories.CreateRelease(ctx, owner, name, draftRelease(tag))
		return err
	}); err != nil {
		return fmt.Errorf("unable to create draft release: %w", err)
	}

	// If we don't make it to the end of the func, clean up our partial-progress draft.
	defer func() {
		if release != nil {
			log.Println("Cleaning up draft release.")
			if err := githubutil.Retry(func() error {
				_, err = client.Repositories.DeleteRelease(ctx, owner, name, *release.ID)
				return err
			}); err != nil {
				// Log error and ignore. We're only trying to clean up.
				log.Printf("Error trying to clean up draft release: %v", err)
			}
		}
	}()

	for _, p := range uploadPaths {
		filename := filepath.Base(p)
		log.Printf("Attaching (uploading) %#q\n", filename)
		if err := githubutil.Retry(func() error {
			file, err := os.Open(p)
			if err != nil {
				return err
			}
			defer file.Close()

			_, _, err = client.Repositories.UploadReleaseAsset(
				ctx,
				owner, name,
				*release.ID,
				&github.UploadOptions{Name: filename},
				file)
			return err
		}); err != nil {
			return fmt.Errorf("failed to upload %#q to release %v: %w", p, *release.ID, err)
		}
	}

	log.Println("Marking release as ready (non-draft)...")
	if err := githubutil.Retry(func() error {
		r, _, err := client.Repositories.EditRelease(ctx, owner, name, *release.ID, undraftEditRelease())
		if err != nil {
			return err
		}
		log.Printf("Created: %v\n", *r.HTMLURL)
		return nil
	}); err != nil {
		return fmt.Errorf("unable to make release ready: %w", err)
	}

	// Disable deferred cleanup by clearing the ref. We made it!
	release = nil
	return nil
}

func draftRelease(tag *string) *github.RepositoryRelease {
	body := "Microsoft build of Go " + *tag
	draft := true
	return &github.RepositoryRelease{
		TagName: tag,
		Name:    tag,
		Body:    &body,
		Draft:   &draft,
	}
}

func undraftEditRelease() *github.RepositoryRelease {
	draft := false
	return &github.RepositoryRelease{
		Draft: &draft,
	}
}

func assetPaths(dir, goSrcURL string) []string {
	p := make([]string, 0, 3)
	p = appendPathAndVerificationFilePaths(p, goSrcURL)
	for i, url := range p {
		// Take filename from the URL (always '/') and find in the local dir (platform separator).
		p[i] = filepath.Join(dir, path.Base(url))
	}
	return p
}
