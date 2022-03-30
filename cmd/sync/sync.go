// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
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
	"github.com/microsoft/go-infra/gitcmd"
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

var azdoDncengPAT = flag.String("azdo-dnceng-pat", "", "Use this Azure DevOps PAT to authenticate to dnceng project HTTPS Git URLs.")

// maxDiffLinesToDisplay is the number of lines of the file diff to show in the console log and
// include in the PR description before truncating the remaining lines. A dev branch may have a
// large number of changes that can cause issues like extremely long email notifications, breaking
// Azure Pipelines by being too long to store in an environment variable, and performance hits due
// to the time it takes to log (in particular on Windows terminals). This infra hasn't hit these
// issues, but other tools have hit some, and it seems reasonable to set a limit ahead of time.
const maxDiffLinesToDisplay = 200

var (
	// maxUpstreamCommitMessageInSnippet is the maximum number of characters to include in the
	// commit message snippet for a submodule update commit message. The snippet gives context to
	// the current submodule pointer in a "git log" or when viewed on GitHub. Because the commit had
	// to be accepted into upstream to make it here, the message length is almost definitely ok to
	// include in its entirety. This is only a safeguard, and the character count is arbitrary.
	maxUpstreamCommitMessageInSnippet = 1000
	// snippetCutoffIndicator is the text to put at the end of the snippet when it is cut off.
	snippetCutoffIndicator = "[...]"
)

// GitAuthOption contains a string value given on the command line to indicate what type of auth to
// use with GitHub URLs.
type GitAuthOption string

// String values given on the command line. See usage help for details.
const (
	GitAuthNone GitAuthOption = "none"
	GitAuthSSH  GitAuthOption = "ssh"
	GitAuthPAT  GitAuthOption = "pat"
)

var auther gitcmd.URLAuther

