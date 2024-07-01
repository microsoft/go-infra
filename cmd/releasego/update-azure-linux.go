// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"path"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/stringutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:        "update-azure-linux",
		Summary:     "Update the Azure Linux Microsoft Go version after release.",
		Description: "",
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

	golangSignaturesFileContent, err = updateSignatureFile(assets, golangSignaturesFileContent)
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
	fileContent, exists, err := githubutil.DownloadFile(ctx, client, owner, repo, branch, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to download file %s: %w", filePath, err)
	}
	if !exists {
		return nil, fmt.Errorf("file %s not found in %s repository", filePath, repo)
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

func updateSpecFile(buildAssets *buildassets.BuildAssets, signatureFileContent []byte) ([]byte, error) {
	// content := string(signatureFileContent)
	//
	// // Define the regex patterns
	// msGoFilenamePattern := regexp.MustCompile(`(%global ms_go_filename\s+)\S+`)
	// msGoRevisionPattern := regexp.MustCompile(`(%global ms_go_revision\s+)\d+`)
	// versionPattern := regexp.MustCompile(`(Version:\s+)\d+\.\d+\.\d+`)
	//
	// // Replace the matched patterns with the new values
	// content = msGoFilenamePattern.ReplaceAllString(content, `${1}`+msGoFilename)
	// content = msGoRevisionPattern.ReplaceAllString(content, `${1}`+msGoRevision)
	// content = versionPattern.ReplaceAllString(content, `${1}`+version)

	return nil, nil
}

func updateSignatureFile(buildAssets *buildassets.BuildAssets, specFileContent []byte) ([]byte, error) {

	return nil, nil
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

	for i, registration := range cgManifest.Registrations {
		if registration.Component.Other.Name == "golang" {
			// Update the version and downloadUrl for the "golang" component
			cgManifest.Registrations[i].Component.Other.Version = buildAssets.GoVersion().MajorMinorPatch()
			cgManifest.Registrations[i].Component.Other.DownloadURL = fmt.Sprintf(
				"https://github.com/microsoft/go/releases/download/%s/%s",
				buildAssets.GoVersion().Full(),
				path.Base(buildAssets.GoSrcURL),
			)

			break // Exit the loop after finding and updating the "golang" component
		}
	}

	// Serialize the updated cgManifest back to JSON
	updatedCgManifestContent, err := json.MarshalIndent(cgManifest, "", "  ") // Use indentation for readability
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated cgmanifest.json: %w", err)
	}

	return updatedCgManifestContent, nil
}
