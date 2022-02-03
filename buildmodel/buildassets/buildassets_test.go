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

func TestBuildAssets_parseVersion(t *testing.T) {
	tests := []struct {
		name                          string
		version                       string
		major, minor, patch, revision string
	}{
		{
			"Full version",
			"1.2.3-4",
			"1", "2", "3", "4",
		},
		{
			"Major only",
			"1",
			"1", "0", "0", "0",
		},
		{
			"Major.minor",
			"1.42",
			"1", "42", "0", "0",
		},
		{
			"Major.minor-revision",
			"1.42-6",
			"1", "42", "0", "6",
		},
		{
			"Too many dotted parts",
			"1.2.3.4.5.6",
			"1", "2", "3", "0",
		},
		{
			"Too many dashed parts",
			"1-2-3-4",
			"1", "0", "0", "2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMajor, gotMinor, gotPatch, gotRevision := ParseVersion(tt.version)
			if gotMajor != tt.major {
				t.Errorf("parseVersion() gotMajor = %v, major %v", gotMajor, tt.major)
			}
			if gotMinor != tt.minor {
				t.Errorf("parseVersion() gotMinor = %v, minor %v", gotMinor, tt.minor)
			}
			if gotPatch != tt.patch {
				t.Errorf("parseVersion() gotPatch = %v, patch %v", gotPatch, tt.patch)
			}
			if gotRevision != tt.revision {
				t.Errorf("parseVersion() gotRevision = %v, revision %v", gotRevision, tt.revision)
			}
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
