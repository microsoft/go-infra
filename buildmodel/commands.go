// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/patch"
	"github.com/microsoft/go-infra/submodule"
)

// ParseBoundFlags parses all flags that have been registered with the flag package. This function
// handles '-help' and validates no unhandled args were passed, so may exit rather than returning.
func ParseBoundFlags(description string) {
	var help = flag.Bool("h", false, "Print this help message.")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}

	flag.Parse()

	if len(flag.Args()) > 0 {
		fmt.Printf("Non-flag argument(s) provided but not accepted: %v\n", flag.Args())
		flag.Usage()
		os.Exit(1)
	}

	if *help {
		flag.Usage()
		// We're exiting early, successfully. All we were asked to do is print usage info.
		os.Exit(0)
	}
}

// BuildAssetJSONFlags is a list of flags to create a build asset JSON file.
type BuildAssetJSONFlags struct {
	artifactsDir   *string
	branch         *string
	destinationURL *string
	sourceDir      *string

	output *string
}

// BindBuildAssetJSONFlags creates BuildAssetJSONFlags with the 'flag' package, globally registering
// them in the flag package so ParseBoundFlags will find them.
func BindBuildAssetJSONFlags() *BuildAssetJSONFlags {
	return &BuildAssetJSONFlags{
		artifactsDir:   flag.String("artifacts-dir", "eng/artifacts/bin", "The path of the directory to scan for artifacts."),
		branch:         flag.String("branch", "unknown", "The name of the branch that produced these artifacts."),
		destinationURL: flag.String("destination-url", "https://example.org/default", "The base URL where all files in the source directory can be downloaded from."),
		sourceDir:      flag.String("source-dir", "", "The path of the source code directory to scan for a VERSION file."),

		output: flag.String("o", "assets.json", "The path of the build asset JSON file to create."),
	}
}

// GenerateBuildAssetJSON uses the specified parameters to summarize a build in a build asset JSON
// file.
func GenerateBuildAssetJSON(f *BuildAssetJSONFlags) error {
	// Look up value of Build.BuildId Azure Pipelines predefined variable:
	// https://docs.microsoft.com/en-us/azure/devops/pipelines/build/variables?view=azure-devops&tabs=yaml#build-variables-devops-services
	buildID := "unknown"
	if id, ok := os.LookupEnv("BUILD_BUILDID"); ok {
		buildID = id
	}

	b := &buildassets.BuildResultsDirectoryInfo{
		SourceDir:      *f.sourceDir,
		ArtifactsDir:   *f.artifactsDir,
		DestinationURL: *f.destinationURL,
		Branch:         *f.branch,
		BuildID:        buildID,
	}

	m, err := b.CreateSummary()
	if err != nil {
		return err
	}

	fmt.Printf("Generated build asset summary:\n%+v\n", m)
	if err := WriteJSONFile(*f.output, m); err != nil {
		return err
	}
	return nil
}

// PRFlags is a list of flags used to submit a Docker update PR.
type PRFlags struct {
	dryRun       *bool
	tempGitDir   *string
	manualBranch *string

	origin *string
	to     *string

	githubPAT         *string
	githubPATReviewer *string

	UpdateFlags
}

// BindPRFlags creates PRFlags with the 'flag' package, globally registering them in the flag
// package so ParseBoundFlags will find them.
func BindPRFlags() *PRFlags {
	var artifactsDir = filepath.Join(getwd(), "eng", "artifacts")
	return &PRFlags{
		dryRun:       flag.Bool("n", false, "Enable dry run: do not push, do not submit PR."),
		tempGitDir:   flag.String("temp-git-dir", filepath.Join(artifactsDir, "sync-upstream-temp-repo"), "Location to create the temporary Git repo. Must not exist."),
		manualBranch: flag.String("manual-branch", "", "Branch to submit PR into. Overrides branch detection."),

		origin: flag.String("origin", "git@github.com:microsoft/go-images", "Submit PR to this repo. \n[Need fetch Git permission.]"),
		to:     flag.String("to", "", "Push PR branch to this Git repository. Defaults to the same repo as 'origin' if not set.\n[Need push Git permission.]"),

		githubPAT:         flag.String("github-pat", "", "Submit the PR with this GitHub PAT, if specified."),
		githubPATReviewer: flag.String("github-pat-reviewer", "", "Approve the PR and turn on auto-merge with this PAT, if specified. Required, if github-pat specified."),

		UpdateFlags: *BindUpdateFlags(),
	}
}

