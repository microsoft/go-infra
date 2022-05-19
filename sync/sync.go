// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"bufio"
	"flag"
	"fmt"
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

type Flags struct {
	DryRun *bool

	GitHubUser        *string
	GitHubPAT         *string
	GitHubPATReviewer *string

	AzDODncengPAT *string

	SyncConfig *string
	TempGitDir *string

	GitAuthString *string
}

func BindFlags(workingDirectory string) *Flags {
	return &Flags{
		DryRun: flag.Bool("n", false, "Enable dry run: do not push, do not submit PR."),

		GitHubUser:        flag.String("github-user", "", "Use this github user to submit pull requests."),
		GitHubPAT:         flag.String("github-pat", "", "Submit the PR with this GitHub PAT, if specified."),
		GitHubPATReviewer: flag.String("github-pat-reviewer", "", "Approve the PR and turn on auto-merge with this PAT, if specified. Required, if github-pat specified."),

		AzDODncengPAT: flag.String("azdo-dnceng-pat", "", "Use this Azure DevOps PAT to authenticate to dnceng project HTTPS Git URLs."),

		SyncConfig: flag.String("c", "eng/sync-config.json", "The sync configuration file to run."),
		TempGitDir: flag.String(
			"temp-git-dir",
			filepath.Join(workingDirectory, "eng", "artifacts", "sync-upstream-temp-repo"),
			"Location to create the temporary Git repo. A timestamped subdirectory is created to reduce chance of collision."),

		GitAuthString: flag.String(
			"git-auth",
			string(GitAuthNone),
			// List valid options. Indent one space, to line up with the automatic ' (default "none")'.
			"The type of Git auth to inject into URLs for fetch/push access. String options:\n"+
				" none - Leave GitHub URLs as they are. Git may use HTTPS authentication in this case.\n"+
				" ssh - Change the GitHub URL to SSH format.\n"+
				" pat - Add the 'github-user' and 'github-pat' values into the URL.\n"),
	}
}

func (f *Flags) ParseAuth() (gitcmd.URLAuther, error) {
	switch GitAuthOption(*f.GitAuthString) {
	case GitAuthNone:
		return gitcmd.NoAuther{}, nil

	case GitAuthSSH:
		return gitcmd.GitHubSSHAuther{}, nil

	case GitAuthPAT:
		var missingArgs string
		if *f.GitHubUser == "" {
			missingArgs += " git-auth pat is specified but github-user is not."
		}
		if *f.GitHubPAT == "" {
			missingArgs += " git-auth pat is specified but github-pat is not."
		}
		if missingArgs != "" {
			return nil, fmt.Errorf("missing command-line args:%v", missingArgs)
		}
		return gitcmd.MultiAuther{
			Authers: []gitcmd.URLAuther{
				gitcmd.GitHubPATAuther{
					User: *f.GitHubUser,
					PAT:  *f.GitHubPAT,
				},
				gitcmd.AzDOPATAuther{
					PAT: *f.AzDODncengPAT,
				},
			},
		}, nil
	}
	return nil, fmt.Errorf("git-auth value %q is not an accepted value.\n", *f.GitAuthString)
}

func (f *Flags) MakeGitWorkDir() (string, error) {
	d, err := buildmodel.MakeWorkDir(*f.TempGitDir)
	if err != nil {
		return "", fmt.Errorf("failed to make working directory for sync: %w", err)
	}
	return d, nil
}

func (f *Flags) ReadConfig() ([]ConfigEntry, error) {
	var entries []ConfigEntry
	if err := buildmodel.ReadJSONFile(*f.SyncConfig, &entries); err != nil {
		return nil, fmt.Errorf("failed to read sync config file: %w", err)
	}
	return entries, nil
}

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

func MakePRs(f *Flags) error {
	entries, err := f.ReadConfig()
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Printf("No entries found in config file: %v\n", *f.SyncConfig)
	}

	currentRunGitDir, err := f.MakeGitWorkDir()
	if err != nil {
		return err
	}

	// If there's an auth error, catch it here rather than repeatedly printing it for each sync entry.
	if _, err := f.ParseAuth(); err != nil {
		return err
	}

	success := true

	for i, entry := range entries {
		syncNum := fmt.Sprintf("%v/%v", i+1, len(entries))
		fmt.Printf("=== Beginning sync %v, from %v -> %v\n", syncNum, entry.Upstream, entry.Target)

		fmt.Printf("--- Repository for PR branch: %v\n", entry.PRBranchStorageRepo())

		// Give each entry a unique dir to avoid interfering with others upon failure.
		repositoryDir := path.Join(currentRunGitDir, strconv.Itoa(i))

		if _, err := MakeBranchPRs(f, repositoryDir, &entry); err != nil {
			// Let sync process continue if an error happens with the current entry.
			fmt.Println(err)
			fmt.Printf("=== Failed sync %v\n", syncNum)
			success = false
		}
	}

	if !success {
		return fmt.Errorf("completed sync with errors")
	}
	return nil
}

