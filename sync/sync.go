// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sync

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/azdo"
	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/stringutil"
)

type Flags struct {
	DryRun          *bool
	InitialCloneDir *string

	GitHubUser        *string
	GitHubPAT         *string
	GitHubPATReviewer *string

	GitHubAppID           *int64
	GitHubAppInstallation *int64
	GitHubAppPrivateKey   *string

	AzDODncengPAT *string

	SyncConfig *string
	TempGitDir *string

	CreateBranches *bool

	GitAuthString *string
}

func BindFlags(workingDirectory string) *Flags {
	return &Flags{
		DryRun: flag.Bool("n", false, "Enable dry run: do not push, do not submit PR."),
		InitialCloneDir: flag.String(
			"initial-clone-dir", "",
			"When creating a repo, clone this repo/directory rather than starting from scratch.\n"+
				"This may be used in dev/test to reduce network usage if fetching the official source from scratch is slow."),

		GitHubUser:        flag.String("github-user", "", "Use this github user to submit pull requests."),
		GitHubPAT:         flag.String("github-pat", "", "Submit the PR with this GitHub PAT, if specified."),
		GitHubPATReviewer: flag.String("github-pat-reviewer", "", "Approve the PR and turn on auto-merge with this PAT, if specified. Required, if github-pat specified."),

		GitHubAppID:           flag.Int64("github-app-id", 0, "Use this GitHub App ID to authenticate to GitHub."),
		GitHubAppInstallation: flag.Int64("github-app-installation", 0, "Use this GitHub App Installation ID to authenticate to GitHub."),
		GitHubAppPrivateKey:   flag.String("github-app-private-key", "", "Use this GitHub App Private Key to authenticate to GitHub."),

		AzDODncengPAT: flag.String("azdo-dnceng-pat", "", "Use this Azure DevOps PAT to authenticate to dnceng project HTTPS Git URLs."),

		SyncConfig: flag.String("c", "eng/sync-config.json", "The sync configuration file to run."),
		TempGitDir: flag.String(
			"temp-git-dir",
			filepath.Join(workingDirectory, "eng", "artifacts", "sync-upstream-temp-repo"),
			"Location to create the temporary Git repo. A timestamped subdirectory is created to reduce chance of collision."),

		CreateBranches: flag.Bool(
			"create-branches", false,
			"Before running sync, check that each target branch exists in the target repo.\n"+
				"If not, push it to the target repo as a fork from the configured MainBranch."),

		GitAuthString: flag.String(
			"git-auth",
			string(GitAuthNone),
			// List valid options. Indent one space, to line up with the automatic ' (default "none")'.
			"The type of Git auth to inject into URLs for fetch/push access. String options:\n"+
				" none - Leave GitHub URLs as they are. Git may use HTTPS authentication in this case.\n"+
				" ssh - Change the GitHub URL to SSH format.\n"+
				" pat - Add the 'github-user' and 'github-pat' values into the URL.\n"+
				" app - Add the 'github-app-id', 'github-app-installation', and 'github-app-private-key' values into the URL.\n"),
	}
}

// AzDOVariableFlags is a set of flags that a sync command runner can set to make sync emit AzDO
// Pipeline log commands to return the results of a sync operation into a form that can be used in
// later steps in the pipeline. See BindAzDOVariableFlags for flag descriptions.
type AzDOVariableFlags struct {
	SetVariablePRNumber       *string
	SetVariableUpToDateCommit *string
}

// BindAzDOVariableFlags creates a flags struct that contains initialized flags.
func BindAzDOVariableFlags() *AzDOVariableFlags {
	return &AzDOVariableFlags{
		SetVariablePRNumber: flag.String(
			"set-azdo-variable-pr-number", "",
			"An AzDO variable name to set to the sync PR number, or nil if no sync PR is created."),
		SetVariableUpToDateCommit: flag.String(
			"set-azdo-variable-up-to-date-commit", "",
			"An AzDO variable name to set to nil if a sync PR is created, otherwise the full commit hash that was found to be already up to date."),
	}
}