// SubmitUpdatePR runs an auto-update in a temp Git repo. If GitHub credentials are provided,
// submits the resulting commit as a GitHub PR, approves with a second account, and enables the
// GitHub auto-merge feature.
func SubmitUpdatePR(f *PRFlags) error {
	if !*f.skipDockerfiles {
		if err := EnsureDockerfileGenerationPrerequisites(); err != nil {
			return err
		}
	}

	gitDir, err := MakeWorkDir(*f.tempGitDir)
	if err != nil {
		return err
	}

	if *f.origin == "" {
		fmt.Println("Origin not specified. Nothing to do.")
		return nil
	}
	if *f.to == "" {
		f.to = f.origin
	}

	var assets *buildassets.BuildAssets
	if *f.buildAssetJSON != "" {
		assets = &buildassets.BuildAssets{}
		if err := ReadJSONFile(*f.buildAssetJSON, &assets); err != nil {
			return err
		}
	}

	targetBranch := *f.manualBranch
	if targetBranch == "" && assets != nil {
		targetBranch = assets.GetDockerRepoTargetBranch()
	}
	if targetBranch == "" {
		fmt.Println("This build assets JSON file isn't associated with any Docker image repo branch.\nSee the GetDockerRepoTargetBranch Go func in 'buildmodel/buildassets'.")
		return nil
	}
	fmt.Printf("---- Target branch for PR: %v\n", targetBranch)

	b := gitpr.PRRefSet{
		Name:    targetBranch,
		Purpose: "auto-update",
	}

	parsedOrigin, err := gitpr.ParseRemoteURL(*f.origin)
	if err != nil {
		return err
	}

	parsedPRHeadRemote, err := gitpr.ParseRemoteURL(*f.to)
	if err != nil {
		return err
	}

	title := fmt.Sprintf("Update dependencies in `%v`", b.Name)
	body := fmt.Sprintf(
		"🔃 This is an automatically generated PR updating the version of Go in `%v`.\n\n"+
			"This PR should auto-merge itself when PR validation passes.\n\n",
		b.Name,
	)
	request := b.CreateGitHubPR(parsedPRHeadRemote.GetOwner(), title, body)

	// If we find a PR, fetch its head branch and push the new commit to its tip. We need to support
	// updating from many branches -> one branch, and force pushing each time would drop updates.
	// Note that we do assume our calculated head branch is the same as what the PR uses: it would
	// be strange for this to not be the case and the assumption simplifies the code for now.
	var existingPR string

	if *f.githubPAT != "" {
		githubUser := gitpr.GetUsername(*f.githubPAT)
		fmt.Printf("---- User for github-pat is: %v\n", githubUser)

		if parsedOrigin != nil {
			fmt.Println("---- Checking for an existing PR for this base branch and origin...")
			existingPR, err = gitpr.FindExistingPR(
				request,
				parsedPRHeadRemote,
				parsedOrigin,
				b.PRBranch(),
				githubUser,
				*f.githubPAT)
			if err != nil {
				return err
			}
			if existingPR != "" {
				fmt.Printf("---- Found PR ID: %v\n", existingPR)
			} else {
				fmt.Printf("---- No PR found.\n")
			}
		}
	}

	// We're updating the target repo inside a clone of the go-infra repo, so we want a fresh clone.
	runOrPanic(exec.Command("git", "init", gitDir))

	// newGitCmd creates a "git {args}" command that runs in the temp git dir.
	newGitCmd := func(args ...string) *exec.Cmd {
		c := exec.Command("git", args...)
		c.Dir = gitDir
		return c
	}

	if existingPR != "" {
		// Fetch the existing PR head branch to add onto.
		runOrPanic(newGitCmd("fetch", "--no-tags", *f.to, b.PRBranchRefspec()))
	} else {
		// Fetch the base branch to start the PR head branch.
		runOrPanic(newGitCmd("fetch", "--no-tags", *f.origin, b.BaseBranchFetchRefspec()))
	}
	runOrPanic(newGitCmd("checkout", b.PRBranch()))

	// Make changes to the files in the temp repo.
	if err := UpdateGoImagesRepo(gitDir, assets); err != nil {
		return err
	}
	if !*f.skipDockerfiles {
		if err := RunDockerfileGeneration(gitDir); err != nil {
			return err
		}
	}

	// Check if there are any files in the stage. If not, we don't need to process this branch
	// anymore, because the merge + autoresolve didn't change anything.
	if err := run(newGitCmd("diff", "--quiet")); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			fmt.Printf("---- Detected changes in Git stage. Continuing to commit and submit PR.\n")
		} else {
			// Make sure we don't ignore more than we intended.
			log.Panic(err)
		}
	} else {
		// If the diff had 0 exit code, there are no changes. Skip this branch's next steps.
		fmt.Printf("---- No updates to %v. Skipping.\n", b.Name)
		return nil
	}

	commitMessage := "Update " + b.Name
	if assets != nil {
		commitMessage += " to " + assets.Version
	}

	runOrPanic(newGitCmd("commit", "-a", "-m", commitMessage))

	// Push the commit.
	args := []string{"push", *f.origin, b.PRBranchRefspec()}
	if *f.dryRun {
		// Show what would be pushed, but don't actually push it.
		args = append(args, "-n")
	}
	// If we didn't find an existing PR, the branch might still exist but contain some bad changes.
	// We need to force push to overwrite it with the new, fresh branch. This situation would happen
	// if someone manually closes a PR.
	if existingPR == "" {
		args = append(args, "-f")
	}
	runOrPanic(newGitCmd(args...))

	// Find reasons to skip all the PR submission code. The caller might intentionally be in one of
	// these cases, so it's not necessarily an error. For example, they can take the commit we
	// generated and submit their own PR later.
	skipReason := ""
	switch {
	case *f.dryRun:
		skipReason = "Dry run"
	case *f.origin == "":
		skipReason = "No origin specified"
	case *f.githubPAT == "":
		skipReason = "github-pat not provided"
	case *f.githubPATReviewer == "":
		skipReason = "github-pat-reviewer not provided"
	}
	if skipReason != "" {
		fmt.Printf("---- %s: skipping submitting PR for %v\n", skipReason, b.Name)
		return nil
	}

	fmt.Printf("---- PR for %v: Submitting...\n", b.Name)

	if existingPR == "" {
		// POST the PR. The call returns success if the PR is created or if we receive a specific error
		// message back from GitHub saying the PR is already created.
		p, err := gitpr.PostGitHub(
			parsedOrigin.GetOwnerSlashRepo(),
			request,
			*f.githubPAT)
		fmt.Printf("%+v\n", p)
		if err != nil {
			return err
		}

		// For the rest of the method, the PR now exists.
		existingPR = p.NodeID
		fmt.Printf("---- Submitted brand new PR: %v\n", p.HTMLURL)

		fmt.Printf("---- Approving with reviewer account...\n")
		if err = gitpr.ApprovePR(existingPR, *f.githubPATReviewer); err != nil {
			return err
		}
	}

	fmt.Printf("---- Enabling auto-merge with reviewer account...\n")
	if err = gitpr.EnablePRAutoMerge(existingPR, *f.githubPATReviewer); err != nil {
		return err
	}

	fmt.Printf("---- PR for %v: Done.\n", b.Name)

	return nil
}

