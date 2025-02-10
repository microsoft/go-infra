// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
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

const (
	AzureLinuxBuddyBuildURL           = "https://dev.azure.com/mariner-org/mariner/_build?definitionId=2190"
	AzureLinuxSourceTarballPublishURL = "https://dev.azure.com/mariner-org/mariner/_build?definitionId=2284"
)

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

	pat := githubutil.BindPATFlag()

	if err := p(); err != nil {
		return err
	}

	start := time.Now()
	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	// Set custom user agent to help GitHub identify the bot if necessary.
	client.UserAgent = "microsoft/go-infra update-azure-linux"

	// Check that the PAT has the necessary scopes.
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

		specPath := golangSpecFilepath(assets, latestMajor)
		golangSpecFileBytes, err := downloadFileFromRepo(ctx, client, upstream, repo, upstreamCommitSHA, specPath)
		if err != nil {
			return err
		}

		golangSpecFileContent := string(golangSpecFileBytes)

		prevGoArchiveName, err := extractGoArchiveNameFromSpecFile(golangSpecFileContent)
		if err != nil {
			return err
		}

		golangSpecFileContent, err = updateSpecFile(assets, start, golangSpecFileContent)
		if err != nil {
			return err
		}

		// Validation (as described in previous response)
		if assets.GoSrcURL == "" || assets.GoSrcSHA256 == "" {
			return fmt.Errorf("invalid or missing GoSrcURL or GoSrcSHA256 in assets.json")
		}

		signaturesPath := golangSignaturesFilepath(assets, latestMajor)
		golangSignaturesFileBytes, err := downloadFileFromRepo(ctx, client, upstream, repo, upstreamCommitSHA, signaturesPath)
		if err != nil {
			return err
		}

		golangSignaturesFileBytes, err = updateSignatureFile(golangSignaturesFileBytes, prevGoArchiveName, path.Base(assets.GoSrcURL), assets.GoSrcSHA256)
		if err != nil {
			return err
		}

		cgManifestBytes, err := downloadFileFromRepo(ctx, client, upstream, repo, upstreamCommitSHA, cgManifestFilepath)
		if err != nil {
			return err
		}

		cgManifestBytes, err = updateCGManifest(assets, cgManifestBytes)
		if err != nil {
			return err
		}

		tree := []*github.TreeEntry{
			{
				Path:    github.String(specPath),
				Content: &golangSpecFileContent,
				Mode:    github.String(githubutil.TreeModeFile),
			},
			{
				Path:    github.String(signaturesPath),
				Content: github.String(string(golangSignaturesFileBytes)),
				Mode:    github.String(githubutil.TreeModeFile),
			},
			{
				Path:    github.String(cgManifestFilepath),
				Content: github.String(string(cgManifestBytes)),
				Mode:    github.String(githubutil.TreeModeFile),
			},
		}

		createTree, _, err := client.Git.CreateTree(ctx, owner, repo, upstreamCommit.Tree.GetSHA(), tree)
		if err != nil {
			return err
		}

		createCommit, _, err := client.Git.CreateCommit(ctx, owner, repo, &github.Commit{
			Message: github.String(generatePRTitleFromAssets(assets, security)),
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
			Title: github.String(generatePRTitleFromAssets(assets, security)),
			Head:  &prHead,
			Base:  github.String(baseBranch),
			// We don't know the PR number yet, so pass 0 to use a placeholder.
			Body:  github.String(GeneratePRDescription(assets, latestMajor, security, notify, 0)),
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
			Body: github.String(GeneratePRDescription(assets, latestMajor, security, notify, pr.GetNumber())),
		})
		return err
	}); err != nil {
		return fmt.Errorf("failed to update pull request description: %w\n", err)
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
	return fmt.Sprintf("refs/heads/update-go-%s", assets.GoVersion().Full())
}

func generatePRTitleFromAssets(assets *buildassets.BuildAssets, security bool) string {
	var b strings.Builder
	if security {
		b.WriteString("(security) ")
	}
	b.WriteString("golang: bump Go version to ")
	b.WriteString(assets.GoVersion().Full())
	return b.String()
}

