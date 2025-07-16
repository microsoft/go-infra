// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/subcmd"
	"github.com/microsoft/go-infra/submodule"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "review",
		Summary: "Review a GitHub PR's patch changes locally.",
		Description: `
This command helps review a PR's patch file changes. It takes a GitHub PR URL (like 
https://github.com/microsoft/go/pull/1234), and sets up a stage diff to review the changes.

Auth is not required. Attempts to use anonymous access if auth isn't specified.

Under the hood, it:
1. Gets the PR details from GitHub (base branch and PR branch)
2. Fetches the specific commits
3. Uses Git to check out the patch files to a temporary directory
3. Runs "git go-patch apply -before" using the merge base commit
4. Runs "git go-patch apply -after" using the PR branch commit
5. Runs "git go-patch stage-diff" to prepare the changes for review

(It doesn't literally execute shell commands, but uses the same logic.)

Note that this command doesn't check out commits in the outer repository. However, it does
modify the state of your submodule, like the "stage-diff" command.

The result is that the submodule will be in a state where the staged changes represent 
the patch modifications introduced by the PR, ready for review using Git tools like 
"git diff --cached" or IDE diff viewers.
` + repoRootSearchDescription,
		Handle: handleReviewGH,
	})
}

func handleReviewGH(p subcmd.ParseFunc) error {
	prURL := flag.String(
		"url", "",
		"The GitHub PR URL to review, format https://github.com/microsoft/go/pull/<number>[extraneous].\n"+
			"If not provided, interactively prompts. The prompt may make it easier to avoid shell quoting/escaping issues.\n"+
			"In the last segment, anything starting with / or # is ignored, for more flexibility when copying PR URLs.")
	authFlags := githubutil.BindGitHubAuthFlags("")
	force := flag.Bool("f", false, "Force apply: throw away changes in the submodule.")
	yes := flag.Bool("y", false, "Skip confirmation prompt.")
	keepWork := flag.Bool("w", false, "Keep the temporary work directory with patch files.")

	if err := p(); err != nil {
		return err
	}

	urlToUse := *prURL
	if urlToUse == "" {
		fmt.Print("Enter GitHub PR URL: ")
		var err error
		urlToUse, err = readStdinLine()
		if err != nil {
			return fmt.Errorf("failed to read PR URL: %v", err)
		}
		if urlToUse == "" {
			return fmt.Errorf("PR URL cannot be empty")
		}
	}

	owner, repo, prNum, err := parsePRURL(urlToUse)
	if err != nil {
		return fmt.Errorf("invalid PR URL format: %v\nExpected format: https://github.com/owner/repo/pull/number", err)
	}

	// Create a GitHub client
	ctx := context.Background()

	var client *github.Client
	client, err = authFlags.NewClient(ctx)
	if err != nil {
		if errors.Is(err, githubutil.ErrNoAuthProvided) {
			client = github.NewClient(nil)
		} else {
			return fmt.Errorf("failed to create GitHub client: %v", err)
		}
	}

	// Get PR information from GitHub
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, prNum)
	if err != nil {
		return fmt.Errorf("failed to get PR information: %v", err)
	}

	// Extract base and head branch information
	baseBranch := pr.GetBase().GetRef()
	baseSHA := pr.GetBase().GetSHA()
	headBranch := pr.GetHead().GetRef()
	headSHA := pr.GetHead().GetSHA()

	fmt.Printf("\nFound %v/%v PR #%d: %s\n", owner, repo, prNum, pr.GetTitle())
	fmt.Printf("Base branch: %q %s\n", baseBranch, baseSHA)
	fmt.Printf("Head branch: %q %s\n", headBranch, headSHA)

	if !*yes {
		fmt.Printf("Are you sure you want to stage the changes? (y/N): ")
		var confirmation string
		_, err := fmt.Scanln(&confirmation)
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %v", err)
		}
		confirmation = strings.TrimSpace(confirmation)
		if strings.ToLower(confirmation) != "y" {
			log.Println("Review aborted by user.")
			return nil
		}
	}

	config, err := loadConfig()
	if err != nil {
		return err
	}
	rootDir, goDir := config.FullProjectRoots()

	log.Println("Fetching PR branch commits...")

	ownerRemoteURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)

	// Fetch the actual commits without creating refs. Both commits are
	// available through the base repo because a PR must have come from another
	// repo in the repository fork network.
	if err := gitcmd.Run(rootDir, "fetch", ownerRemoteURL, baseSHA, headSHA); err != nil {
		return fmt.Errorf("failed to fetch commits: %v", err)
	}

	baseSubmoduleSHA, err := gitcmd.GetSubmoduleCommitAtRev(rootDir, goDir, baseSHA)
	if err != nil {
		return fmt.Errorf("failed to get base submodule commit: %v", err)
	}
	headSubmoduleSHA, err := gitcmd.GetSubmoduleCommitAtRev(rootDir, goDir, headSHA)
	if err != nil {
		return fmt.Errorf("failed to get head submodule commit: %v", err)
	}
	log.Printf("Base submodule commit: %s\n", baseSubmoduleSHA)
	log.Printf("Head submodule commit: %s\n", headSubmoduleSHA)

	// The base branch is just the target. We need to find the actual merge-base
	// to avoid adding unrelated changes to the staged review. Now that we've
	// pulled both commits locally, we must also have the shared base, and we
	// don't need to use another GitHub API call to get this done.
	mergeBaseCommit, err := gitcmd.CombinedOutput(
		rootDir,
		"merge-base",
		pr.GetBase().GetSHA(),
		pr.GetHead().GetSHA(),
	)
	if err != nil {
		return fmt.Errorf("failed to find merge base between head and base branches: %v", err)
	}
	mergeBaseCommit = strings.TrimSpace(mergeBaseCommit)

	// Create a temporary directory to store before/after patch files.
	tempDir, err := os.MkdirTemp("", "git-go-patch-review-patches-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory for patches: %v", err)
	}
	if !*keepWork {
		defer func() {
			// Best effort cleanup.
			err := os.RemoveAll(tempDir)
			if err != nil {
				log.Printf("Failed to remove temporary directory %q: %v", tempDir, err)
			}
		}()
	}

	headPatchDir := filepath.Join(tempDir, "head")
	if err := os.MkdirAll(headPatchDir, 0o755); err != nil {
		return fmt.Errorf("failed to create temporary patch directory for head: %v", err)
	}
	basePatchDir := filepath.Join(tempDir, "base")
	if err := os.MkdirAll(basePatchDir, 0o755); err != nil {
		return fmt.Errorf("failed to create temporary patch directory for base: %v", err)
	}

	// Check out patch files into temp dir.
	//
	// Small assumption: the layout of the repository hasn't changed between the
	// commit the user happens to have checked out and the PR to review.
	if err := gitcmd.CheckoutRevToTargetDir(rootDir, mergeBaseCommit, config.PatchesDir, basePatchDir); err != nil {
		return fmt.Errorf("failed to check out base patch files into temp directory: %v", err)
	}
	if err := gitcmd.CheckoutRevToTargetDir(rootDir, headSHA, config.PatchesDir, headPatchDir); err != nil {
		return fmt.Errorf("failed to check out head patch files into temp directory: %v", err)
	}

	log.Println("Applying base branch patches...")
	if err := resetSubmoduleTo(rootDir, goDir, baseSubmoduleSHA, *force); err != nil {
		return err
	}
	if err := applyPatchCommits(goDir, filepath.Join(basePatchDir, config.PatchesDir)); err != nil {
		return fmt.Errorf("failed to apply base branch patches: %v", err)
	}
	if err := createBranch(goDir, stageDiffBeforeBranch); err != nil {
		return err
	}

	log.Println("Applying PR branch patches...")
	if err := resetSubmoduleTo(rootDir, goDir, headSubmoduleSHA, *force); err != nil {
		return err
	}
	if err := applyPatchCommits(goDir, filepath.Join(headPatchDir, config.PatchesDir)); err != nil {
		return fmt.Errorf("failed to apply PR branch patches: %v", err)
	}
	if err := createBranch(goDir, stageDiffAfterBranch); err != nil {
		return err
	}

	// Delete the status files. (Best effort.) This will make sure the rebase
	// and extract subcommands won't run: it isn't clear what that should really
	// do in this state.
	_ = os.RemoveAll(config.FullStatusFileDir())

	log.Println("Setting up diff for review...")
	if err := runStageDiff(goDir, stageDiffBeforeBranch, stageDiffAfterBranch); err != nil {
		return fmt.Errorf("failed to set up stage diff for review: %v", err)
	}

	fmt.Println("\nReview setup complete!")
	fmt.Println("You can now review the changes using:")
	fmt.Println("  - git diff --cached (in the submodule)")
	fmt.Println("  - Your IDE's Git diff viewer")
	fmt.Printf("  - cd %s && git status (to see affected files)\n", goDir)
	fmt.Printf("\nWhen you're done, run 'git go-patch apply -f' to reset the submodule.\n")

	if baseSubmoduleSHA != headSubmoduleSHA {
		fmt.Printf("\nWARNING: The submodule commit has changed from %s to %s.\n", baseSubmoduleSHA, headSubmoduleSHA)
		fmt.Println("The staged changes may not all be caused by the patch differences.")
	}

	return nil
}