// changedBranch stores the refs that have changes that need to be submitted in a PR, and the diff
// of files being changed in the PR for use in the PR body.
type changedBranch struct {
	Refs    *gitpr.SyncPRRefSet
	Diff    string
	PRTitle string
	PRBody  string

	// Result is a pointer to the SyncResult to update with PR info once a PR is submitted for this
	// changed branch.
	Result *SyncResult
}

// SyncResult is the result of a sync call.
type SyncResult struct {
	// PR is the GitHub PR creation response if a PR is necessary. If the target repo is already up
	// to date, this is nil. If this is the result of a dry run, PR may be nil even if a PR is
	// necessary, because a PR wasn't created.
	PR *gitpr.GitHubResponse
	// Commit is the commit hash that contains the updated result. This is either the commit that
	// was pushed for the PR, or a commit that already exists in the target repo.
	Commit string
}

// MakeBranchPRs creates sync changes for each branch in the given entry and submits them as PRs.
// Multiple branches are processed at the same time in order to efficiently use Git: it is better to
// tell Git to fetch/push multiple branches at the same time than run the operations individually.
// Returns an error, or the sync results of each branch.
func MakeBranchPRs(f *Flags, dir string, entry *ConfigEntry) ([]SyncResult, error) {
	auther, err := f.ParseAuth()
	if err != nil {
		return nil, err
	}

	if err := run(exec.Command("git", "init", dir)); err != nil {
		return nil, err
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
		if commit, ok := entry.SourceBranchLatestCommit[upstream]; ok {
			nb.Commit = commit
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
		return nil, err
	}
	if err := run(fetchOrigin); err != nil {
		return nil, err
	}

	// Fetch the state of the official/upstream-maintained mirror (if specified) so we can check
	// against it later.
	if entry.UpstreamMirror != "" {
		fetchUpstreamMirror := newGitCmd("fetch", "--no-tags", auther.InsertAuth(entry.UpstreamMirror))
		for _, b := range branches {
			fetchUpstreamMirror.Args = append(fetchUpstreamMirror.Args, b.UpstreamMirrorFetchRefspec())
		}
		if err := run(fetchUpstreamMirror); err != nil {
			return nil, err
		}
	}

	// Before attempting any update, mirror everything (if specified). Only continue once this is
	// complete, to avoid a potentially broken internal build state.
	if entry.MirrorTarget != "" {
		mirror := newGitCmd("push", auther.InsertAuth(entry.MirrorTarget))
		for _, b := range branches {
			mirror.Args = append(mirror.Args, b.UpstreamMirrorRefspec())
		}
		if *f.DryRun {
			mirror.Args = append(mirror.Args, "-n")
		}

		if err := run(mirror); err != nil {
			return nil, err
		}
	}

	// Track the sync results. In this first section, we figure out the result's Commit value. In
	// the second section, a PR is created if the Commit doesn't exist in the target, and we update
	// the result struct to include that info.
	results := make([]SyncResult, len(branches))

	// While looping through the branches and trying to sync, use this slice to keep track of which
	// branches have changes, so we can push changes and submit PRs later.
	changedBranches := make([]changedBranch, 0, len(branches))

	for i, b := range branches {
		fmt.Printf("---- Processing branch %q for entry targeting %v\n", b.Name, entry.Target)

		if err := run(newGitCmd("checkout", b.PRBranch())); err != nil {
			return nil, err
		}

		c := changedBranch{
			Refs: b,
			PRBody: "Hi! I'm a bot, and this is an automatically generated upstream sync PR. 🔃" +
				"\n\nAfter submitting the PR, I will attempt to enable auto-merge in the \"merge commit\" configuration." +
				"\n\nFor more information, visit [sync documentation in microsoft/go-infra](https://github.com/microsoft/go-infra/tree/main/docs/automation/sync.md).",
			Result: &results[i],
		}
		var commitMessage string

		if entry.SubmoduleTarget == "" {
			// This is not a submodule update, so merge with the upstream repository.
			if err := run(newGitCmd("merge", "--no-ff", "--no-commit", b.UpstreamLocalSyncTarget())); err != nil {
				if exitError, ok := err.(*exec.ExitError); ok {
					fmt.Printf("---- Merge hit an ExitError: %q. A non-zero exit code is expected if there were conflicts. The script will try to resolve them, next.\n", exitError)
				} else {
					// Make sure we don't ignore more than we intended.
					return nil, err
				}
			}

			if len(entry.AutoResolveTarget) > 0 {
				// Automatically resolve conflicts in specific project doc files. Use '--no-overlay' to make
				// sure we delete new files in e.g. '.github' that are in upstream but don't exist locally.
				// '--ours' auto-deletes if upstream modifies a file that we deleted in our branch.
				if err := run(newGitCmd(append([]string{"checkout", "--no-overlay", "--ours", "HEAD", "--"}, entry.AutoResolveTarget...)...)); err != nil {
					return nil, err
				}
			}
			c.PRTitle = fmt.Sprintf("Merge upstream %#q into %#q", b.UpstreamName, b.Name)
			c.PRBody += fmt.Sprintf(
				"\n\nThis PR merges %#q into %#q.\n\nIf PR validation fails and you need to fix up the PR, make sure to use a merge commit, not a squash or rebase!",
				c.Refs.UpstreamName, c.Refs.Name,
			)
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

			// This update uses a submodule, so find the target version of upstream and update the
			// submodule to point at it.
			newCommit, err := getTrimmedCmdOutput("rev-parse", b.UpstreamLocalSyncTarget())
			if err != nil {
				return nil, err
			}

			// Limit the commit to one that's available in every known official repository.
			if entry.UpstreamMirror != "" {
				upstreamMirrorCommit, err := getTrimmedCmdOutput("rev-parse", b.UpstreamMirrorLocalBranch())
				if err != nil {
					return nil, err
				}
				if newCommit != upstreamMirrorCommit {
					// Point out mismatches, so we can keep track of them later by searching logs.
					// This happening normally isn't a concern: our sync schedule most likely
					// coincided with the potential time window where a commit has been pushed to
					// Upstream before being pushed to UpstreamMirror. Or, the user gave a specific
					// commit to use which is intentionally not the latest one in the branch.
					fmt.Printf("--- Upstream and upstream mirror commits do not match: %v != %v\n", newCommit, upstreamMirrorCommit)
				}

				commonCommit, err := getTrimmedCmdOutput("merge-base", newCommit, upstreamMirrorCommit)
				if err != nil {
					return nil, err
				}
				fmt.Printf("---- Common commit of upstream and upstream mirror: %v\n", commonCommit)
				newCommit = commonCommit
			}

			// Set the submodule commit directly in the Git index. This avoids the need to
			// init/clone the submodule, which can be time-consuming. Mode 160000 means the tree
			// entry is a submodule.
			cacheInfo := fmt.Sprintf("160000,%v,%v", newCommit, entry.SubmoduleTarget)
			if err := run(newGitCmd("update-index", "--cacheinfo", cacheInfo)); err != nil {
				return nil, err
			}

			upstreamCommitMessage, err := getTrimmedCmdOutput("log", "--format=%B", "-n", "1", newCommit)
			if err != nil {
				return nil, err
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
				return nil, err
			}
		} else {
			// If the diff had 0 exit code, there are no changes. Skip this branch's next steps.
			fmt.Printf("---- No changes to sync for %v. Skipping.\n", b.Name)

			// Save the current commit in the result struct. This lets the caller know exactly what
			// commit was found to be up to date, to avoid racing with other changes being merged
			// into the target repo.
			commit, err := combinedOutput(newGitCmd("rev-parse", "HEAD"))
			if err != nil {
				return nil, err
			}
			c.Result.Commit = commit

			continue
		}

		// If we still have unmerged files, 'git commit' will exit non-zero, causing the script to
		// exit. This prevents the script from pushing a bad merge.
		if err := run(newGitCmd("commit", "-m", commitMessage)); err != nil {
			return nil, err
		}

		// Save the created commit in the result struct.
		commit, err := combinedOutput(newGitCmd("rev-parse", "HEAD"))
		if err != nil {
			return nil, err
		}
		c.Result.Commit = commit

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
				return nil, err
			}

			// The diff may be large. Truncate it if it seems unreasonable to show on the console, or to
			// include in a PR description. The user can use Git to dig deeper if needed.
			var diffLines strings.Builder
			diffLineScanner := bufio.NewScanner(strings.NewReader(diff))
			for lineNumber := 0; diffLineScanner.Scan(); lineNumber++ {
				if err := diffLineScanner.Err(); err != nil {
					return nil, err
				}
				if lineNumber == maxDiffLinesToDisplay {
					diffLines.WriteString(fmt.Sprintf("Diff truncated: contains more than %v lines.\n", maxDiffLinesToDisplay))
					break
				}
				diffLines.WriteString(diffLineScanner.Text())
				diffLines.WriteString("\n")
			}
			c.Diff = diffLines.String()

			if c.Diff != "" {
				c.PRBody += fmt.Sprintf(
					"\n\n"+
						"<details><summary>Click on this text to view the file difference between this branch and upstream.</summary>\n\n"+
						"```\n%v\n```"+
						"\n\n</details>",
					c.Diff,
				)
			}
		}

		changedBranches = append(changedBranches, c)
	}

	if len(changedBranches) == 0 {
		fmt.Println("Checked branches for changes to sync: none found.")
		fmt.Println("Success.")
		return results, nil
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
		if *f.DryRun {
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
	if err := run(newGitPushCommand(entry.PRBranchStorageRepo(), true, mergePushRefspecs)); err != nil {
		return nil, err
	}

	// All Git operations are complete! Next, ensure there's a GitHub PR for each auto-merge branch.

	// Accumulate overall failure. This lets PR submission continue even if there's a problem for a
	// specific branch.
	var prFailed bool

	// Parse the URLs involved in the PR to get segment information.
	parsedPRTargetRemote, err := gitpr.ParseRemoteURL(entry.Target)
	if err != nil {
		return nil, err
	}
	parsedPRHeadRemote, err := gitpr.ParseRemoteURL(entry.PRBranchStorageRepo())
	if err != nil {
		return nil, err
	}

	for _, b := range changedBranches {
		prFlowDescription := fmt.Sprintf("%v -> %v", b.Refs.UpstreamName, b.Refs.PRBranch())

		fmt.Printf("---- %s: Checking if PR should be submitted.\n", prFlowDescription)
		fmt.Printf("---- PR Title: %s\n", b.PRTitle)
		fmt.Printf("---- PR Body:\n%s\n", b.PRBody)

		var skipReason string
		switch {
		case *f.DryRun:
			skipReason = "Dry run"

		case *f.GitHubUser == "":
			skipReason = "github-user not provided"
		case *f.GitHubPAT == "":
			skipReason = "github-pat not provided"

		case *f.GitHubPATReviewer == "":
			// In theory, if we have githubPAT but no reviewer, we can submit the PR but skip
			// reviewing it/enabling auto-merge. However, this doesn't seem very useful, so it isn't
			// implemented.
			skipReason = "github-pat-reviewer not provided"
		}

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

			request := b.Refs.CreateGitHubPR(parsedPRHeadRemote.GetOwner(), b.PRTitle, b.PRBody)

			// POST the PR. The call returns success if the PR is created or if we receive a
			// specific error message back from GitHub saying the PR is already created.
			pr, err := gitpr.PostGitHub(parsedPRTargetRemote.GetOwnerSlashRepo(), request, *f.GitHubPAT)
			fmt.Printf("%+v\n", pr)
			if err != nil {
				return err
			}

			if pr.AlreadyExists {
				fmt.Println("---- A PR already exists. Attempting to find it...")
				existingPR, err := gitpr.FindExistingPR(
					request,
					parsedPRHeadRemote,
					parsedPRTargetRemote,
					b.Refs.PRBranch(),
					*f.GitHubUser,
					*f.GitHubPAT)
				if err != nil {
					return err
				}
				if pr == nil {
					return fmt.Errorf("no PR found")
				}
				pr.NodeID = existingPR.ID
				pr.Number = existingPR.Number
			} else {
				fmt.Printf("---- Submitted brand new PR: %v\n", pr.HTMLURL)

				fmt.Printf("---- Approving with reviewer account...\n")
				if err = gitpr.ApprovePR(pr.NodeID, *f.GitHubPATReviewer); err != nil {
					return err
				}
			}

			fmt.Printf("---- Enabling auto-merge with reviewer account...\n")
			if err = gitpr.EnablePRAutoMerge(pr.NodeID, *f.GitHubPATReviewer); err != nil {
				return err
			}

			fmt.Printf("---- PR for %v: Done.\n", prFlowDescription)
			b.Result.PR = pr
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
		return nil, fmt.Errorf("failed to submit one or more PRs")
	}

	return results, nil
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