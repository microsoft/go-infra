// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
	"golang.org/x/tools/txtar"
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

	flag.StringVar(&buildAssetJSON, "build-asset-json", "", "[Required] The path of a build asset JSON file describing the Go build to update to.")
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

	golangSpecFileBytes, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", golangSpecFilepath)
	if err != nil {
		return err
	}

	golangSpecFileContent := string(golangSpecFileBytes)
	golangSpecFileContent, err = updateSpecFile(assets, golangSpecFileContent)
	if err != nil {
		return err
	}

	prevGoArchiveName, err := extractGoArchiveNameFromSpecFile(golangSpecFileContent)
	if err != nil {
		return err
	}

	// Validation (as described in previous response)
	if assets.GoSrcURL == "" || assets.GoSrcSHA256 == "" {
		return fmt.Errorf("invalid or missing GoSrcURL or GoSrcSHA256 in assets.json")
	}

	golangSignaturesFileBytes, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", golangSignaturesFilepath)
	if err != nil {
		return err
	}

	golangSignaturesFileBytes, err = updateSignatureFile(golangSignaturesFileBytes, prevGoArchiveName, path.Base(assets.GoSrcURL), assets.GoSrcSHA256)
	if err != nil {
		return err
	}

	cgManifestBytes, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", cgManifestFilepath)
	if err != nil {
		return err
	}

	cgManifestBytes, err = updateCGManifest(assets, cgManifestBytes)
	if err != nil {
		return err
	}

	ar := txtar.Archive{
		Comment: []byte("Bump version to " + assets.GoVersion().Full()),
		Files: []txtar.File{
			{Name: cgManifestFilepath, Data: cgManifestBytes},
			{Name: golangSignaturesFilepath, Data: golangSignaturesFileBytes},
			{Name: golangSpecFilepath, Data: []byte(golangSpecFileContent)},
		},
	}
	fmt.Println(string(txtar.Format(&ar)))

	return nil
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

	return specFileContent, nil
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
	Registrations []struct {
		Component struct {
			Type  string `json:"type"`
			Other struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				DownloadURL string `json:"downloadUrl"`
			} `json:"other"`
		} `json:"component"`
	} `json:"Registrations"`
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
		registration := &cgManifest.Registrations[i].Component.Other
		if registration.Name == "golang" {
			registration.Version = buildAssets.GoVersion().MajorMinorPatch()
			registration.DownloadURL = fmt.Sprintf(
				"https://github.com/microsoft/go/releases/download/%s/%s",
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
	updatedCgManifestContent, err := json.MarshalIndent(cgManifest, "", "  ") // Use indentation for readability
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated cgmanifest.json: %w", err)
	}

	updatedCgManifestContent = append(updatedCgManifestContent, '\n') // Add a newline at the end

	return updatedCgManifestContent, nil
}