// resetSubmoduleTo resets the submodule (or fails if dirty) then checks out the
// target commit.
func resetSubmoduleTo(rootDir, goDir, commit string, force bool) error {
	if err := submodule.Reset(rootDir, goDir, force); err != nil {
		return fmt.Errorf("failed to reset submodule: %v", err)
	}
	if err := gitcmd.Run(goDir, "checkout", commit); err != nil {
		return fmt.Errorf("failed to check out commit in submodule: %v", err)
	}
	return nil
}

func applyPatchCommits(goDir, patchDir string) error {
	args := []string{"am", "--whitespace=nowarn"}
	if err := patch.WalkPatches(patchDir, func(file string) error {
		args = append(args, file)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to walk patches: %v", err)
	}
	return gitcmd.Run(goDir, args...)
}

func parsePRURL(urlStr string) (owner, repo string, prNum int, err error) {
	fail := func(err error) (string, string, int, error) {
		return "", "", 0, fmt.Errorf("invalid PR URL: %s", err)
	}
	// Clean up: trim whitespace and trailing slashes.
	urlStr = strings.TrimSpace(urlStr)
	urlStr = strings.TrimRight(urlStr, "/")

	suffix, ok := strings.CutPrefix(urlStr, "https://github.com/")
	if !ok {
		return fail(fmt.Errorf("URL must start with https://github.com/"))
	}
	owner, suffix, ok = strings.Cut(suffix, "/")
	if !ok {
		return fail(fmt.Errorf("URL must contain owner name"))
	}
	repo, suffix, ok = strings.Cut(suffix, "/")
	if !ok {
		return fail(fmt.Errorf("URL must contain repository name"))
	}
	suffix, ok = strings.CutPrefix(suffix, "pull/")
	if !ok {
		return fail(fmt.Errorf("URL must contain 'pull/' before PR number: %v", suffix))
	}
	// Ignore anything after another / (.../files) or # (...#issuecomment...).
	suffix, _, _ = strings.Cut(suffix, "/")
	suffix, _, _ = strings.Cut(suffix, "#")

	prNum, err = strconv.Atoi(suffix)
	if err != nil {
		return fail(fmt.Errorf("PR number must be an integer: %v", err))
	}
	return owner, repo, prNum, nil
}

func readStdinLine() (string, error) {
	var line string
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		line = scanner.Text()
	} else {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("failed to read input: %v", err)
		}
		return "", fmt.Errorf("no input provided")
	}
	if line == "" {
		return "", fmt.Errorf("input cannot be empty")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("input cannot be just whitespace")
	}
	return line, nil
}