func GeneratePRDescription(assets *buildassets.BuildAssets, latestMajor, security bool, notify string, prNumber int) string {
	// Use calls to fmt.Fprint* family for readability with consistency.
	// Ignore errors because they're acting upon a simple builder.
	var b strings.Builder
	fmt.Fprint(&b, "Hi! 👋 I'm the Microsoft team's bot. This is an automated pull request I generated to bump the Go version to ")
	fmt.Fprintf(&b, "[%s](%s).\n\n", assets.GoVersion().Full(), githubReleaseURL(assets))

	if security {
		fmt.Fprint(&b, "**This update contains security fixes.**\n\n")
	}

	fmt.Fprint(&b, "I'm not able to run the Azure Linux pipelines yet, so the Microsoft release runner will need to finalize this PR.")
	if notify != "" && notify != "ghost" {
		fmt.Fprintf(&b, " @%s", notify)
	}
	fmt.Fprint(&b, "\n\nFinalization steps:\n")

	printCopiableOption := func(name, value string) {
		fmt.Fprintf(&b, "  %s:  \n", name)
		fmt.Fprint(&b, "  ```\n")
		fmt.Fprintf(&b, "  %s\n", value)
		fmt.Fprint(&b, "  ```\n")
	}

	fmt.Fprintf(&b, "- Trigger [Source Tarball Publishing](%s) with:  \n", AzureLinuxSourceTarballPublishURL)
	printCopiableOption("Full Name", path.Base(assets.GoSrcURL))
	printCopiableOption("URL", githubReleaseDownloadURL(assets))

	fmt.Fprintf(&b, "- Trigger [the Buddy Build](%s) with:  \n", AzureLinuxBuddyBuildURL)
	if prNumber == 0 {
		// If we aren't able to update the PR later to put the PR number in, at least leave
		// instructions the release runner can follow.
		fmt.Fprint(&b, "  First field: `PR-` then the number of this PR.  \n")
	} else {
		printCopiableOption("First field", fmt.Sprintf("PR-%v", prNumber))
	}

	printCopiableOption("Core spec", golangSpecName(assets, latestMajor))

	fmt.Fprint(&b, "- Post a PR comment with the URL of the triggered Buddy Build.\n")
	fmt.Fprint(&b, "- Mark this draft PR as ready for review.\n")

	fmt.Fprint(&b, "\nThanks!\n")
	return b.String()
}

func loadBuildAssets(assetFilePath string) (*buildassets.BuildAssets, error) {
	assets := new(buildassets.BuildAssets)

	if err := stringutil.ReadJSONFile(assetFilePath, assets); err != nil {
		return nil, fmt.Errorf("error loading build assets: %w", err)
	}

	return assets, nil
}

func downloadFileFromRepo(ctx context.Context, client *github.Client, owner, repo, ref, filePath string) ([]byte, error) {
	fileContent, err := githubutil.DownloadFile(ctx, client, owner, repo, ref, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to download file %q: %w", filePath, err)
	}
	return fileContent, nil
}

func golangSignaturesFilepath(assets *buildassets.BuildAssets, latestMajor bool) string {
	return "SPECS/golang/" + golangSpecName(assets, latestMajor) + ".signatures.json"
}

func golangSpecFilepath(assets *buildassets.BuildAssets, latestMajor bool) string {
	return "SPECS/golang/" + golangSpecName(assets, latestMajor) + ".spec"
}

func golangSpecName(assets *buildassets.BuildAssets, latestMajor bool) string {
	if latestMajor {
		return "golang"
	}
	return "golang-" + assets.GoVersion().MajorMinor()
}

const cgManifestFilepath = "cgmanifest.json"

var specFileVersionRegex = regexp.MustCompile(`(Version: +)(.+)`)

func extractVersionFromSpecFile(content string) (*goversion.GoVersion, error) {
	matches := specFileVersionRegex.FindStringSubmatch(content)
	if matches == nil {
		return nil, fmt.Errorf("no Version declaration found in spec content")
	}
	return goversion.New(matches[2]), nil
}

func updateVersionInSpecFile(content string, newVersion string) string {
	return specFileVersionRegex.ReplaceAllString(
		content,
		"${1}"+escapeRegexReplacementValue(newVersion))
}

