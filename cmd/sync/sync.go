// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/buildmodel"
	"github.com/microsoft/go-infra/gitpr"
)

const description = `
Example: A sync operation dry run:

  go run ./cmd/sync -n

Sync runs a "merge from upstream" and submits it as a PR. This means fetching commits from an
upstream repo and merging them into corresponding branches in a target repo. This is configured in a
config file, by default 'eng/sync-config.json'. For each entry in the configuration:

1. Fetch each SourceBranch 'branch' from 'Upstream' to a local temp repository.
2. Fetch each 'microsoft/{branch}' from 'Target'.
3. Merge each upstream branch 'b' into corresponding 'microsoft/b'.
4. Push each merge commit to 'Head' (or 'Target' if 'Head' isn't specified) with a name that follows
   the pattern 'dev/auto-merge/microsoft/{branch}'.
5. Create a PR in 'Target' that merges the auto-merge branch. If the PR already exists, overwrite.
   (Force push.)

This script creates the temporary repository in 'eng/artifacts/' by default.

To run a subset of the syncs specified in the config file, or to swap out URLs for development
purposes, create a copy of the configuration file and point at it using a '-c' argument.
`

var dryRun = flag.Bool("n", false, "Enable dry run: do not push, do not submit PR.")

var githubUser = flag.String("github-user", "", "Use this github user to submit pull requests.")
var githubPAT = flag.String("github-pat", "", "Submit the PR with this GitHub PAT, if specified.")
var githubPATReviewer = flag.String("github-pat-reviewer", "", "Approve the PR and turn on auto-merge with this PAT, if specified. Required, if github-pat specified.")

func main() {
	var syncConfig = flag.String("c", "eng/sync-config.json", "The sync configuration file to run.")
	var tempGitDir = flag.String(
		"temp-git-dir",
		filepath.Join(getwdOrPanic(), "eng", "artifacts", "sync-upstream-temp-repo"),
		"Location to create the temporary Git repo. A timestamped subdirectory is created to reduce chance of collision.")

	var gitAuthSSH = flag.Bool("git-auth-ssh", false, "If enabled, automatically convert Target GitHub URLs into SSH format for authentication. 'git-auth-pat' is ignored if also specified.")
	var gitAuthPAT = flag.Bool("git-auth-pat", false, "If enabled, automatically modify GitHub URLs to use 'github-user' and 'github-pat' for fetch/push access.")

	buildmodel.ParseBoundFlags(description)

	if *gitAuthPAT {
		missingArgs := false
		if *githubUser == "" {
			fmt.Printf("Error: git-auth-pat is specified but github-user is not.")
			missingArgs = true
		}
		if *githubPAT == "" {
			fmt.Printf("Error: git-auth-pat is specified but github-pat is not.")
			missingArgs = true
		}
		if missingArgs {
			os.Exit(1)
		}
	}

	var entries []SyncConfigEntry
	if err := buildmodel.ReadJSONFile(*syncConfig, &entries); err != nil {
		log.Panic(err)
	}

	if len(entries) == 0 {
		fmt.Printf("No entries found in config file: %v\n", *syncConfig)
	}

	currentRunGitDir, err := buildmodel.MakeWorkDir(*tempGitDir)
	if err != nil {
		log.Panic(err)
	}

	success := true

	for i, entry := range entries {
		syncNum := fmt.Sprintf("%v/%v", i+1, len(entries))
		fmt.Printf("=== Beginning sync %v, from %v -> %v\n", syncNum, entry.Upstream, entry.Target)

		// Add authentication to Target and Upstream URLs if necessary.
		entry.Target = createAuthorizedGitUrl(entry.Target, *gitAuthSSH, *gitAuthPAT)
		entry.Upstream = createAuthorizedGitUrl(entry.Upstream, *gitAuthSSH, *gitAuthPAT)

		if entry.Head == "" {
			entry.Head = entry.Target
		}
		fmt.Printf("--- Head repository for PR: %v\n", entry.Head)

		// Give each entry a unique dir to avoid interfering with others upon failure.
		repositoryDir := path.Join(currentRunGitDir, strconv.Itoa(i))

		if err := syncRepository(repositoryDir, entry); err != nil {
			// Let sync process continue if an error happens with the current entry.
			fmt.Println(err)
			fmt.Printf("=== Failed sync %v\n", syncNum)
			success = false
		}
	}

	fmt.Println()
	if success {
		fmt.Println("Completed successfully.")
	} else {
		fmt.Println("Completed with errors.")
	}
}