// SetAzDOVariables prints logging commands to stdout to assign the output variables if the variable
// name flags have been set, otherwise does nothing.
func (a *AzDOVariableFlags) SetAzDOVariables(prNumber, upToDateCommit string) {
	if *a.SetVariablePRNumber != "" {
		azdo.LogCmdSetVariable(*a.SetVariablePRNumber, prNumber)
	}
	if *a.SetVariableUpToDateCommit != "" {
		azdo.LogCmdSetVariable(*a.SetVariableUpToDateCommit, upToDateCommit)
	}
}

func (f *Flags) ParseAuth() (gitcmd.URLAuther, error) {
	switch GitAuthOption(*f.GitAuthString) {
	case GitAuthNone:
		return gitcmd.NoAuther{}, nil

	case GitAuthApp:
		var missingArgs string
		if *f.GitHubAppID == 0 {
			missingArgs += " git-auth app is specified but github-app-id is not."
		}
		if *f.GitHubAppInstallation == 0 {
			missingArgs += " git-auth app is specified but github-app-installation is not."
		}
		if *f.GitHubAppPrivateKey == "" {
			missingArgs += " git-auth app is specified but github-app-private-key is not."
		}
		if missingArgs != "" {
			return nil, fmt.Errorf("missing command-line args:%v", missingArgs)
		}
		return gitcmd.GitHubAppAuther{
			AppID:          *f.GitHubAppID,
			InstallationID: *f.GitHubAppInstallation,
			PrivateKey:     *f.GitHubAppPrivateKey,
		}, nil

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
	d, err := executil.MakeWorkDir(*f.TempGitDir)
	if err != nil {
		return "", fmt.Errorf("failed to make working directory for sync: %w", err)
	}
	return d, nil
}

func (f *Flags) ReadConfig() ([]ConfigEntry, error) {
	var entries []ConfigEntry
	if err := stringutil.ReadJSONFile(*f.SyncConfig, &entries); err != nil {
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
	GitAuthApp  GitAuthOption = "app"
)

var errWouldCreateBranchButCurrentlyDryRun = errors.New("would have pushed a new branch to the target repository to kick off a new version, but this is a dry run. Cannot continue")

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

// changedBranch is the state of a specific branch that is being changed by the sync process.
// Some of the sync process is shared between branches (for performance), and some is per-branch.
type changedBranch struct {
	Refs       *gitpr.SyncPRRefSet
	PRRequest  *gitpr.GitHubRequest
	ExistingPR *gitpr.ExistingPR

	SkipReason string

	// Result contains this branch's [SyncResult] once a PR is submitted/updated.
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

	var reviewAuther gitcmd.URLAuther

	if f.GitHubPATReviewer != nil {
		reviewAuther = &gitcmd.GitHubPATAuther{
			PAT: *f.GitHubPATReviewer,
		}
	}

	if *f.InitialCloneDir == "" {
		if err := run(exec.Command("git", "init", dir)); err != nil {
			return nil, err
		}
	} else {
		if err := run(exec.Command("git", "clone", *f.InitialCloneDir, dir)); err != nil {
			return nil, err
		}
	}

	// newGitCmd creates a "git {args}" command that runs in the temp fetch repo Git dir.
	newGitCmd := func(args ...string) *exec.Cmd {
		c := exec.Command("git", args...)
		c.Dir = dir
		return c
	}

	branches := make([]*gitpr.SyncPRRefSet, 0, len(entry.AutoSyncBranches))
	for _, upstream := range entry.AutoSyncBranches {
		target, err := entry.TargetBranch(upstream)
		if err != nil {
			return nil, err
		}
		if target == "" {
			return nil, fmt.Errorf("no target match found for auto sync branch %q", upstream)
		}
		nb := &gitpr.SyncPRRefSet{
			UpstreamName: upstream,
			PRRefSet: gitpr.PRRefSet{
				Name:    target,
				Purpose: "auto-sync",
			},
		}
		if commit, ok := entry.SourceBranchLatestCommit[upstream]; ok {
			nb.Commit = commit
		}
		branches = append(branches, nb)
	}
	// Auto-mirrored branches are simpler: always get the latest commits to
	// push to the mirror repo with the same branch name.
	autoMirrorBranches := make([]*gitpr.MirrorRefSet, 0, len(entry.AutoMirrorBranches))
	for _, upstreamPattern := range entry.AutoMirrorBranches {
		autoMirrorBranches = append(autoMirrorBranches, &gitpr.MirrorRefSet{
			UpstreamPattern: upstreamPattern,
		})
	}

	if *f.CreateBranches {
		if entry.MainBranch == "" {
			return nil, errors.New("the create-branches flag requires MainBranch to be configured, but it is not")
		}

		for _, b := range branches {
			check := newGitCmd("ls-remote", "--exit-code", "--heads", entry.Target, "refs/heads/"+b.Name)
			if err := run(check); err != nil {
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					return nil, err
				}
				if exitErr.ExitCode() != 2 {
					return nil, err
				}

				// Get a reference to the main branch to fork from.
				mainRef := gitpr.PRRefSet{
					Name:    entry.MainBranch,
					Purpose: "auto-sync-new-branch",
				}
				fetchMain := newGitCmd(
					"fetch", "--no-tags",
					auther.InsertAuth(entry.Target),
					mainRef.BaseBranchFetchRefspec())
				if err := run(fetchMain); err != nil {
					return nil, err
				}

				// Push the new branch: fork from the main commit we just fetched to a local branch.
				fork := newGitCmd(
					"push", "--no-tags",
					auther.InsertAuth(entry.Target),
					b.ForkFromMainRefspec(mainRef.PRBranch()))
				if *f.DryRun {
					fork.Args = append(fork.Args, "-n")
				}
				if err := run(fork); err != nil {
					return nil, err
				}

				if *f.DryRun {
					return nil, errWouldCreateBranchButCurrentlyDryRun
				}
			}
		}
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
	for _, b := range autoMirrorBranches {
		fetchUpstream.Args = append(fetchUpstream.Args, b.UpstreamMirrorFetchRefspec())
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
		for _, b := range autoMirrorBranches {
			mirror.Args = append(mirror.Args, b.UpstreamMirrorRefspec())
		}
		if *f.DryRun {
			mirror.Args = append(mirror.Args, "-n")
		}

		if err := run(mirror); err != nil {
			return nil, err
		}
	}

	// Parse the URLs involved in the PR to get segment information.
	parsedPRTargetRemote, err := gitpr.ParseRemoteURL(entry.Target)
	if err != nil {
		return nil, err
	}
	parsedPRHeadRemote, err := gitpr.ParseRemoteURL(entry.PRBranchStorageRepo())
	if err != nil {
		return nil, err
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

		changedBranches = append(changedBranches, changedBranch{
			Refs:   b,
			Result: &results[i],
		})
		c := &changedBranches[len(changedBranches)-1]

		prBody := "Hi! I'm a bot, and this is an automatically generated upstream sync PR. 🔃" +
			"\n\nAfter submitting the PR, I will attempt to enable auto-merge in the \"merge commit\" configuration." +
			"\n\nFor more information, visit [sync documentation in microsoft/go-infra](https://github.com/microsoft/go-infra/tree/main/docs/automation/sync.md)."
		var prTitle, commitMessage string

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
			prTitle = fmt.Sprintf("Merge upstream %#q into %#q", b.UpstreamName, b.Name)
			prBody += fmt.Sprintf(
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

			// Set the content of the file at the path, creating the file if necessary, and add the
			// file to the Git index for committing later. If the content is empty, delete the file
			// if it exists.
			updateFile := func(path, content string) error {
				fmt.Printf("---- Setting %#q content: %q\n", path, content)
				if content == "" {
					if err := run(newGitCmd("rm", "--ignore-unmatch", "--", path)); err != nil {
						return err
					}
				} else {
					if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
						return err
					}
					if err := run(newGitCmd("add", "--", path)); err != nil {
						return err
					}
				}
				return nil
			}

			// This update uses a submodule, so find the target version of upstream and update the
			// submodule to point at it. UpstreamLocalSyncTarget might be a commit hash, and if so,
			// this rev-parse is simply checking that the commit actually exists.
			newCommit, err := getTrimmedCmdOutput("rev-parse", b.UpstreamLocalSyncTarget())
			if err != nil {
				return nil, fmt.Errorf("failed to find upstream sync target in local repo after fetching: %w", err)
			}

			// If we're updating to the latest upstream commit, not a specific upstream commit, find
			// the latest commit available in both upstream and the upstream mirror.
			//
			// If we're updating to a specific commit, the caller of the sync command should have
			// already validated that it's available in both places. At worst, a missing commit will
			// cause a failure in the PR validation check.
			if entry.UpstreamMirror != "" && b.Commit == "" {
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

			// Ensure either the VERSION file in the submodule is what we expect it to be, or the
			// Microsoft build of Go repository contains a VERSION file specifying that version. This fixes boring
			// branches, where upstream has no VERSION file and we need to make it ourselves.
			//
			// If there is no expected version, (e.g. the sync entry simply wants to sync to
			// latest), don't even check.
			if entry.GoVersionFileContent != "" {
				upstreamVersion, err := getTrimmedCmdOutput("show", newCommit+":VERSION")
				if err != nil {
					if _, ok := err.(*exec.ExitError); ok {
						fmt.Printf("---- VERSION file doesn't exist in submodule.\n")
					} else {
						return nil, err
					}
				}

				var content string
				// We only need an outer repo VERSION file if the expected version mismatches the
				// submodule's VERSION file.
				if entry.GoVersionFileContent != upstreamVersion {
					content = entry.GoVersionFileContent
				}

				if err := updateFile(filepath.Join(dir, "VERSION"), content); err != nil {
					return nil, err
				}
			}

			// Ensure the Microsoft revision file is what we expect it to be. If there is no
			// expected revision, leave it alone.
			if entry.GoMicrosoftRevisionFileContent != "" {
				var content string
				// We only need a MICROSOFT_REVISION file for revisions > 1 (the default/minimum).
				if entry.GoMicrosoftRevisionFileContent != "1" {
					content = entry.GoMicrosoftRevisionFileContent
				}

				if err := updateFile(filepath.Join(dir, "MICROSOFT_REVISION"), content); err != nil {
					return nil, err
				}
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

			prTitle = fmt.Sprintf("Update submodule to latest %#q in %#q", b.UpstreamName, b.Name)
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
			c.SkipReason = "No changes to sync"

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
		commitArgs := []string{"commit", "-m", commitMessage}
		if err := run(newGitCmd(commitArgs...)); err != nil {
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
			diffString := diffLines.String()

			if diffString != "" {
				prBody += fmt.Sprintf(
					"\n\n"+
						"<details><summary>Click on this text to view the file difference between this branch and upstream.</summary>\n\n"+
						"```\n%v\n```"+
						"\n\n</details>",
					diffString,
				)
			}
		}

		c.PRRequest = c.Refs.CreateGitHubPR(parsedPRHeadRemote.GetOwner(), prTitle, prBody)

		switch {
		case *f.DryRun:
			c.SkipReason = "Dry run"

		case *f.GitHubUser == "" && *f.GitHubAppID == 0:
			c.SkipReason = "github-user not provided"
		case *f.GitHubPAT == "" && *f.GitHubAppID == 0:
			c.SkipReason = "github-pat not provided"

		case *f.GitHubPATReviewer == "":
			// In theory, if we have githubPAT but no reviewer, we can submit the PR but skip
			// reviewing it/enabling auto-merge. However, this doesn't seem very useful, so it isn't
			// implemented.
			c.SkipReason = "github-pat-reviewer not provided"
		}
		if c.SkipReason != "" {
			continue
		}

		c.ExistingPR, err = gitpr.FindExistingPR(
			c.PRRequest,
			parsedPRHeadRemote,
			parsedPRTargetRemote,
			c.Refs.PRBranch(),
			*f.GitHubUser,
			auther)
		if err != nil {
			return nil, err
		}
		if c.ExistingPR != nil {
			// If the PR already exists, we need to check if anyone else has pushed changes to the
			// branch to make sure we don't overwrite them.
			remoteCommit, err := gitcmd.FetchRefCommit(
				dir,
				auther.InsertAuth(entry.PRBranchStorageRepo()),
				"refs/heads/"+c.Refs.PRBranch())
			if err != nil {
				return nil, err
			}
			myAuthorEmail, err := gitcmd.ShowQuietPretty(dir, "%ae", c.Result.Commit)
			if err != nil {
				return nil, err
			}
			remoteAuthorEmail, err := gitcmd.ShowQuietPretty(dir, "%ae", remoteCommit)
			if err != nil {
				return nil, err
			}

			if myAuthorEmail != remoteAuthorEmail {
				c.SkipReason = "PR already exists, but my author name (" +
					myAuthorEmail +
					") is different from the author of the remote commit (" +
					remoteAuthorEmail +
					"). Skipping PR submission."
			}
		}

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
		c.Args = append(c.Args, refspecs...)
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
		if b.SkipReason != "" {
			continue
		}
		mergePushRefspecs = append(mergePushRefspecs, b.Refs.PRBranchRefspec())
	}
	if len(mergePushRefspecs) > 0 {
		if err := run(newGitPushCommand(entry.PRBranchStorageRepo(), true, mergePushRefspecs)); err != nil {
			return nil, err
		}
	}

	// All Git operations are complete! Next, ensure there's a GitHub PR for each auto-merge branch.

	// Accumulate overall failure. This lets PR submission continue even if there's a problem for a
	// specific branch.
	var prFailed bool

	for _, b := range changedBranches {
		prFlowDescription := fmt.Sprintf("%v -> %v", b.Refs.UpstreamName, b.Refs.PRBranch())

		fmt.Printf("---- %s: Checking if PR should be submitted.\n", prFlowDescription)

		if b.SkipReason != "" {
			fmt.Printf("---- %s: skipping submitting PR: %s\n", prFlowDescription, b.SkipReason)
			continue
		}

		// err contains any err we get from running the sequence of GitHub PR submission API calls.
		//
		// This uses an immediately invoked anonymous function for convenience/maintainability. We
		// can 'return err' from anywhere in the function, to keep control flow simple. Also, we can
		// capture vars from the 'main()' scope rather than making them global or explicitly passing
		// each one into a named function.
		err := func() error {
			fmt.Printf("---- %s: submitting PR...\n", prFlowDescription)

			// POST the PR. The call returns success if the PR is created or if we receive a
			// specific error message back from GitHub saying the PR is already created.
			pr, err := gitpr.PostGitHub(parsedPRTargetRemote.GetOwnerSlashRepo(), b.PRRequest, auther)
			if err != nil {
				if errors.Is(err, gitpr.ErrPRAlreadyExists) {
					if b.ExistingPR == nil {
						return fmt.Errorf("unable to submit PR because PR already exists, but no existing PR was found")
					}
					pr = &gitpr.GitHubResponse{
						NodeID: b.ExistingPR.ID,
						Number: b.ExistingPR.Number,
					}
				} else {
					return err
				}
			} else {
				fmt.Printf("%+v\n", pr)
				if b.ExistingPR != nil {
					return fmt.Errorf("submitted a fresh PR, but failure was expected because an existing PR was found")
				}
				fmt.Printf("---- Submitted brand new PR: %v\n", pr.HTMLURL)

				fmt.Printf("---- Approving with reviewer account...\n")
				if err = gitpr.ApprovePR(pr.NodeID, reviewAuther); err != nil {
					return err
				}
			}

			fmt.Printf("---- Enabling auto-merge with reviewer account...\n")
			if err = gitpr.EnablePRAutoMerge(pr.NodeID, auther); err != nil {
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
