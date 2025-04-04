// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/internal/azurelinux"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-azure-linux",
		Summary: "Create a GitHub PR that updates the Go spec files for Azure Linux.",
		Description: `
Updates the golang package spec file in the Azure Linux GitHub repository to build the version of
Go specified in the provided build asset JSON file. Creates a branch in [owner]/[repo] and submits
the PR to [upstream]/[repo].

If [owner]/[repo] doesn't exist, tries to create the fork in the account associated with the PAT.

Fork creation assumes [owner] matches the PAT user. If the created fork doesn't match
[owner]/[repo], the command fails.

If the user already has a fork of the repo with a different name, fork creation may fail. For
example, if the fork was created before the cbl-mariner repository was renamed to azurelinux, it
may still have the old name.

Note: the PAT must have "repo" scope, and if using a fork, it must also have "workflow" scope.
Otherwise, GitHub will return "404" when attempting to update the fork if the upstream repo has
modified any GitHub workflows.
`,
		Handle: updateAzureLinux,
	})
}

func updateAzureLinux(p subcmd.ParseFunc) error {
	var (
		buildAssetJSON string
		upstream       string
		owner          string
		repo           string
		baseBranch     string
		updateBranch   string
		latestMajor    bool
		notify         string
		security       bool
	)
	flag.StringVar(&buildAssetJSON, "build-asset-json", "assets.json", "The path of a build asset JSON file describing the Go build to update to.")
	flag.StringVar(&upstream, "upstream", "microsoft", "The owner of the Azure Linux repository.")
	flag.StringVar(&owner, "owner", "microsoft", "The owner of the repository to create the dev branch in.")
	flag.StringVar(&repo, "repo", "azurelinux", "The upstream repository name to update.")
	flag.StringVar(&baseBranch, "base-branch", "refs/heads/3.0-dev", "The base branch to download files from.")
	flag.StringVar(&updateBranch, "update-branch", "", "The target branch to update files in.")
	flag.BoolVar(&latestMajor, "latest-major", false, "This is the latest major version, so update 'golang.spec' instead of 'golang-1.<N>.spec'.")
	flag.StringVar(&notify, "notify", "", "A GitHub user to tag in the PR body and request that they finalize the PR, or empty. The value 'ghost' is also treated as empty.")
	flag.BoolVar(&security, "security", false, "Whether to indicate in the PR title and description that this is a security release.")

	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	authorFlag := changelogAuthorFlag()

	if err := p(); err != nil {
		return err
	}

	author, err := changelogAuthor(*authorFlag)
	if err != nil {
		return err
	}

	start := time.Now()
	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	// Set custom user agent to help GitHub identify the bot if necessary.
	client.UserAgent = "microsoft/go-infra update-azure-linux"

	// Check that the PAT has the necessary scopes.
	if *gitHubAuthFlags.GitHubPat != "" {
		patScopes, err := githubutil.PATScopes(ctx, client)
		if err != nil {
			return err
		}
		if !slices.Contains(patScopes, "repo") {
			return fmt.Errorf("the PAT must have 'repo' scope, but not found in list: %v", patScopes)
		}

		if upstream != owner {
			// Submitting PR via fork. Try to make sure it'll work.
			if !slices.Contains(patScopes, "workflow") {
				return fmt.Errorf("the PAT must have 'workflow' scope, but not found in list: %v", patScopes)
			}

			// Get full details about the fork.
			forkRepo, err := githubutil.FetchRepository(ctx, client, owner, repo)
			if err != nil {
				if errors.Is(err, githubutil.ErrRepositoryNotExists) {
					// Fork doesn't exist. Try to create it and get the full details.
					forkRepo, err = githubutil.FullyCreateFork(ctx, client, upstream, repo)
					if err != nil {
						return err
					}
				} else {
					return err
				}
			}
			log.Printf("Submitting PR via owner's fork: %s\n", forkRepo.GetHTMLURL())
		}
	}

	assets, err := loadBuildAssets(buildAssetJSON)
	if err != nil {
		return err
	}

	if updateBranch == "nil" || updateBranch == "" {
		updateBranch = generateUpdateBranchNameFromAssets(assets)
	}

	// If anything fails here, retry from the beginning to use a fresh base commit.
	// Some individual steps also have their own retries; this is fine.
	if err := githubutil.Retry(func() error {
		// Find details about the state of the upstream base branch. This pins the commit that we're
		// working on, making sure our PR won't revert an unfortunately-timed upstream merge.
		upstreamRef, _, err := client.Git.GetRef(ctx, upstream, repo, baseBranch)
		if err != nil {
			return fmt.Errorf("failed to get ref %v: %w", baseBranch, err)
		}
		upstreamCommitSHA := upstreamRef.Object.GetSHA()
		upstreamCommit, _, err := client.Git.GetCommit(ctx, upstream, repo, upstreamCommitSHA)
		if err != nil {
			return fmt.Errorf("failed to get commit %v: %w", upstreamCommitSHA, err)
		}

		fs := githubutil.NewRefFS(ctx, client, upstream, repo, upstreamCommitSHA)
		rm, err := azurelinux.ReadModel(fs)
		if err != nil {
			return err
		}
		v, err := rm.UpdateMatchingVersion(assets, latestMajor, start, author)
		if err != nil {
			return err
		}

		treeFile := func(path string, content []byte) *github.TreeEntry {
			return &github.TreeEntry{
				Path:    github.String(path),
				Content: github.String(string(content)),
				Mode:    github.String(githubutil.TreeModeFile),
			}
		}
		tree := []*github.TreeEntry{
			treeFile(v.SpecPath, v.Spec),
			treeFile(v.SignaturesPath, v.Signatures),
			treeFile(azurelinux.CGManifestPath, rm.CGManifest),
		}

		createTree, _, err := client.Git.CreateTree(ctx, owner, repo, upstreamCommit.Tree.GetSHA(), tree)
		if err != nil {
			return err
		}

		createCommit, _, err := client.Git.CreateCommit(ctx, owner, repo, &github.Commit{
			Message: github.String(azurelinux.GeneratePRTitleFromAssets(assets, security)),
			Parents: []*github.Commit{upstreamCommit},
			Tree:    createTree,
		}, &github.CreateCommitOptions{})
		if err != nil {
			return err
		}

		newRef := &github.Reference{
			Ref:    github.String(updateBranch),
			Object: &github.GitObject{SHA: createCommit.SHA},
		}
		if _, _, err = client.Git.CreateRef(ctx, owner, repo, newRef); err != nil {
			return fmt.Errorf("failed to create ref: %w", err)
		}
		// Now that we've created the ref, we can't retry: the name is taken.
		// This retry loop is over.
		return nil
	}); err != nil {
		return err
	}

	var pr *github.PullRequest

	if err := githubutil.Retry(func() error {
		prHead := updateBranch
		if owner != upstream {
			prHead = owner + ":" + updateBranch
		}
		pr, _, err = client.PullRequests.Create(ctx, upstream, repo, &github.NewPullRequest{
			Title: github.String(azurelinux.GeneratePRTitleFromAssets(assets, security)),
			Head:  &prHead,
			Base:  github.String(baseBranch),
			// We don't know the PR number yet, so pass 0 to use a placeholder.
			Body:  github.String(azurelinux.GeneratePRDescription(assets, latestMajor, security, notify, 0)),
			Draft: github.Bool(true),
		})
		if err != nil {
			return fmt.Errorf("failed to create PR: %w", err)
		}
		// We can't create the PR again (one per ref), so this retry loop is over.
		return nil
	}); err != nil {
		return err
	}

	// Update the PR description with the PR number.
	if err := githubutil.Retry(func() error {
		_, _, err := client.PullRequests.Edit(ctx, upstream, repo, pr.GetNumber(), &github.PullRequest{
			Body: github.String(azurelinux.GeneratePRDescription(assets, latestMajor, security, notify, pr.GetNumber())),
		})
		return err
	}); err != nil {
		return fmt.Errorf("failed to update pull request description: %w", err)
	}

	fmt.Printf("Pull request created successfully: %s\n", pr.GetHTMLURL())

	if err := githubutil.Retry(func() error {
		// This function utilizes the Issues API because in GitHub's API model, pull requests are treated as a special type of issue.
		// While GitHub provides a dedicated PullRequests API, it doesn't currently offer a method for adding labels directly to pull requests.
		//
		// Therefore, we use the Issues.AddLabelsToIssue method, passing the pull request's number (which is equivalent to its issue number)
		// to apply the labels.
		//
		// This approach is a workaround until GitHub potentially adds direct label management for pull requests in their API.
		_, _, err := client.Issues.AddLabelsToIssue(ctx, upstream, repo, pr.GetNumber(), []string{"3.0-dev", "Automatic PR"})
		return err
	}); err != nil {
		// Labeling may require giving an unjustified amount of permission to the bot.
		// It's ok if the PAT is not permitted to do this: count it as success.
		fmt.Printf("Unable to add label to pull request: %v\n", err)
	} else {
		fmt.Printf("Added labels to pull request.\n")
	}

	return nil
}

func generateUpdateBranchNameFromAssets(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf("refs/heads/bot-for-go/dev/go-%s", assets.GoVersion().Full())
}

func loadBuildAssets(assetFilePath string) (*buildassets.BuildAssets, error) {
	assets := new(buildassets.BuildAssets)

	if err := stringutil.ReadJSONFile(assetFilePath, assets); err != nil {
		return nil, fmt.Errorf("error loading build assets: %w", err)
	}

	// Basic validation up front.
	if assets.GoSrcURL == "" {
		return nil, fmt.Errorf("invalid or missing GoSrcUrl in assets.json")
	}
	if assets.GoSrcSHA256 == "" {
		return nil, fmt.Errorf("invalid or missing GoSrcSHA256 in assets.json")
	}

	return assets, nil
}

func changelogAuthorFlag() *string {
	return flag.String(
		"changelog-author", "",
		"The author to mention in the changelog, 'Name <name@example.org>'. Otherwise, the globally configured Git author will be used.")
}

func changelogAuthor(authorFlag string) (string, error) {
	if authorFlag != "" {
		return authorFlag, nil
	}
	return gitcmd.GetGlobalAuthor()
}