// createAuthorizedGitUrl takes a URL, auth options, and returns an authorized URL. The authorized
// URL may be the same as the original URL, depending on the options given and the URL content.
func createAuthorizedGitUrl(url string, gitAuthSSH bool, gitAuthPAT bool) string {
	const githubPrefix = "https://github.com"
	if strings.HasPrefix(url, githubPrefix) {
		targetRepoOwnerSlashName := strings.TrimPrefix(url, githubPrefix)
		if gitAuthSSH {
			url = fmt.Sprintf("git@github.com:%v", targetRepoOwnerSlashName)
		} else if gitAuthPAT {
			url = fmt.Sprintf("https://%v:%v@github.com/%v", *githubUser, *githubPAT, targetRepoOwnerSlashName)
		}
	}
	return url
}

// changedBranch stores the refs that have changes that need to be submitted in a PR, and the diff
// of files being changed in the PR for use in the PR body.
type changedBranch struct {
	Refs *gitpr.SyncPRRefSet
	Diff string
}

func syncRepository(dir string, entry SyncConfigEntry) error {
	if err := run(exec.Command("git", "init", dir)); err != nil {
		return err
	}

	// newGitCmd creates a "git {args}" command that runs in the temp fetch repo Git dir.
	newGitCmd := func(args ...string) *exec.Cmd {
		c := exec.Command("git", args...)
		c.Dir = dir
		return c
	}

	branches := make([]*gitpr.SyncPRRefSet, 0, len(entry.UpstreamMergeBranches)+len(entry.MergeMap))
	for _, b := range entry.UpstreamMergeBranches {
		// Map from upstream branch name to "microsoft/"-prefixed branch name.
		nb := &gitpr.SyncPRRefSet{
			UpstreamName: b,
			PRRefSet: gitpr.PRRefSet{
				Name:    "microsoft/" + strings.ReplaceAll(b, "master", "main"),
				Purpose: "auto-merge",
			},
		}
		branches = append(branches, nb)
	}
	for upstream, target := range entry.MergeMap {
		nb := &gitpr.SyncPRRefSet{
			UpstreamName: upstream,
			PRRefSet: gitpr.PRRefSet{
				Name:    target,
				Purpose: "auto-merge",
			},
		}
		branches = append(branches, nb)
	}

	// Fetch latest from remotes. We fetch with one big Git command with many refspecs, instead of
	// simply looping across every branch. This keeps round-trips to a minimum and may benefit from
	// innate Git parallelism. Later in the process, we do a batched "push" for the same reasons.
	//
	// For an overview of the sequence of Git commands below, see the command description.

	fetchUpstream := newGitCmd("fetch", "--no-tags", entry.Upstream)
	fetchOrigin := newGitCmd("fetch", "--no-tags", entry.Target)
	for _, b := range branches {
		fetchUpstream.Args = append(fetchUpstream.Args, b.UpstreamFetchRefspec())
		fetchOrigin.Args = append(fetchOrigin.Args, b.BaseBranchFetchRefspec())
	}
	if err := run(fetchUpstream); err != nil {
		return err
	}
	if err := run(fetchOrigin); err != nil {
		return err
	}

	// While looping through the branches and trying to sync, use this slice to keep track of which
	// branches have changes, so we can push changes and submit PRs later.
	changedBranches := make([]changedBranch, 0, len(branches))

	for _, b := range branches {
		fmt.Printf("---- Processing branch '%v' for entry targeting %v\n", b.Name, entry.Target)

		if err := run(newGitCmd("checkout", b.PRBranch())); err != nil {
			return err
		}

		if err := run(newGitCmd("merge", "--no-ff", "--no-commit", b.UpstreamLocalBranch())); err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				fmt.Printf("---- Merge hit an ExitError: '%v'. A non-zero exit code is expected if there were conflicts. The script will try to resolve them, next.\n", exitError)
			} else {
				// Make sure we don't ignore more than we intended.
				return err
			}
		}

		if len(entry.AutoResolveTarget) > 0 {
			// Automatically resolve conflicts in specific project doc files. Use '--no-overlay' to make
			// sure we delete new files in e.g. '.github' that are in upstream but don't exist locally.
			// '--ours' auto-deletes if upstream modifies a file that we deleted in our branch.
			if err := run(newGitCmd(append([]string{"checkout", "--no-overlay", "--ours", "HEAD", "--"}, entry.AutoResolveTarget...)...)); err != nil {
				return err
			}
		}

		// Check if there are any files in the stage. If not, we don't need to process this branch
		// anymore, because the merge + autoresolve didn't change anything.
		if err := run(newGitCmd("diff", "--cached", "--quiet")); err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				fmt.Printf("---- Detected changes in Git stage. Continuing to commit and submit PR.\n")
			} else {
				// Make sure we don't ignore more than we intended.
				return err
			}
		} else {
			// If the diff had 0 exit code, there are no changes. Skip this branch's next steps.
			fmt.Printf("---- No changes to sync for %v. Skipping.\n", b.Name)
			continue
		}

		// If we still have unmerged files, 'git commit' will exit non-zero, causing the script to
		// exit. This prevents the script from pushing a bad merge.
		if err := run(newGitCmd("commit", "-m", "Merge upstream branch '"+b.UpstreamName+"' into "+b.Name)); err != nil {
			return err
		}

		// Show a summary of which files are in our branch vs. upstream. This is just informational.
		// CI is a better place to *enforce* a low diff: it's more visible, can be fixed up more
		// easily, and doesn't block other branch mirror/merge operations.
		diff, err := combinedOutput(newGitCmd(
			"diff",
			"--name-status",
			b.UpstreamLocalBranch(),
			b.PRBranch(),
		))
		if err != nil {
			return err
		}

		fmt.Printf("---- Files changed from '%v' to '%v' ----\n", b.UpstreamName, b.Name)
		fmt.Print(diff)
		fmt.Println("--------")

		changedBranches = append(changedBranches, changedBranch{
			Refs: b,
			Diff: diff,
		})
	}

	if len(changedBranches) == 0 {
		fmt.Println("Checked branches for changes to sync: none found.")
		fmt.Println("Success.")
		return nil
	}

	newGitPushCommand := func(remote string, force bool, refspecs []string) *exec.Cmd {
		c := newGitCmd("push")
		if force {
			c.Args = append(c.Args, "--force")
		}
		c.Args = append(c.Args, remote)
		for _, r := range refspecs {
			c.Args = append(c.Args, r)
		}
		if *dryRun {
			c.Args = append(c.Args, "-n")
		}
		return c
	}

	// Force push the merge branches. We can't do a fast-forward push: our new merge commit is based
	// on "origin", not "to", so if "to" has any commits, they aren't in our commit's history.
	//
	// Even if we did base our branch on "to", we'd hit undesired behaviors if the branch still has
	// changes from an old PR. There are ways to handle this, but no clear benefit. Force push is
	// simple and makes the PR flow simple.
	mergePushRefspecs := make([]string, 0, len(changedBranches))
	for _, b := range changedBranches {
		mergePushRefspecs = append(mergePushRefspecs, b.Refs.PRBranchRefspec())
	}
	if err := run(newGitPushCommand(entry.Head, true, mergePushRefspecs)); err != nil {
		return err
	}

	// All Git operations are complete! Next, ensure there's a GitHub PR for each auto-merge branch.

	// Accumulate overall failure. This lets PR submission continue even if there's a problem for a
	// specific branch.
	var prFailed bool

	// Parse the URLs involved in the PR to get segment information.
	parsedPRTargetRemote, err := gitpr.ParseRemoteURL(entry.Target)
	if err != nil {
		return err
	}
	parsedPRHeadRemote, err := gitpr.ParseRemoteURL(entry.Head)
	if err != nil {
		return err
	}

	for _, b := range changedBranches {
		var skipReason string
		switch {
		case *dryRun:
			skipReason = "Dry run"

		case *githubUser == "":
			skipReason = "github-user not provided"
		case *githubPAT == "":
			skipReason = "github-pat not provided"

		case *githubPATReviewer == "":
			// In theory, if we have githubPAT but no reviewer, we can submit the PR but skip
			// reviewing it/enabling auto-merge. However, this doesn't seem very useful, so it isn't
			// implemented.
			skipReason = "github-pat-reviewer not provided"
		}

		prFlowDescription := fmt.Sprintf("%v -> %v", b.Refs.UpstreamName, b.Refs.PRBranch())

		if skipReason != "" {
			fmt.Printf("---- %s: skipping submitting PR for %v\n", skipReason, prFlowDescription)
			continue
		}

		// err contains any err we get from running the sequence of GitHub PR submission API calls.
		//
		// This uses an immediately invoked anonymous function for convenience/maintainability. We
		// can 'return err' from anywhere in the function, to keep control flow simple. Also, we can
		// capture vars from the 'main()' scope rather than making them global or explicitly passing
		// each one into a named function.
		err := func() error {
			fmt.Printf("---- PR for %v: Submitting...\n", prFlowDescription)

			title := fmt.Sprintf("Merge upstream `%v` into `%v`", b.Refs.UpstreamName, b.Refs.Name)
			body := fmt.Sprintf(
				"🔃 This is an automatically generated PR merging upstream `%v` into `%v`.\n\n"+
					"This PR should auto-merge itself when PR validation passes. If CI fails and you need to make fixups, be sure to use a merge commit, not a squash or rebase!\n\n"+
					"---\n\n"+
					"After these changes, the difference between upstream and the branch is:\n\n"+
					"```\n%v\n```",
				b.Refs.UpstreamName,
				b.Refs.Name,
				strings.TrimSpace(b.Diff),
			)
			request := b.Refs.CreateGitHubPR(parsedPRHeadRemote.GetOwner(), title, body)

			// POST the PR. The call returns success if the PR is created or if we receive a
			// specific error message back from GitHub saying the PR is already created.
			pr, err := gitpr.PostGitHub(parsedPRTargetRemote.GetOwnerSlashRepo(), request, *githubPAT)
			fmt.Printf("%+v\n", pr)
			if err != nil {
				return err
			}

			if pr.AlreadyExists {
				fmt.Println("---- A PR already exists. Attempting to find it...")
				pr.NodeID, err = gitpr.FindExistingPR(
					request,
					parsedPRHeadRemote,
					parsedPRTargetRemote,
					b.Refs.PRBranch(),
					*githubUser,
					*githubPAT)
				if err != nil {
					return err
				}
				if pr.NodeID == "" {
					return fmt.Errorf("no PR found")
				}
			} else {
				fmt.Printf("---- Submitted brand new PR: %v\n", pr.HTMLURL)

				fmt.Printf("---- Approving with reviewer account...\n")
				if err = gitpr.ApprovePR(pr.NodeID, *githubPATReviewer); err != nil {
					return err
				}
			}

			fmt.Printf("---- Enabling auto-merge with reviewer account...\n")
			if err = gitpr.EnablePRAutoMerge(pr.NodeID, *githubPATReviewer); err != nil {
				return err
			}

			fmt.Printf("---- PR for %v: Done.\n", prFlowDescription)
			return nil
		}()

		// If we got an error, don't panic! Log the error and set a flag to indicate it happened,
		// then continue to process the next branch in the for loop.
		//
		// Say we are syncing branches main, go1.15, and go1.16. We're in the go1.15 iteration. For
		// some reason, GitHub errored out when we submitted the PR for go1.15. If we panic, the
		// script terminates before trying to submit a PR for go1.16, even though that one might
		// work fine. That's not ideal. But worse, if the error persists and happens again when we
		// try to update go1.15 in future runs of this script, go1.16 will never get synced. This is
		// why we want to try to keep processing branches.
		if err != nil {
			fmt.Println(err)
			prFailed = true
			continue
		}
	}

	// If PR submission failed for any branch, exit the overall script with NZEC.
	if prFailed {
		return fmt.Errorf("failed to submit one or more PRs")
	}

	return nil
}

// getwdOrPanic gets the current working dir or panics, for easy use in expressions.
func getwdOrPanic() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Panic(err)
	}
	return wd
}

// run sets up the command so it logs directly to our stdout/stderr streams, then runs it.
func run(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// combinedOutput returns the output string of c.CombinedOutput.
func combinedOutput(c *exec.Cmd) (string, error) {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	out, err := c.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
