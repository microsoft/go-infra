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

	specFilepPath := filepath.Join("testdata", "update-azure-linux", "golang.spec")
	specFile, err := os.ReadFile(specFilepPath)
	if err != nil {
		t.Fatalf("Error reading spec file from path %s, error is:%s", specFilepPath, err)
	}

	specFileContent := string(specFile)
	extractedGoFileVersion, err := extractGoArchiveNameFromSpecFile(specFileContent)
	if err != nil {
		t.Fatalf("Error extracting go archive name from spec file : %s", err)
	}

	if extractedGoFileVersion != "go1.22.4-20240604.2.src.tar.gz" {
		t.Fatalf("Expected extracted Go file version is not same as actual filename. Expected %s, returned %s", extractedGoFileVersion, "go1.22.4-20240604.2.src.tar.gz")
	}

	updatedSpecFile, err := updateSpecFile(assets, specFileContent)
	if err != nil {
		t.Fatalf("Error updating Go revision in spec file : %s", err)
	}

	goldentest.Check(
		t, "TestUpdateSpecFileContent ",
		filepath.Join("testdata", "update-azure-linux", "updated_golang.golden.spec"),
		updatedSpecFile)
}

func TestUpdateSignaturesFileContent(t *testing.T) {
	assets, err := loadBuildAssets(assetsJsonPath)
	if err != nil {
		t.Fatal(err)
	}

	signaturesFilePath := filepath.Join("testdata", "update-azure-linux", "signatures.json")
	signaturesFile, err := os.ReadFile(signaturesFilePath)
	if err != nil {
		t.Fatalf("Error reading spec file from path %s, error is:%s", signaturesFilePath, err)
	}

	updatedSignatureFile, err := updateSignatureFile(signaturesFile, "go1.22.4-20240604.2.src.tar.gz", path.Base(assets.GoSrcURL), assets.GoSrcSHA256)
	if err != nil {
		t.Errorf("Error updating CG Manifest file : %s", err)
	}

	goldentest.Check(
		t, "TestUpdateCGManifestFileContent ",
		filepath.Join("testdata", "update-azure-linux", "updated_signatures.golden.json"),
		string(updatedSignatureFile))
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

	goldentest.Check(
		t, "TestUpdateCGManifestFileContent ",
		filepath.Join("testdata", "update-azure-linux", "updated_cgmanifest.golden.json"),
		string(updatedCgManifestFile))
}
