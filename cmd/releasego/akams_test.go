// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/goldentest"
)

func Test_createLinkPairs(t *testing.T) {
	latestShortLinkPrefix = "testing/"

	blobStorageBuild := &buildassets.BuildAssets{
		Branch:  "release-branch.go1.18",
		BuildID: "123456",
		Version: "1.17.7-1",
		Arches: []*dockerversions.Arch{
			{
				Env: &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "amd64"},
				URL: "https://example.org/golang/build/1234.10/go.1234.10.linux-amd64.tar.gz",
			},
		},
		GoSrcURL: "https://example.org/golang/build/1234.10/go.1234.10.src.tar.gz",
	}

	releaseStudioBuild := &buildassets.BuildAssets{
		Branch:  "release-branch.go1.22",
		BuildID: "654321",
		Version: "1.22.3-1",
		Arches: []*dockerversions.Arch{
			{
				Env:               &dockerversions.ArchEnv{GOOS: "windows", GOARCH: "amd64"},
				URL:               "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/62f44/go1.23-90bcc55-20240626.1.windows-amd64.zip",
				SHA256ChecksumURL: "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/41bc5/go1.23-90bcc55-20240626.1.windows-amd64.zip.sha256",
			},
			{
				Env:               &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "amd64"},
				URL:               "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/333aa/go1.23-90bcc55-20240626.1.linux-amd64.tar.gz",
				SHA256ChecksumURL: "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/87654/go1.23-90bcc55-20240626.1.linux-amd64.tar.gz.sha256",
				PGPSignatureURL:   "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/12345/go1.23-90bcc55-20240626.1.linux-amd64.tar.gz.sig",
			},
			{
				URL:               "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/3b32d/go1.23-90bcc55-20240626.1.src.tar.gz",
				SHA256ChecksumURL: "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/66dda/go1.23-90bcc55-20240626.1.src.tar.gz.sha256",
				PGPSignatureURL:   "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/af253/go1.23-90bcc55-20240626.1.src.tar.gz.sig",
			},
		},
		GoSrcURL: "https://download.visualstudio.microsoft.com/download/pr/8330ba2b/3b32d/go1.23-90bcc55-20240626.1.src.tar.gz",
	}

	tests := []struct {
		name      string
		assets    *buildassets.BuildAssets
		assetsURL string
	}{
		{"blob", blobStorageBuild, ""},
		{"release studio", releaseStudioBuild, "https://download.visualstudio.microsoft.com/download/pr/1234-56-78-123456/3df7/assets.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := createLinkPairs(*tt.assets, tt.assetsURL)
			if err != nil {
				t.Fatal(err)
			}
			var gotTableBuffer bytes.Buffer
			if err := writeLinkPairTable(&gotTableBuffer, got); err != nil {
				t.Fatal(err)
			}
			goldentest.Check(t, "testdata/akams/"+t.Name()+".golden.txt", gotTableBuffer.String())
		})
	}
}

func Test_makeFloatingFilename(t *testing.T) {
	type args struct {
		filename     string
		floatVersion string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		// Actual 1.21.0 names.
		{"1.21", args{"go.20230808.6.linux-amd64.tar.gz", "1.21"}, "go1.21.linux-amd64.tar.gz", false},
		{"1.21 windows", args{"go.20230808.6.windows-amd64.zip", "1.21"}, "go1.21.windows-amd64.zip", false},
		{"1.21 src", args{"go.20230808.6.src.tar.gz", "1.21"}, "go1.21.src.tar.gz", false},
		{"1.21 assets", args{"assets.json", "1.21"}, "go1.21.assets.json", false},

		// The naming change in 1.22 means we need to handle cases like these.
		{"1.22", args{"go1.22.0-1234.5.linux-amd64.tar.gz", "1.22"}, "go1.22.linux-amd64.tar.gz", false},
		{"1.22 windows", args{"go1.22.0-1234.5.windows-amd64.zip", "1.22"}, "go1.22.windows-amd64.zip", false},

		// Some versions include commit hashes in them.
		{"missing build number", args{"go1.23-ad77cef-20240705.1.linux-amd64.tar.gz", "1.23.0"}, "go1.23.0.linux-amd64.tar.gz", false},
		{"dev build", args{"go1.23-ad77cef-20240705.1.windows-amd64.zip", "1.23.0"}, "go1.23.0.windows-amd64.zip", false},

		// Fail with bad inputs.
		{"nothing before platform", args{"windows-amd64.zip", "1.23.0"}, "", true},
		{"unrecognized extension", args{"go1.23.linux-amd64.gz", "1.23.0"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := makeFloatingFilename(tt.args.filename, tt.args.floatVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("makeFloatingFilename() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("makeFloatingFilename() got = %v, want %v", got, tt.want)
			}
		})
	}
}