func main() {
	var syncConfig = flag.String("c", "eng/sync-config.json", "The sync configuration file to run.")
	var tempGitDir = flag.String(
		"temp-git-dir",
		filepath.Join(getwdOrPanic(), "eng", "artifacts", "sync-upstream-temp-repo"),
		"Location to create the temporary Git repo. A timestamped subdirectory is created to reduce chance of collision.")

	var gitAuthString = flag.String(
		"git-auth",
		string(GitAuthNone),
		// List valid options. Indent one space, to line up with the automatic ' (default "none")'.
		"The type of Git auth to inject into URLs for fetch/push access. String options:\n"+
			" none - Leave GitHub URLs as they are. Git may use HTTPS authentication in this case.\n"+
			" ssh - Change the GitHub URL to SSH format.\n"+
			" pat - Add the 'github-user' and 'github-pat' values into the URL.\n")

	buildmodel.ParseBoundFlags(description)

	gitAuth := GitAuthOption(*gitAuthString)
	switch gitAuth {
	case GitAuthNone:
		auther = gitcmd.NoAuther{}
		break

	case GitAuthSSH:
		auther = gitcmd.GitHubSSHAuther{}
		break

	case GitAuthPAT:
		missingArgs := false
		if *githubUser == "" {
			fmt.Printf("Error: git-auth pat is specified but github-user is not.")
			missingArgs = true
		}
		if *githubPAT == "" {
			fmt.Printf("Error: git-auth pat is specified but github-pat is not.")
			missingArgs = true
		}
		if missingArgs {
			os.Exit(1)
		}
		auther = gitcmd.MultiAuther{
			Authers: []gitcmd.URLAuther{
				gitcmd.GitHubPATAuther{
					User: *githubUser,
					PAT:  *githubPAT,
				},
				gitcmd.AzDOPATAuther{
					PAT: *azdoDncengPAT,
				},
			},
		}
		break

	default:
		fmt.Printf("Error: git-auth value %q is not an accepted value.\n", *gitAuthString)
		flag.Usage()
		os.Exit(1)
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

// changedBranch stores the refs that have changes that need to be submitted in a PR, and the diff
// of files being changed in the PR for use in the PR body.
type changedBranch struct {
	Refs    *gitpr.SyncPRRefSet
	Diff    string
	PRTitle string
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

	branches := make([]*gitpr.SyncPRRefSet, 0, len(entry.BranchMap))
	for upstream, target := range entry.BranchMap {
		nb := &gitpr.SyncPRRefSet{
			UpstreamName: upstream,
			PRRefSet: gitpr.PRRefSet{
				Name:    strings.ReplaceAll(target, "?", upstream),
				Purpose: "auto-sync",
			},
		}
		branches = append(branches, nb)
	}

	// Fetch latest from remotes. We fetch with one big Git command with many refspecs, instead of
	// simply looping across every branch. This keeps round-trips to a minimum and may benefit from
	// innate Git parallelism. Later in the process, we do a batched "push" for the same reasons.
	//
	// For an overview of the sequence of Git commands below, see the command description.

	fetchUpstream := newGitCmd("fetch", "--no-tags", auther.InsertAuth(entry.Upstream))
	fetchOrigin := newGitCmd("fetch", "--no-tags", auther.InsertAuth(entry.Target))
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

	// Fetch the state of the official/upstream-maintained mirror (if specified) so we can check
	// against it later.
	if entry.UpstreamMirror != "" {
		fetchUpstreamMirror := newGitCmd("fetch", "--no-tags", auther.InsertAuth(entry.UpstreamMirror))
		for _, b := range branches {
			fetchUpstreamMirror.Args = append(fetchUpstreamMirror.Args, b.UpstreamMirrorFetchRefspec())
		}
		if err := run(fetchUpstreamMirror); err != nil {
			return err
		}
	}

	// Before attempting any update, mirror everything (if specified). Only continue once this is
	// complete, to avoid a potentially broken internal build state.
	if entry.MirrorTarget != "" {
		mirror := newGitCmd("push", auther.InsertAuth(entry.MirrorTarget))
		for _, b := range branches {
			mirror.Args = append(mirror.Args, b.UpstreamMirrorRefspec())
		}
		if *dryRun {
			mirror.Args = append(mirror.Args, "-n")
		}

		if err := run(mirror); err != nil {
			return err
		}
	}

	// While looping through the branches and trying to sync, use this slice to keep track of which
	// branches have changes, so we can push changes and submit PRs later.
	changedBranches := make([]changedBranch, 0, len(branches))

	for _, b := range branches {
		fmt.Printf("---- Processing branch %q for entry targeting %v\n", b.Name, entry.Target)

		if err := run(newGitCmd("checkout", b.PRBranch())); err != nil {
			return err
		}

		c := changedBranch{Refs: b}
		var commitMessage string

		if entry.SubmoduleTarget == "" {
			// This is not a submodule update, so merge with the upstream repository.
			if err := run(newGitCmd("merge", "--no-ff", "--no-commit", b.UpstreamLocalBranch())); err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					fmt.Printf("---- Merge hit an ExitError: %q. A non-zero exit code is expected if there were conflicts. The script will try to resolve them, next.\n", exitError)
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
			c.PRTitle = fmt.Sprintf("Merge upstream %#q into %#q", b.UpstreamName, b.Name)
			commitMessage = fmt.Sprintf("Merge upstream branch %q into %v", b.UpstreamName, b.Name)
		} else {
			// This is a submodule update. We'll be doing more evaluation to figure out which commit
			// to update to, so define a helper func with captured context.
			getTrimmedCmdOutput := func(args ...string) (string, error) {
				out, err := combinedOutput(newGitCmd(args...))
				if err != nil {
					return "", err
				}
				return strings.TrimSpace(out), nil
			}

			// This update uses a submodule, so find the latest version of upstream and update the
			// submodule to point at it.
			newCommit, err := getTrimmedCmdOutput("rev-parse", b.UpstreamLocalBranch())
			if err != nil {
				return err
			}

			// Get the latest commit available in every known official location.
			if entry.UpstreamMirror != "" {
				upstreamMirrorCommit, err := getTrimmedCmdOutput("rev-parse", b.UpstreamMirrorLocalBranch())
				if err != nil {
					return err
				}
				if newCommit != upstreamMirrorCommit {
					// Point out mismatches, so we can keep track of them later by searching logs.
					// This happening normally isn't a concern: our sync schedule most likely
					// coincided with the potential time window where a commit has been pushed to
					// Upstream before being pushed to UpstreamMirror.
					fmt.Printf("--- Upstream and upstream mirror commits do not match: %v != %v\n", newCommit, upstreamMirrorCommit)
				}

				commonCommit, err := getTrimmedCmdOutput("merge-base", newCommit, upstreamMirrorCommit)
				if err != nil {
					return err
				}
				fmt.Printf("---- Common commit of upstream and upstream mirror: %v\n", commonCommit)
				newCommit = commonCommit
			}

			// Set the submodule commit directly in the Git index. This avoids the need to
			// init/clone the submodule, which can be time-consuming. Mode 160000 means the tree
			// entry is a submodule.
			cacheInfo := fmt.Sprintf("160000,%v,%v", newCommit, entry.SubmoduleTarget)
			if err := run(newGitCmd("update-index", "--cacheinfo", cacheInfo)); err != nil {
				return err
			}

			upstreamCommitMessage, err := getTrimmedCmdOutput("log", "--format=%B", "-n", "1", newCommit)
			if err != nil {
				return err
			}
			snippet := createCommitMessageSnippet(upstreamCommitMessage)

			c.PRTitle = fmt.Sprintf("Update submodule to latest %#q in %#q", b.UpstreamName, b.Name)
			commitMessage = fmt.Sprintf("Update submodule to latest %v (%v): %v", b.UpstreamName, newCommit[:8], snippet)
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
		if err := run(newGitCmd("commit", "-m", commitMessage)); err != nil {
			return err
		}

		if entry.SubmoduleTarget == "" {
			// Show a summary of which files are in our fork branch vs. upstream. This is just
			// informational. CI is a better place to *enforce* a low diff: it's more visible, can
			// be fixed up more easily, and doesn't block other branch mirror/merge operations.
			diff, err := combinedOutput(newGitCmd(
				"diff",
				"--name-status",
				b.UpstreamLocalBranch(),
				b.PRBranch(),
			))
			if err != nil {
				return err
			}

			// The diff may be large. Truncate it if it seems unreasonable to show on the console, or to
			// include in a PR description. The user can use Git to dig deeper if needed.
			var diffLines strings.Builder
			diffLineScanner := bufio.NewScanner(strings.NewReader(diff))
			for lineNumber := 0; diffLineScanner.Scan(); lineNumber++ {
				if err := diffLineScanner.Err(); err != nil {
					return err
				}
				if lineNumber == maxDiffLinesToDisplay {
					diffLines.WriteString(fmt.Sprintf("Diff truncated: contains more than %v lines.\n", maxDiffLinesToDisplay))
					break
				}
				diffLines.WriteString(diffLineScanner.Text())
				diffLines.WriteString("\n")
			}
			c.Diff = diffLines.String()

			fmt.Printf("---- Files changed from %q to %q ----\n", b.UpstreamName, b.Name)
			fmt.Print(diff)
			fmt.Println("--------")
		}

		changedBranches = append(changedBranches, c)
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
		c.Args = append(c.Args, auther.InsertAuth(remote))
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

			body := "Hi! I'm a bot, and this is an automatically generated upstream sync PR. 🔃" +
				"\n\nAfter submitting the PR, I will attempt to enable auto-merge in the \"merge commit\" configuration.\n\n" +
				"\n\nFor more information, visit [sync documentation in microsoft/go-infra](https://github.com/microsoft/go-infra/tree/main/docs/automation/sync.md)."
			if entry.SubmoduleTarget == "" {
				body += fmt.Sprintf(
					"\n\nThis PR merges %#q into %#q.\n\nIf PR validation fails and you need to fix up the PR, make sure to use a merge commit, not a squash or rebase!",
					b.Refs.UpstreamName, b.Refs.Name,
				)
			}
			if b.Diff != "" {
				body += fmt.Sprintf(
					"\n\n"+
						"<details><summary>Click on this text to view the file difference between this branch and upstream.</summary>\n\n"+
						"```\n%v\n```"+
						"\n\n</details>",
					b.Diff,
				)
			}

			request := b.Refs.CreateGitHubPR(parsedPRHeadRemote.GetOwner(), b.PRTitle, body)

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

func createCommitMessageSnippet(message string) string {
	if i := strings.IndexAny(message, "\r\n"); i >= 0 {
		message = message[:i]
	}
	if len(message) > maxUpstreamCommitMessageInSnippet {
		message = message[:maxUpstreamCommitMessageInSnippet-len(snippetCutoffIndicator)+1] + snippetCutoffIndicator
	}
	return message
}
