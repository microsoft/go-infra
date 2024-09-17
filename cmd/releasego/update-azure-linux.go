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
	"path"
	"regexp"
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
Updates the golang package spec file in [upstream]/[repo] to build the version of Go specified in
the provided build asset JSON file. If [upstream] and [owner] differ, the PR will be created in a
fork of the Azure Linux repo under [owner]. If [owner] user doesn't already have an Azure Linux
fork, it is created.
`,
		Handle: updateAzureLinux,
	})
}

func updateAzureLinux(p subcmd.ParseFunc) error {
	var baseBranch string
	var buildAssetJSON string
	var upstream string
	var owner string
	var repo string
	var updateBranch string

	flag.StringVar(&baseBranch, "base-branch", "refs/heads/3.0-dev", "The base branch to download files from.")
	flag.StringVar(&buildAssetJSON, "build-asset-json", "assets.json", "The path of a build asset JSON file describing the Go build to update to.")
	flag.StringVar(&upstream, "upstream", "microsoft", "The owner of the Azure Linux repository.")
	flag.StringVar(&owner, "owner", "microsoft", "The owner of the repository to create the dev branch in.")
	flag.StringVar(&repo, "repo", "azurelinux", "The upstream repository name to update.")
	flag.StringVar(&updateBranch, "update-branch", "", "The target branch to update files in.")

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

	assets, err := loadBuildAssets(buildAssetJSON)
	if err != nil {
		return err
	}

	if updateBranch == "nil" || updateBranch == "" {
		updateBranch = generateUpdateBranchNameFromAssets(assets)
	}

	golangSpecFileBytes, err := downloadFileFromRepo(ctx, client, owner, repo, baseBranch, golangSpecFilepath)
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

	golangSignaturesFileBytes, err := downloadFileFromRepo(ctx, client, owner, repo, baseBranch, golangSignaturesFilepath)
	if err != nil {
		return err
	}

	golangSignaturesFileBytes, err = updateSignatureFile(golangSignaturesFileBytes, prevGoArchiveName, path.Base(assets.GoSrcURL), assets.GoSrcSHA256)
	if err != nil {
		return err
	}

	cgManifestBytes, err := downloadFileFromRepo(ctx, client, owner, repo, baseBranch, cgManifestFilepath)
	if err != nil {
		return err
	}

	cgManifestBytes, err = updateCGManifest(assets, cgManifestBytes)
	if err != nil {
		return err
	}

	ref, _, err := client.Git.GetRef(ctx, owner, repo, baseBranch)
	if err != nil {
		return fmt.Errorf("failed to get ref: %v", err)
	}

	newRef := &github.Reference{
		Ref:    github.String(updateBranch),
		Object: &github.GitObject{SHA: ref.Object.SHA},
	}

	if _, _, err = client.Git.CreateRef(ctx, owner, repo, newRef); err != nil {
		return fmt.Errorf("Failed to create ref: %v", err)
	}

	updatedFiles := map[string][]byte{
		golangSpecFilepath:       []byte(golangSpecFileContent),
		golangSignaturesFilepath: golangSignaturesFileBytes,
		cgManifestFilepath:       cgManifestBytes,
	}

	for filePath, newContent := range updatedFiles {
		// Get the file to update
		file, _, _, err := client.Repositories.GetContents(ctx, owner, repo, filePath, &github.RepositoryContentGetOptions{Ref: updateBranch})
		if err != nil {
			return fmt.Errorf("Failed to get file %q: %v", filePath, err)
		}

		// Update the file
		_, _, err = client.Repositories.UpdateFile(ctx, owner, repo, filePath, &github.RepositoryContentFileOptions{
			Message: github.String(generatePRTitleFromAssets(assets)),
			Content: newContent,
			SHA:     file.SHA,
			Branch:  github.String(updateBranch),
		})
		if err != nil {
			return fmt.Errorf("failed to update file %q: %v", filePath, err)
		}
	}

	pr, _, err := client.PullRequests.Create(ctx, owner, repo, &github.NewPullRequest{
		Title: github.String(generatePRTitleFromAssets(assets)),
		Head:  github.String(updateBranch),
		Base:  github.String(baseBranch),
		Body:  github.String(GeneratePRDescription(assets)),
	})
	if err != nil {
		return fmt.Errorf("failed to create PR: %v", err)
	}

	// This function utilizes the Issues API because in GitHub's API model, pull requests are treated as a special type of issue.
	// While GitHub provides a dedicated PullRequests API, it doesn't currently offer a method for adding labels directly to pull requests.
	//
	// Therefore, we use the Issues.AddLabelsToIssue method, passing the pull request's number (which is equivalent to its issue number)
	// to apply the labels.
	//
	// This approach is a workaround until GitHub potentially adds direct label management for pull requests in their API.
	if _, _, err := client.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), []string{"3.0-dev", "Automatic PR"}); err != nil {
		return fmt.Errorf("error adding label to pull request: %w\n", err)
	}

	fmt.Printf("Pull request created successfully: %s\n", pr.GetHTMLURL())

	return nil
}

func generateUpdateBranchNameFromAssets(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf("refs/heads/update-go-%s", assets.GoVersion().Full())
}

func generatePRTitleFromAssets(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf("Bump Go Version to %s", assets.GoVersion().Full())
}

func GeneratePRDescription(assets *buildassets.BuildAssets) string {
	const format = "Bump Go Version to %s"
	return fmt.Sprintf(format, assets.GoVersion().Full())
}

func loadBuildAssets(assetFilePath string) (*buildassets.BuildAssets, error) {
	assets := new(buildassets.BuildAssets)

	if err := stringutil.ReadJSONFile(assetFilePath, assets); err != nil {
		return nil, fmt.Errorf("error loading build assets: %w", err)
	}

	return assets, nil
}

func downloadFileFromRepo(ctx context.Context, client *github.Client, owner, repo, branch, filePath string) ([]byte, error) {
	fileContent, err := githubutil.DownloadFile(ctx, client, owner, repo, branch, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to download file %q: %w", filePath, err)
	}
	return fileContent, nil
}

const (
	golangSignaturesFilepath = "SPECS/golang/golang.signatures.json"
	golangSpecFilepath       = "SPECS/golang/golang.spec"
	cgManifestFilepath       = "cgmanifest.json"
)

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
	specFileContent = addChangelogToSpecFile(specFileContent, changelogDate, assets)

	return specFileContent, nil
}

func addChangelogToSpecFile(specFile string, changelogDate time.Time, assets *buildassets.BuildAssets) string {
	template := `%%changelog
* %s Microsoft Golang Bot <microsoft-golang-bot@users.noreply.github.com> - %s
- Bump version to %s
`
	formattedTime := changelogDate.Format("Mon Jan 02 2006")

	changelog := fmt.Sprintf(template, formattedTime, assets.GoVersion().Full(), assets.GoVersion().Full())

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
		if reg.Component.Other.Name == "golang" {
			reg.Component.Other.Version = buildAssets.GoVersion().MajorMinorPatch()
			reg.Component.Other.DownloadURL = fmt.Sprintf(
				"https://github.com/microsoft/go/releases/download/v%s/%s",
				buildAssets.GoVersion().Full(),
				path.Base(buildAssets.GoSrcURL),
			)
			updated = true
			break
		}
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
		// there has been a patch specific to Microsoft Go.
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