// MakeWorkDir creates a unique path inside the given root dir to use as a workspace. The name
// starts with the local time in a sortable format to help with browsing multiple workspaces. This
// function allows a command to run multiple times in sequence without overwriting or deleting the
// old data, for diagnostic purposes. This function uses os.MkdirAll to ensure the root dir exists.
func MakeWorkDir(rootDir string) (string, error) {
	pathDate := time.Now().Format("2006-01-02_15-04-05")
	if err := os.MkdirAll(rootDir, os.ModePerm); err != nil {
		return "", err
	}
	return os.MkdirTemp(rootDir, fmt.Sprintf("%s_*", pathDate))
}

// UpdateFlags is a list of flags used for an update command.
type UpdateFlags struct {
	buildAssetJSON  *string
	skipDockerfiles *bool
}

// BindUpdateFlags creates UpdateFlags with the 'flag' package, globally registering them in
// the flag package so ParseBoundFlags will find them.
func BindUpdateFlags() *UpdateFlags {
	return &UpdateFlags{
		buildAssetJSON:  flag.String("build-asset-json", "", "The path of a build asset JSON file describing the Go build to update to."),
		skipDockerfiles: flag.Bool("skip-dockerfiles", false, "If set, don't touch Dockerfiles.\nUpdating Dockerfiles requires bash/awk/jq, so when developing on Windows, skipping may be useful."),
	}
}

