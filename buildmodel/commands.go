// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/gitpr"
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

// BuildAssetJsonFlags is a list of flags to create a build asset JSON file.
type BuildAssetJsonFlags struct {
	artifactsDir   *string
	branch         *string
	destinationURL *string
	sourceDir      *string

	output *string
}

// BindBuildAssetJsonFlags creates BuildAssetJsonFlags with the 'flag' package, globally registering
// them in the flag package so ParseBoundFlags will find them.
func BindBuildAssetJsonFlags() *BuildAssetJsonFlags {
	return &BuildAssetJsonFlags{
		artifactsDir:   flag.String("artifacts-dir", "eng/artifacts/bin", "The path of the directory to scan for artifacts."),
		branch:         flag.String("branch", "unknown", "The name of the branch that produced these artifacts."),
		destinationURL: flag.String("destination-url", "https://example.org/default", "The base URL where all files in the source directory can be downloaded from."),
		sourceDir:      flag.String("source-dir", "", "The path of the source code directory to scan for a VERSION file."),

		output: flag.String("o", "assets.json", "The path of the build asset JSON file to create."),
	}
}

// GenerateBuildAssetJson uses the specified parameters to summarize a build in a build asset json
// file.
func GenerateBuildAssetJson(f *BuildAssetJsonFlags) error {
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
	dryRun     *bool
	tempGitDir *string
	branch     *string

	origin *string
	to     *string

	githubPAT         *string
	githubPATReviewer *string

	buildAssetJSON  *string
	skipDockerfiles *bool
}

// BindPRFlags creates PRFlags with the 'flag' package, globally registering them in the flag
// package so ParseBoundFlags will find them.
func BindPRFlags() *PRFlags {
	var artifactsDir = filepath.Join(getwd(), "eng", "artifacts")
	return &PRFlags{
		dryRun:     flag.Bool("n", false, "Enable dry run: do not push, do not submit PR."),
		tempGitDir: flag.String("temp-git-dir", filepath.Join(artifactsDir, "sync-upstream-temp-repo"), "Location to create the temporary Git repo. Must not exist."),
		branch:     flag.String("branch", "", "Branch to submit PR into. Required, if origin is provided."),

		origin: flag.String("origin", "git@github.com:microsoft/go-docker", "Submit PR to this repo. \n[Need fetch Git permission.]"),
		to:     flag.String("to", "", "Push PR branch to this Git repository. Defaults to the same repo as 'origin' if not set.\n[Need push Git permission.]"),

		githubPAT:         flag.String("github-pat", "", "Submit the PR with this GitHub PAT, if specified."),
		githubPATReviewer: flag.String("github-pat-reviewer", "", "Approve the PR and turn on auto-merge with this PAT, if specified. Required, if github-pat specified."),

		buildAssetJSON:  flag.String("build-asset-json", "", "The path of a build asset JSON file describing the Go build to update to."),
		skipDockerfiles: flag.Bool("skip-dockerfiles", false, "If set, don't touch Dockerfiles.\nUpdating Dockerfiles requires bash/awk/jq, so when developing on Windows, skipping may be useful."),
	}
}

