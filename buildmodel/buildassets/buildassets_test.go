// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildassets

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/goldentest"
	"golang.org/x/tools/txtar"
)

func TestBuildResultsDirectoryInfo_GoldenCreateSummary(t *testing.T) {
	tests := []string{
		"1.17-build",
		"1.23dev-publish",
		"1.23dev-missing-publish",
	}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			dir := filepath.Join("testdata", tt)

			binDir, binExists := tempExtractTxtar(t, filepath.Join(dir, "bin.txtar"))
			srcDir, srcExists := tempExtractTxtar(t, filepath.Join(dir, "src.txtar"))
			destManifestPath := filepath.Join(dir, "msGo.output.manifest.json")

			// Set up state based on what exists in the testdata directory.
			b := BuildResultsDirectoryInfo{
				Branch:  "placeholder-branch",
				BuildID: "placeholder-build-id",
			}
			if binExists {
				b.ArtifactsDir = binDir
			}
			if srcExists {
				b.SourceDir = srcDir
			}
			if _, err := os.Stat(destManifestPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					b.DestinationURL = "https://example.org"
				} else {
					t.Fatal(err)
				}
			} else {
				b.DestinationManifest = destManifestPath
			}

			got, err := b.CreateSummary()
			var resultData []byte
			if err == nil {
				resultData, err = json.MarshalIndent(got, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				resultData = []byte(err.Error())
			}

			goldentest.Check(t, "result.golden.txt", string(resultData))
		})
	}
}

func Test_getVersion(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantVersion string
	}{
		{"Ordinary", "go1.17.3", "go1.17.3"},
		{"TrailingNewline", "go1.17.3\n", "go1.17.3"},
		{"MultilineFile", "go1.17.3\nMore information", "go1.17.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := path.Join(t.TempDir(), "VERSION")
			err := os.WriteFile(filePath, []byte(tt.text), os.ModePerm)
			if err != nil {
				t.Fatal(err)
			}

			gotVersion, err := getVersion(filePath, "default")
			if err != nil {
				t.Errorf("getVersion() error is not wanted: %v", err)
				return
			}
			if gotVersion != tt.wantVersion {
				t.Errorf("getVersion() = %v, want %v", gotVersion, tt.wantVersion)
			}
		})
	}
}

func tempExtractTxtar(t *testing.T, path string) (outDir string, ok bool) {
	t.Helper()

	tar, err := txtar.ParseFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		t.Fatal(err)
	}

	td := t.TempDir()
	for _, file := range tar.Files {
		if err := os.WriteFile(filepath.Join(td, file.Name), file.Data, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return td, true
}