// RunUpdate updates the given Go Docker image repository with the provided flags.
func RunUpdate(repoRoot string, f *UpdateFlags) error {
	if !*f.skipDockerfiles {
		if err := EnsureDockerfileGenerationPrerequisites(); err != nil {
			return err
		}
	}

	var assets *buildassets.BuildAssets
	if *f.buildAssetJSON != "" {
		assets = &buildassets.BuildAssets{}
		if err := ReadJSONFile(*f.buildAssetJSON, &assets); err != nil {
			return err
		}
	}

	if err := UpdateGoImagesRepo(repoRoot, assets); err != nil {
		return err
	}

	if !*f.skipDockerfiles {
		if err := RunDockerfileGeneration(repoRoot); err != nil {
			return err
		}
	}
	return nil
}

// UpdateGoImagesRepo runs an auto-update process in the given Go Docker images repository. It finds
// the 'versions.json' and 'manifest.json' files and updates them based on the given build assets
// struct. If the struct pointer is nil, only updates the 'manifest.json'.
func UpdateGoImagesRepo(repoRoot string, b *buildassets.BuildAssets) error {
	var versionsJSONPath = filepath.Join(repoRoot, "src", "microsoft", "versions.json")
	var manifestJSONPath = filepath.Join(repoRoot, "manifest.json")

	versions := dockerversions.Versions{}
	if err := ReadJSONFile(versionsJSONPath, &versions); err != nil {
		return err
	}

	manifest := dockermanifest.Manifest{}
	if err := ReadJSONFile(manifestJSONPath, &manifest); err != nil {
		return err
	}

	if b != nil {
		if err := UpdateVersions(b, versions); err != nil {
			return err
		}
		if err := WriteJSONFile(versionsJSONPath, &versions); err != nil {
			return err
		}
	}

	fmt.Printf("Generating '%v' based on '%v'...\n", manifestJSONPath, versionsJSONPath)

	UpdateManifest(&manifest, versions)
	if err := WriteJSONFile(manifestJSONPath, &manifest); err != nil {
		return err
	}

	return nil
}

// EnsureDockerfileGenerationPrerequisites checks if Dockerfile generation prerequisites are
// satisfied and returns a descriptive error if not.
func EnsureDockerfileGenerationPrerequisites() error {
	missingTools := false
	for _, requiredCmd := range []string{"bash", "jq", "awk"} {
		if _, err := exec.LookPath(requiredCmd); err != nil {
			fmt.Printf("Unable to find '%s' in PATH. It is required to run 'eng/update-dockerfiles.sh'.\n", requiredCmd)
			fmt.Printf("Error: %s\n", err)
			missingTools = true
		}
	}
	if missingTools {
		return fmt.Errorf("missing required tools to generate Dockerfiles. Make sure the tools are in PATH and try again, or pass '-skip-dockerfiles' to the command")
	}
	return nil
}

