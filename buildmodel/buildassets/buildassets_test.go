// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildassets

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
)

func TestBuildResultsDirectoryInfo_CreateSummary(t *testing.T) {
	b := BuildResultsDirectoryInfo{
		SourceDir:      "testdata/example-1.17-build/src",
		ArtifactsDir:   "testdata/example-1.17-build/assets",
		DestinationURL: "https://example.org",
		Branch:         "release-branch.go1.17",
		BuildID:        "placeholder-build-id",
	}
	want := &BuildAssets{
		Branch:  b.Branch,
		BuildID: b.BuildID,
		Version: "1.17.2-1",
		Arches: []*dockerversions.Arch{
			{
				Env: dockerversions.ArchEnv{
					GOARCH: "amd64",
					GOOS:   "linux",
				},
				SHA256:    strings.Repeat("0", 64),
				Supported: false,
				URL:       fmt.Sprintf("%v/go.linux-amd64.tar.gz", b.DestinationURL),
			},
		},
	}

	got, err := b.CreateSummary()
	if err != nil {
		t.Errorf("CreateSummary() error is not wanted: %v", err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CreateSummary() got = %v, want %v", got, want)
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
