// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
)

var assetsJsonPath = filepath.Join("testdata", "update-azure-linux", "assets.json")

func TestUpdateSpecFileContent(t *testing.T) {
	assets, err := loadBuildAssets(assetsJsonPath)
	if err != nil {
		t.Fatal(err)
	}

	_ = assets

	specFilepPath := filepath.Join("testdata", "update-azure-linux", "golang.spec")
	specFile, err := os.ReadFile(specFilepPath)
	if err != nil {
		t.Fatalf("Error reading spec file from path %s, error is:%s", specFilepPath, err)
	}

	extractedGoFileVersion, err := extractGoArchiveNameFromSpecFile(string(specFile))
	if err != nil {
		t.Fatalf("Error extracting go archive name from spec file : %s", err)
	}

	if extractedGoFileVersion != "go1.22.4-20240604.2.src.tar.gz" {
		t.Fatalf("Expected extracted Go file version is not same as actual filename. Expected %s, returned %s", extractedGoFileVersion, "go1.22.4-20240604.2.src.tar.gz")
	}

	updatedspecFile, err := updateGoArchiveNameInSpecFile(string(specFile), path.Base(assets.GoSrcURL))

	goldentest.Check(
		t, "TestUpdateSpecFileContent ",
		filepath.Join("testdata", "update-azure-linux", "golang_updated.spec"),
		updatedspecFile)
}

func TestUpdateSignaturesFileContent(t *testing.T) {
	assets, err := loadBuildAssets(assetsJsonPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Error(assets.GoVersion().Revision)
	_ = assets
}

func TestUpdateCGManifestFileContent(t *testing.T) {
	assets, err := loadBuildAssets(assetsJsonPath)
	if err != nil {
		t.Fatal(err)
	}

	cgManifestFilePath := filepath.Join("testdata", "update-azure-linux", "cgmanifest.json")
	cgManifestFile, err := os.ReadFile(cgManifestFilePath)
	if err != nil {
		t.Fatalf("Error reading spec file from path %s, error is:%s", cgManifestFilePath, err)
	}

	updatedCgManifestFile, err := updateCGManifest(assets, cgManifestFile)
	if err != nil {
		t.Errorf("Error updating CG Manifest file : %s", err)
	}

	// append a new line for fully matching the file
	updatedCgManifestFile = append(updatedCgManifestFile, '\n')

	goldentest.Check(
		t, "TestUpdateCGManifestFileContent ",
		filepath.Join("testdata", "update-azure-linux", "cgmanifest_updated.json"),
		string(updatedCgManifestFile))
}