// RunDockerfileGeneration runs the Dockerfile generation script in the given go-images repo root.
// Call this after updating the versions.json file to synchronize the Dockerfiles. This function
// doesn't check for prerequisites: EnsureDockerfileGenerationPrerequisites should be called before
// any auto-update code runs to detect problems before wasting time on an incompletable update.
func RunDockerfileGeneration(repoRoot string) error {
	fmt.Println("Generating Dockerfiles...")

	// The location of upstream Go Docker code. We start by assuming we have a submodule at "go".
	goDir := filepath.Join(repoRoot, "go")
	// Location of our Dockerfiles: where the "1.16", "1.17" etc. directories are located.
	microsoftDockerfileRoot := filepath.Join(repoRoot, "src", "microsoft")

	// Detect whether this go-images repository is based on a Git fork or a submodule. A submodule
	// uses scripts from a slightly different location and requires patches to be applied first.
	fork := false
	_, err := os.Stat(goDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Fork repository detected: no 'go' directory.")
			fork = true
		} else {
			return err
		}
	}

	if fork {
		// We are in a Git fork (not a submodule) so we now know Go Docker source code is directly
		// in the repo.
		goDir = repoRoot
	} else {
		// Ensure the submodule is set up correctly and patched, so we can use the patched templates
		// inside to generate our Dockerfiles.
		fmt.Println("---- Resetting submodule...")
		if err := submodule.Reset(repoRoot, false); err != nil {
			return err
		}
		fmt.Println("---- Applying patches to submodule...")
		if err := patch.Apply(repoRoot, patch.ApplyModeCommits); err != nil {
			return err
		}
	}

	// Copy templates into "our" directory. This puts them in the correct location for
	// "apply-templates.sh" to see them. We don't check in a copy: we want to keep it in sync with
	// upstream's copy and apply some small patches.
	if err := copyDockerfileTemplates(goDir, microsoftDockerfileRoot); err != nil {
		return err
	}

	// Run the upstream "apply-templates.sh", but in this directory. This causes the script to
	// update our Dockerfiles using the data in our "versions.json". Keeping our own version of the
	// checked-in evaluated templates prevents merge conflicts in generated code when we merge
	// changes from upstream.
	cmd := exec.Command(filepath.Join(goDir, "apply-templates.sh"))
	// Run this script from the "src/microsoft" directory, which contains the versions.json file and
	// the Dockerfiles. The upstream script relies on the current working directory to decide where
	// to generate the Dockerfiles.
	cmd.Dir = microsoftDockerfileRoot
	// Make sure "apply-templates.sh" uses our checked-in copy of "jq-template.awk" instead of
	// downloading it on the fly.
	cmd.Env = append(cmd.Env, "BASHBREW_SCRIPTS=.")

	return run(cmd)
}

// getwd gets the current working dir or panics, for easy use in expressions.
func getwd() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Panic(err)
	}
	return wd
}

// runOrPanic uses 'run', then panics on error (such as nonzero exit code).
func runOrPanic(c *exec.Cmd) {
	if err := run(c); err != nil {
		log.Panic(err)
	}
}

// run sets up the command so it logs directly to our stdout/stderr streams, then runs it.
func run(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func copyDockerfileTemplates(srcDir, dstDir string) error {
	templates, err := filepath.Glob(filepath.Join(srcDir, "*.template"))
	if err != nil {
		return err
	}

	for _, t := range templates {
		dst := filepath.Join(dstDir, filepath.Base(t))
		fmt.Printf("---- Copying template %q to %q...\n", t, dst)
		if err := copyFile(t, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) (err error) {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Close()
}
