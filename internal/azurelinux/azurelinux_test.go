// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azurelinux

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/goldentest"
	"github.com/microsoft/go-infra/goversion"
)

var assetsJsonPath = filepath.Join("testdata", "update-azure-linux", "assets.json")

func loadBuildAssets(t *testing.T) *buildassets.BuildAssets {
	t.Helper()
	data := readFile(t, assetsJsonPath)

	var assets buildassets.BuildAssets
	if err := json.Unmarshal(data, &assets); err != nil {
		t.Fatal(err)
	}
	return &assets
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAzLUpdateSpecFileContent(t *testing.T) {
	assets := loadBuildAssets(t)
	v := Version{
		Spec: readFile(t, filepath.Join("testdata", "update-azure-linux", "golang.spec")),
	}

	extractedGoFileVersion, err := v.parseGoArchiveName()
	if err != nil {
		t.Fatalf("error parsing Go archive name: %s", err)
	}

	if extractedGoFileVersion != "go1.22.4-20240604.2.src.tar.gz" {
		t.Fatalf("Expected extracted Go file version is not same as actual filename. Expected %s, returned %s", extractedGoFileVersion, "go1.22.4-20240604.2.src.tar.gz")
	}

	changelogTime, err := time.Parse("2006-01-02", "2024-08-12")
	if err != nil {
		t.Fatalf("Error parsing changelog time : %s", err)
	}

	if err := v.updateSpec(assets, changelogTime); err != nil {
		t.Fatalf("Error updating Go revision in spec file : %s", err)
	}

	goldentest.Check(t, "updated_golang.golden.spec", string(v.Spec))
}

func TestAzLUpdateSignaturesFileContent(t *testing.T) {
	assets := loadBuildAssets(t)
	v := Version{
		Signatures: readFile(t, filepath.Join("testdata", "update-azure-linux", "signatures.json")),
	}

	if err := v.updateSignatures("go1.22.4-20240604.2.src.tar.gz", path.Base(assets.GoSrcURL), assets.GoSrcSHA256); err != nil {
		t.Fatalf("error updating signatures in spec file: %s", err)
	}

	goldentest.Check(t, "updated_signatures.golden.json", string(v.Signatures))
}

func TestAzLUpdateCGManifestFileContent(t *testing.T) {
	assets := loadBuildAssets(t)
	rm := RepositoryModel{
		CGManifest: readFile(t, filepath.Join("testdata", "update-azure-linux", "cgmanifest.json")),
	}

	if err := rm.UpdateCGManifest(assets); err != nil {
		t.Fatalf("error updating CG Manifest file: %s", err)
	}

	goldentest.Check(t, "updated_cgmanifest.golden.json", string(rm.CGManifest))
}

func TestAzLUpdateSpecVersion(t *testing.T) {
	type args struct {
		newGoVersion string
		oldVersion   string
		oldRelease   int
	}
	tests := []struct {
		name        string
		args        args
		wantVersion string
		wantRelease string
	}{
		{
			"patch",
			args{"1.22.4-1", "1.22.3", 1},
			"1.22.4", "1",
		},
		{
			"patch-modified-package",
			args{"1.22.4-1", "1.22.3", 2},
			"1.22.4", "1",
		},
		{
			"patch-msft-go-release",
			args{"1.22.3-2", "1.22.3", 1},
			"1.22.3", "2",
		},
		{
			"patch-msft-go-release-modified",
			args{"1.22.3-2", "1.22.3", 4},
			"1.22.3", "5",
		},
		{
			"go-major",
			args{"1.23.0-1", "1.22.8", 1},
			"1.23.0", "1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assets := &buildassets.BuildAssets{
				Version: tt.args.newGoVersion,
			}
			gotVersion, gotRelease := updateSpecVersion(
				assets,
				goversion.New(tt.args.oldVersion),
				tt.args.oldRelease,
			)
			if gotVersion != tt.wantVersion {
				t.Errorf("updateSpecVersion() gotVersion = %v, want %v", gotVersion, tt.wantVersion)
			}
			if gotRelease != tt.wantRelease {
				t.Errorf("updateSpecVersion() gotRelease = %v, want %v", gotRelease, tt.wantRelease)
			}
		})
	}
}

func TestAzLPRBody(t *testing.T) {
	assets := loadBuildAssets(t)

	type args struct {
		latestMajor bool
		security    bool
		notify      string
		prNumber    int
	}
	tests := []struct {
		name string
		args args
	}{
		{"essential", args{true, false, "a-go-developer", 0}},
		{"non-latest", args{false, false, "a-go-developer", 0}},
		{"security", args{true, true, "a-go-developer", 0}},
		{"no-dev", args{true, false, "", 0}},
		{"pr-number", args{true, false, "a-go-developer", 1234}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GeneratePRTitleFromAssets(assets, tt.args.security)
			got += "\n\n---\n\n"
			got += GeneratePRDescription(assets, tt.args.latestMajor, tt.args.security, tt.args.notify, tt.args.prNumber)
			goldentest.Check(t, "pr-description-"+tt.name+".golden.md", got)
		})
	}
}