var specFileReleaseRegex = regexp.MustCompile(`(Release: +)(.+)(%\{\?dist\})`)

func extractReleaseFromSpecFile(content string) (int, error) {
	matches := specFileReleaseRegex.FindStringSubmatch(content)
	if matches == nil {
		return 0, fmt.Errorf("no Release declaration found in spec content")
	}
	releaseInt, err := strconv.Atoi(matches[2])
	if err != nil {
		return 0, fmt.Errorf("failed to parse old Release number: %v", err)
	}
	return releaseInt, nil
}

func updateReleaseInSpecFile(content string, newRelease string) string {
	return specFileReleaseRegex.ReplaceAllString(
		content,
		"${1}"+escapeRegexReplacementValue(newRelease)+"${3}")
}

var specFileGoFilenameRegex = regexp.MustCompile(`(%global ms_go_filename +)(.+)`)

func extractGoArchiveNameFromSpecFile(specContent string) (string, error) {
	matches := specFileGoFilenameRegex.FindStringSubmatch(specContent)

	if matches == nil {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}

	return strings.TrimSpace(matches[2]), nil
}

func updateGoArchiveNameInSpecFile(specContent, newArchiveName string) (string, error) {
	if !specFileGoFilenameRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}
	return specFileGoFilenameRegex.ReplaceAllString(
		specContent,
		"${1}"+escapeRegexReplacementValue(newArchiveName),
	), nil
}

var specFileRevisionRegex = regexp.MustCompile(`(%global ms_go_revision +)(.+)`)

func updateGoRevisionInSpecFile(specContent, newRevisionVersion string) (string, error) {
	if !specFileRevisionRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go revision version declaration found in spec content")
	}
	return specFileRevisionRegex.ReplaceAllString(
		specContent,
		"${1}"+escapeRegexReplacementValue(newRevisionVersion),
	), nil
}

func updateSpecFile(assets *buildassets.BuildAssets, changelogDate time.Time, specFileContent string) (string, error) {
	if len(specFileContent) == 0 {
		return "", fmt.Errorf("provided spec file content is empty")
	}

	// Gather the old spec file data we need to make the updated one.
	oldVersion, err := extractVersionFromSpecFile(specFileContent)
	if err != nil {
		return "", err
	}
	oldRelease, err := extractReleaseFromSpecFile(specFileContent)
	if err != nil {
		return "", err
	}

	newVersion, newRelease := updateSpecVersion(assets, oldVersion, oldRelease)

	// Perform updates with a series of replacements.
	specFileContent, err = updateGoArchiveNameInSpecFile(specFileContent, path.Base(assets.GoSrcURL))
	if err != nil {
		return "", fmt.Errorf("error updating Go archive name in spec file: %w", err)
	}
	specFileContent, err = updateGoRevisionInSpecFile(specFileContent, assets.GoVersion().Revision)
	if err != nil {
		return "", fmt.Errorf("error updating Go revision in spec file: %w", err)
	}
	specFileContent = updateVersionInSpecFile(specFileContent, newVersion)
	specFileContent = updateReleaseInSpecFile(specFileContent, newRelease)
	specFileContent = addChangelogToSpecFile(specFileContent, changelogDate, assets, newVersion, newRelease)

	return specFileContent, nil
}

func addChangelogToSpecFile(specFile string, changelogDate time.Time, assets *buildassets.BuildAssets, newVersion, newRelease string) string {
	const template = `%%changelog
* %s Microsoft Golang Bot <microsoft-golang-bot@users.noreply.github.com> - %s-%s
- Bump version to %s
`
	formattedTime := changelogDate.Format("Mon Jan 02 2006")

	changelog := fmt.Sprintf(
		template,
		formattedTime,
		newVersion, newRelease,
		assets.GoVersion().Full())

	return strings.Replace(specFile, "%changelog", changelog, 1)
}

// JSONSignature structure to map the provided JSON data
type JSONSignature struct {
	Signatures map[string]string `json:"Signatures"`
}

