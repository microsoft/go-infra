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

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-azure-linux",
		Summary: "Experimental: Update the Go spec files for Azure Linux and print the result without pushing.",
		Description: `
See https://github.com/microsoft/go-lab/issues/79
`,
		Handle: updateAzureLinux,
	})
}

func updateAzureLinux(p subcmd.ParseFunc) error {
	var buildAssetJSON string
	var owner string
	var repo string
	var baseBranch string
	var updateBranch string
	var buddyBuildID string
	var upgradePipelineRunID string

	flag.StringVar(&owner, "owner", "microsoft", "The owner of the repository.")
	flag.StringVar(&repo, "repo", "azurelinux", "The repository to update.")
	flag.StringVar(&baseBranch, "base-branch", "refs/heads/3.0-dev", "The base branch to download files from.")
	flag.StringVar(&updateBranch, "update-branch", "", "The target branch to update files in.")
	flag.StringVar(&buildAssetJSON, "build-asset-json", "assets.json", "The path of a build asset JSON file describing the Go build to update to.")
	flag.StringVar(&buddyBuildID, "buddy-build-id", "", "The job ID for the buddy build in Azure DevOps")
	flag.StringVar(&upgradePipelineRunID, "upgrade-pipeline-run-id", "", "The run ID for the Upgrade pipeline in Azure DevOps")

	pat := githubutil.BindPATFlag()

	if err := p(); err != nil {
		return err
	}

	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	assets, err := loadBuildAssets(buildAssetJSON)
	if err != nil {
		return err
	}

	if updateBranch == "" {
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

	golangSpecFileContent, err = updateSpecFile(assets, golangSpecFileContent)
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
			Message: github.String("Bump version to " + assets.GoVersion().Full()),
			Content: newContent,
			SHA:     file.SHA,
			Branch:  github.String(updateBranch),
		})
		if err != nil {
			return fmt.Errorf("Failed to update file %q: %v", filePath, err)
		}
	}

	pr, _, err := client.PullRequests.Create(ctx, owner, repo, &github.NewPullRequest{
		Title: github.String(generatePRTitleFromAssets(assets)),
		Head:  github.String(updateBranch),
		Base:  github.String(baseBranch),
		Body:  github.String(GeneratePRDescription(upgradePipelineRunID, buddyBuildID, assets)),
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

func GeneratePRDescription(upgradePipelineRunID, buddyBuildID string, assets *buildassets.BuildAssets) string {
	template := `Bump Go Version to %s
Upgrade pipeline run -> https://dev.azure.com/mariner-org/mariner/_build/results?buildId=%s&view=results

Buddy build -> https://dev.azure.com/mariner-org/mariner/_build/results?buildId=%s&view=results
`
	return fmt.Sprintf(template, assets.GoVersion().Full(), upgradePipelineRunID, buddyBuildID)
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

var (
	specFileGoFilenameRegex = regexp.MustCompile(`(%global ms_go_filename +)(.+)`)
	specFileRevisionRegex   = regexp.MustCompile(`(%global ms_go_revision +)(.+)`)
	specFileVersionRegex    = regexp.MustCompile(`(Version: +)(.+)`)
	specFileReleaseRegex    = regexp.MustCompile(`(Release: +)(.+)(%\{\?dist\})`)
)

func extractGoArchiveNameFromSpecFile(specContent string) (string, error) {
	matches := specFileGoFilenameRegex.FindStringSubmatch(specContent)

	if matches == nil {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}

	return strings.TrimSpace(matches[2]), nil
}

func updateGoArchiveNameInSpecFile(specContent, newArchiveName string) (string, error) {
	if strings.Contains(newArchiveName, "$") {
		return "", fmt.Errorf("new archive name %q contains unexpected $", newArchiveName)
	}
	if !specFileGoFilenameRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}

	updatedContent := specFileGoFilenameRegex.ReplaceAllString(specContent, "${1}"+newArchiveName)
	return updatedContent, nil
}

func updateGoRevisionInSpecFile(specContent, newRevisionVersion string) (string, error) {
	if strings.Contains(newRevisionVersion, "$") {
		return "", fmt.Errorf("new revision version %q contains unexpected $", newRevisionVersion)
	}
	if !specFileRevisionRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go revision version declaration found in spec content")
	}

	updatedContent := specFileRevisionRegex.ReplaceAllString(specContent, "${1}"+newRevisionVersion)
	return updatedContent, nil
}

func updateSpecFile(assets *buildassets.BuildAssets, specFileContent string) (string, error) {
	if len(specFileContent) == 0 {
		return "", fmt.Errorf("provided spec file content is empty")
	}

	specFileContent, err := updateGoArchiveNameInSpecFile(specFileContent, path.Base(assets.GoSrcURL))
	if err != nil {
		return "", fmt.Errorf("error updating Go archive name in spec file: %w", err)
	}
	specFileContent, err = updateGoRevisionInSpecFile(specFileContent, assets.GoVersion().Revision)
	if err != nil {
		return "", fmt.Errorf("error updating Go revision in spec file: %w", err)
	}

	var oldVersion *goversion.GoVersion
	if matches := specFileVersionRegex.FindStringSubmatch(specFileContent); matches == nil {
		return "", fmt.Errorf("no Version declaration found in spec content")
	} else {
		oldVersion = goversion.New(matches[2])
	}

	var oldRelease int
	if matches := specFileReleaseRegex.FindStringSubmatch(specFileContent); matches == nil {
		return "", fmt.Errorf("no Release declaration found in spec content")
	} else {
		var err error
		oldRelease, err = strconv.Atoi(matches[2])
		if err != nil {
			return "", fmt.Errorf("failed to parse Release number: %w", err)
		}
	}

	newVersion := assets.GoVersion().MajorMinorPatch()
	if strings.Contains(newVersion, "$") {
		return "", fmt.Errorf("new version %q contains unexpected $", newVersion)
	}
	specFileContent = specFileVersionRegex.ReplaceAllString(specFileContent, "${1}"+newVersion)

	// For servicing patches, increment release. Azure Linux may have incremented it manually for a
	// Azure-Linux-specific fix, so this is independent of Microsoft Go releases. When updating to a
	// new major/minor version (as semver refers to them), reset release to 1.
	var newRelease int
	if assets.GoVersion().MajorMinor() != oldVersion.MajorMinor() {
		newRelease = 1
	} else {
		newRelease = oldRelease + 1
	}

	specFileContent = specFileReleaseRegex.ReplaceAllString(specFileContent, "${1}"+strconv.Itoa(newRelease)+"${3}")
	specFileContent = addChangelogToSpecFile(specFileContent, assets)

	return specFileContent, nil
}

func addChangelogToSpecFile(specFile string, assets *buildassets.BuildAssets) string {
	template := `%%changelog
* %s Microsoft Golang Bot <microsoft-golang-bot@users.noreply.github.com> - %s
- Bump version to %s
`
	t := time.Now() // Get the current time
	formattedTime := t.Format("Mon Jan 02 2006")

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
	Registrations []json.RawMessage `json:"Registrations"`
	Version       int               `json:"Version"`
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
	for i, reg := range cgManifest.Registrations {
		registration := make(map[string]interface{})
		if err := json.Unmarshal(reg, &registration); err != nil {
			return nil, fmt.Errorf("failed to parse registration in cgmanifest.json: %w", err)
		}

		component, ok := registration["component"].(map[string]interface{})
		if !ok {
			continue
		}

		other, ok := component["other"].(map[string]interface{})
		if !ok {
			continue
		}

		if name, ok := other["name"].(string); ok && name == "golang" {
			other["version"] = buildAssets.GoVersion().MajorMinorPatch()
			other["downloadUrl"] = fmt.Sprintf(
				"https://github.com/microsoft/go/releases/download/v%s/%s",
				buildAssets.GoVersion().Full(),
				path.Base(buildAssets.GoSrcURL),
			)

			buf := new(bytes.Buffer)
			encoder := json.NewEncoder(buf)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(registration); err != nil {
				return nil, fmt.Errorf("failed to marshal updated registration in cgmanifest.json: %w", err)
			}

			cgManifest.Registrations[i] = buf.Bytes()
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

	buf.WriteByte('\n') // Add a newline at the end

	return buf.Bytes(), nil
}