// SubmitUpdatePR runs an auto-update in a temp Git repo. If GitHub credentials are provided,
// submits the resulting commit as a GitHub PR, approves with a second account, and enables the
// GitHub auto-merge feature.
func SubmitUpdatePR(f *PRFlags) error {
	if _, err := os.Stat(*f.tempGitDir); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("temporary Git dir already exists: %v", *f.tempGitDir)
	}

	if *f.origin == "" {
		fmt.Println("Origin not specified. Nothing to do.")
		return nil
	}
	if *f.branch == "" {
		fmt.Println("No base branch is specified")
		return nil
	}

	if *f.to == "" {
		f.to = f.origin
	}

	b := gitpr.PRBranch{
		Name:    *f.branch,
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
				&b,
				githubUser,
				parsedPRHeadRemote.GetOwner(),
				parsedOrigin.GetOwner(),
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
	runOrPanic(exec.Command("git", "init", *f.tempGitDir))

	// newGitCmd creates a "git {args}" command that runs in the temp git dir.
	newGitCmd := func(args ...string) *exec.Cmd {
		c := exec.Command("git", args...)
		c.Dir = *f.tempGitDir
		return c
	}

	if existingPR != "" {
		// Fetch the existing PR head branch to add onto.
		runOrPanic(newGitCmd("fetch", "--no-tags", *f.to, b.PRBranchFetchRefspec()))
	} else {
		// Fetch the base branch to start the PR head branch.
		runOrPanic(newGitCmd("fetch", "--no-tags", *f.origin, b.BaseBranchFetchRefspec()))
	}
	runOrPanic(newGitCmd("checkout", b.PRBranch()))

	// Make changes to the files ins the temp repo.
	r, err := runUpdate(*f.tempGitDir, f)
	if err != nil {
		return err
	}

	// Check if there are any files in the stage. If not, we don't need to process this branch
	// anymore, because the merge + autoresolve didn't change anything.
	if err := run(newGitCmd("diff", "--quiet")); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			fmt.Printf("---- Detected changes in Git stage. Continuing to commit and submit PR.\n")
		} else {
			// Make sure we don't ignore more than we intended.
			panic(err)
		}
	} else {
		// If the diff had 0 exit code, there are no changes. Skip this branch's next steps.
		fmt.Printf("---- No updates to %v. Skipping.\n", b.Name)
		return nil
	}

	runOrPanic(newGitCmd("commit", "-a", "-m", "Update "+b.Name+" to "+r.buildAssets.Version))

	// Push the commit.
	args := []string{"push", *f.origin, b.PRPushRefspec()}
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
		p, err := gitpr.PostGitHub(parsedOrigin.GetOwnerSlashRepo(), b.CreateGitHubPR(parsedPRHeadRemote.GetOwner()), *f.githubPAT)
		fmt.Printf("%+v\n", p)
		if err != nil {
			return err
		}

		// For the rest of the method, the PR now exists.
		existingPR = p.NodeID
		fmt.Printf("---- Submitted brand new PR: %v\n", p.HTMLURL)

		fmt.Printf("---- Approving with reviewer account...\n")
		err = gitpr.MutateGraphQL(
			*f.githubPATReviewer,
			`mutation ($nodeID: ID!) {
				addPullRequestReview(input: {pullRequestId: $nodeID, event: APPROVE, body: "Thanks! Auto-approving."}) {
					clientMutationId
				}
			}`,
			map[string]interface{}{"nodeID": p.NodeID})
		if err != nil {
			return err
		}
	}

	fmt.Printf("---- Enabling auto-merge with reviewer account...\n")
	err = gitpr.MutateGraphQL(
		*f.githubPATReviewer,
		`mutation ($nodeID: ID!) {
			enablePullRequestAutoMerge(input: {pullRequestId: $nodeID, mergeMethod: MERGE}) {
				clientMutationId
			}
		}`,
		map[string]interface{}{"nodeID": existingPR})
	if err != nil {
		return err
	}

	fmt.Printf("---- PR for %v: Done.\n", b.Name)

	return nil
}

type updateResults struct {
	buildAssets *buildassets.BuildAssets
}

// runUpdate runs an auto-update process in the given Go Docker repository using the given update
// options. It finds the 'versions.json' and 'manifest.json' files, updates them appropriately, and
// optionally regenerates the Dockerfiles.
func runUpdate(repoRoot string, f *PRFlags) (*updateResults, error) {
	var versionsJsonPath = filepath.Join(repoRoot, "src", "microsoft", "versions.json")
	var manifestJsonPath = filepath.Join(repoRoot, "manifest.json")

	var dockerfileUpdateScript = filepath.Join(repoRoot, "eng", "update-dockerfiles.sh")

	if !*f.skipDockerfiles {
		missingTools := false
		for _, requiredCmd := range []string{"bash", "jq", "awk"} {
			if _, err := exec.LookPath(requiredCmd); err != nil {
				fmt.Printf("Unable to find '%s' in PATH. It is required to run 'eng/update-dockerfiles.sh'.\n", requiredCmd)
				fmt.Printf("Error: %s\n", err)
				missingTools = true
			}
		}
		if missingTools {
			return nil, fmt.Errorf("missing required tools to generate Dockerfiles. Make sure the tools are in PATH and try again, or pass '-skip-dockerfiles' to the command")
		}
	}

	versions := dockerversions.Versions{}
	if err := ReadJSONFile(versionsJsonPath, &versions); err != nil {
		return nil, err
	}

	manifest := dockermanifest.Manifest{}
	if err := ReadJSONFile(manifestJsonPath, &manifest); err != nil {
		return nil, err
	}

	var assets *buildassets.BuildAssets
	if *f.buildAssetJSON != "" {
		assets = &buildassets.BuildAssets{}
		if err := ReadJSONFile(*f.buildAssetJSON, &assets); err != nil {
			return nil, err
		}
		if err := UpdateVersions(assets, versions); err != nil {
			return nil, err
		}
		if err := WriteJSONFile(versionsJsonPath, &versions); err != nil {
			return nil, err
		}
	}

	fmt.Printf("Generating '%v' based on '%v'...\n", manifestJsonPath, versionsJsonPath)

	UpdateManifest(&manifest, versions)
	if err := WriteJSONFile(manifestJsonPath, &manifest); err != nil {
		return nil, err
	}

	if !*f.skipDockerfiles {
		fmt.Println("Generating Dockerfiles...")
		if err := run(exec.Command("bash", dockerfileUpdateScript)); err != nil {
			return nil, err
		}
	}
	return &updateResults{
		buildAssets: assets,
	}, nil
}

// getwd gets the current working dir or panics, for easy use in expressions.
func getwd() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}

// runOrPanic uses 'run', then panics on error (such as nonzero exit code).
func runOrPanic(c *exec.Cmd) {
	if err := run(c); err != nil {
		panic(err)
	}
}

// run sets up the command so it logs directly to our stdout/stderr streams, then runs it.
func run(c *exec.Cmd) error {
	fmt.Printf("---- Running command: %v %v\n", c.Path, c.Args)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