func updateSignatureFile(jsonData []byte, oldFilename, newFilename, newHash string) ([]byte, error) {
	if len(jsonData) == 0 {
		return nil, fmt.Errorf("provided signature file data is empty")
	}

	var data JSONSignature
	if err := json.Unmarshal(jsonData, &data); err != nil {
		return nil, err
	}

	// Check if the oldFilename exists in the map
	if _, exists := data.Signatures[oldFilename]; !exists {
		return nil, errors.New("old filename not found in signatures")
	}

	// Update the filename and hash in the map
	delete(data.Signatures, oldFilename) // Remove the old entry
	// add new filename and hash
	data.Signatures[newFilename] = newHash

	// Marshal the updated JSON back to a byte slice
	updatedJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, err
	}

	updatedJSON = append(updatedJSON, '\n') // Add a newline at the end

	return updatedJSON, nil
}

type CGManifest struct {
	Registrations []Registration `json:"Registrations"`
	Version       int            `json:"Version"`
}

type Registration struct {
	Component struct {
		Type    string `json:"type"`
		Comment string `json:"comment,omitempty"`
		Other   struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			DownloadURL string `json:"downloadUrl"`
		} `json:"other"`
	} `json:"component"`
}

func updateCGManifest(buildAssets *buildassets.BuildAssets, cgManifestContent []byte) ([]byte, error) {
	if len(cgManifestContent) == 0 {
		return nil, fmt.Errorf("provided CG manifest content is empty")
	}

	var cgManifest CGManifest
	if err := json.Unmarshal(cgManifestContent, &cgManifest); err != nil {
		return nil, fmt.Errorf("failed to parse cgmanifest.json: %w", err)
	}

	updated := false
	for i := range cgManifest.Registrations {
		reg := &cgManifest.Registrations[i]
		if reg.Component.Other.Name != "golang" {
			continue
		}
		// Azure Linux maintains two major versions. Only update this one.
		regVersion := goversion.New(reg.Component.Other.Version)
		if regVersion.MajorMinor() != buildAssets.GoVersion().MajorMinor() {
			continue
		}
		reg.Component.Other.Version = buildAssets.GoVersion().MajorMinorPatch()
		reg.Component.Other.DownloadURL = githubReleaseDownloadURL(buildAssets)
		updated = true
		break
	}

	if !updated {
		return nil, fmt.Errorf("golang component not found in cgmanifest.json")
	}

	// Serialize the updated cgManifest back to JSON
	buf := new(bytes.Buffer)
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")

	err := encoder.Encode(cgManifest) // Use indentation for readability
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated cgmanifest.json: %w", err)
	}

	return buf.Bytes(), nil
}

func githubReleaseURL(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf(
		"https://github.com/microsoft/go/releases/tag/v%s",
		assets.GoVersion().Full(),
	)
}

func githubReleaseDownloadURL(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf(
		"https://github.com/microsoft/go/releases/download/v%s/%s",
		assets.GoVersion().Full(),
		path.Base(assets.GoSrcURL),
	)
}

func updateSpecVersion(assets *buildassets.BuildAssets, oldVersion *goversion.GoVersion, oldRelease int) (version, release string) {
	// Decide on the new Azure Linux golang package release number.
	//
	// We don't use assets.GoVersion().Revision because Azure Linux may have incremented the
	// release version manually for an Azure-Linux-specific fix.
	var newRelease int
	if assets.GoVersion().MajorMinorPatch() != oldVersion.MajorMinorPatch() {
		// When updating to a new upstream Go version, reset release number to 1.
		newRelease = 1
	} else {
		// When the upstream Go version didn't change, increment the release number. This means
		// there has been a patch specific to Microsoft build of Go.
		newRelease = oldRelease + 1
	}

	return assets.GoVersion().MajorMinorPatch(), strconv.Itoa(newRelease)
}

// escapeRegexReplacementValue returns s where all "$" signs are replaced with with "$$" for the
// purposes of passing the result to ReplaceAllString. Use this to make sure a replacement value
// doesn't get interpreted as part of a regex replacement pattern in [Regexp.Expand].
//
// As of writing, we don't expect "$" in the replacement values. However, it's easy to handle, and
// it's cleaner than rejecting "$" and propagating an error.
func escapeRegexReplacementValue(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}
