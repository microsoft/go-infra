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
	"strings"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "update-azure-linux",
		Summary:     "Update the Go spec files for Azure Linux.",
		Description: " ",
		Handle:      updateAzureLinux,
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

	// Validation (as described in previous response)
	if assets.GoSrcURL == "" || assets.GoSrcHash == "" {
		return fmt.Errorf("invalid or missing GoSrcURL or GoSrcHash in assets.json")
	}

	golangSignaturesFileContent, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", golangSignaturesFilepath)
	if err != nil {
		return err
	}

	golangSignaturesFileContent, err = updateSignatureFile(golangSignaturesFileContent, assets.GoSrcURL, path.Base(assets.GoSrcURL), assets.GoSrcHash)
	if err != nil {
		return err
	}

	golangSpecFileContent, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", golangSpecFilepath)
	if err != nil {
		return err
	}

	golangSpecFileContent, err = updateSpecFile(assets, golangSpecFileContent)
	if err != nil {
		return err
	}

	cgManifestContent, err := downloadFileFromRepo(ctx, client, "microsoft", "azurelinux", "3.0-dev", cgManifestFilepath)
	if err != nil {
		return err
	}

	cgManifestContent, err = updateCGManifest(assets, cgManifestContent)
	if err != nil {
		return err
	}

	// #todo
	_, _, _ = golangSpecFileContent, cgManifestContent, golangSignaturesFileContent

	return nil
}

func loadBuildAssets(assetFilePath string) (*buildassets.BuildAssets, error) {
	assets := new(buildassets.BuildAssets)

	if err := stringutil.ReadJSONFile(assetFilePath, &assets); err != nil {
		return nil, fmt.Errorf("error loading build assets: %w", err)
	}

	return assets, nil
}

func downloadFileFromRepo(ctx context.Context, client *github.Client, owner, repo, branch, filePath string) ([]byte, error) {
	fileContent, err := githubutil.DownloadFile(ctx, client, owner, repo, branch, filePath)
	if err != nil {
		if errors.Is(err, githubutil.ErrNotExists) {
			// Handle the specific case of the file not existing
			return nil, fmt.Errorf("file '%s' not found in repository '%s' on branch '%s'", filePath, repo, branch)
		} else {
			// Handle other errors (network issues, authentication, etc.)
			return nil, fmt.Errorf("failed to download file '%s': %w", filePath, err)
		}
	}

	if len(fileContent) == 0 {
		return nil, fmt.Errorf("downloaded file %s is empty", filePath)
	}
	return fileContent, nil
}

const (
	golangSignaturesFilepath = "SPECS/golang/golang.signatures.json"
	golangSpecFilepath       = "SPECS/golang/golang.spec"
	cgManifestFilepath       = "cgmanifest.json"
)

var (
	specFileGoFilenameRegex = regexp.MustCompile(`(%global ms_go_filename  )(.+)`)
	specFileRevisionRegex   = regexp.MustCompile(`(%global ms_go_revision  )(.+)`)
)

func extractGoArchiveNameFromSpecFile(specContent string) (string, error) {
	matches := specFileGoFilenameRegex.FindStringSubmatch(specContent)

	if len(matches) < 2 {
		return "", fmt.Errorf("no match found")
	}

	return strings.TrimSpace(matches[2]), nil
}

func updateGoArchiveNameInSpecFile(specContent, newArchiveName string) (string, error) {
	if !specFileGoFilenameRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}

	updatedContent := specFileGoFilenameRegex.ReplaceAllString(specContent, "${1}"+newArchiveName)
	return updatedContent, nil
}

func updateGoRevisionInSpecFile(specContent, newRevisionVersion string) (string, error) {
	if !specFileRevisionRegex.MatchString(specContent) {
		return "", fmt.Errorf("no Go revision version declaration found in spec content")
	}

	updatedContent := specFileRevisionRegex.ReplaceAllString(specContent, "${1}"+newRevisionVersion)
	return updatedContent, nil
}

func updateSpecFile(buildAssets *buildassets.BuildAssets, signatureFileContent []byte) ([]byte, error) {
	content := string(signatureFileContent)

	_ = content

	return nil, nil
}

// JSONSignature structure to map the provided JSON data
type JSONSignature struct {
	Signatures map[string]string `json:"Signatures"`
}

func updateSignatureFile(jsonData []byte, oldFilename, newFilename, newHash string) ([]byte, error) {
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

	return updatedCgManifestContent, nil
}
