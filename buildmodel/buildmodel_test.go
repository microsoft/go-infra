// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"errors"
	"flag"
	"path/filepath"
	"testing"

	"github.com/go-test/deep"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
)

var update = flag.Bool("update", false, "Update the golden files instead of failing.")

func TestBuildAssets_UpdateVersions(t *testing.T) {
	newArch := &dockerversions.Arch{
		Env: dockerversions.ArchEnv{
			GOARCH: "amd64",
			GOOS:   "linux",
		},
		SHA256: "abcdef123",
		URL:    "example.org",
	}
	a := &buildassets.BuildAssets{
		Version: "1.42",
		Arches:  []*dockerversions.Arch{newArch},
	}

	t.Run("Update existing", func(t *testing.T) {
		v := dockerversions.Versions{
			"1.42": {
				Version:  "1.42",
				Revision: "",
				Arches: map[string]*dockerversions.Arch{
					"amd64": {
						Env:       dockerversions.ArchEnv{},
						SHA256:    "old-sha",
						URL:       "old-url",
						Supported: true,
					},
				},
			},
		}
		if err := UpdateVersions(a, v); err != nil {
			t.Fatal(err)
		}

		gotArch := v["1.42"].Arches["amd64"]
		if gotArch.URL != newArch.URL || gotArch.SHA256 != newArch.SHA256 {
			t.Errorf("Old arch was not replaced by new arch.")
		}
		if gotArch.Supported != true {
			t.Errorf("Supported flag not correctly copied from old arch to new arch.")
		}
	})

	t.Run("Reject mismatched major.minor", func(t *testing.T) {
		v := dockerversions.Versions{
			// This is not 1.42, so update should fail to find a match.
			"1.48": {
				Version:  "1.48.15",
				Revision: "5",
				Arches:   nil,
			},
		}
		err := UpdateVersions(a, v)
		if !errors.Is(err, NoMajorMinorUpgradeMatchError) {
			t.Fatalf("Failed to reject the update with expected error result.")
		}
	})
}

func Test_makeOsArchPlatform(t *testing.T) {
	type args struct {
		os        string
		osVersion string
		goArch    string
		goARM     string
	}
	type want struct {
		os, osVersion, arch, variant string
	}
	tests := []struct {
		name string
		args args
		want want
	}{
		{
			"linux-amd64",
			args{"linux", "", "amd64", ""},
			want{"linux", "", "amd64", ""},
		},
		{
			"linux-arm64 default v8",
			args{"linux", "", "arm64", ""},
			want{"linux", "", "arm64", "v8"},
		},
		{
			"linux-arm v7 passthrough",
			args{"linux", "", "arm", "7"},
			want{"linux", "", "arm", "v7"},
		},
		{
			"linux-arm no version if Mariner",
			args{"linux", "cbl-mariner1.0", "arm", "7"},
			want{"linux", "cbl-mariner1.0", "arm", ""},
		},
		{
			"linux-arm64 no version if Mariner",
			args{"linux", "cbl-mariner1.0", "arm64", ""},
			want{"linux", "cbl-mariner1.0", "arm64", ""},
		},
		{
			"linux-arm64 no version if Mariner 2.0",
			args{"linux", "cbl-mariner2.0", "arm64", ""},
			want{"linux", "cbl-mariner2.0", "arm64", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &dockerversions.ArchEnv{
				GOARCH: tt.args.goArch,
				GOARM:  tt.args.goARM,
			}
			w := &dockermanifest.Platform{
				OS:           tt.want.os,
				OSVersion:    tt.want.osVersion,
				Architecture: tt.want.arch,
				Variant:      tt.want.variant,
			}
			got := makeOSArchPlatform(tt.args.os, tt.args.osVersion, a)
			if diff := deep.Equal(got, w); diff != nil {
				for _, d := range diff {
					t.Error(d)
				}
			}
		})
	}
}

func TestUpdateManifest(t *testing.T) {
	assetDir := filepath.Join("testdata", "UpdateManifest")
	var versions dockerversions.Versions
	var manifest dockermanifest.Manifest

	if err := ReadJSONFile(filepath.Join(assetDir, "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}
	if err := ReadJSONFile(filepath.Join(assetDir, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}

	UpdateManifest(&manifest, versions)

	checkGoldenJSON(t, filepath.Join(assetDir, "updatedManifest.golden.json"), manifest)
}

func TestUpdateVersions(t *testing.T) {
	assetDir := filepath.Join("testdata", "UpdateVersions")
	var buildAssetJSON buildassets.BuildAssets
	var versions dockerversions.Versions

	if err := ReadJSONFile(filepath.Join(assetDir, "assets.json"), &buildAssetJSON); err != nil {
		t.Fatal(err)
	}
	if err := ReadJSONFile(filepath.Join(assetDir, "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}

	if err := UpdateVersions(&buildAssetJSON, versions); err != nil {
		t.Fatal(err)
	}

	checkGoldenJSON(t, filepath.Join(assetDir, "updatedVersions.golden.json"), versions)
}

func checkGoldenJSON[T any](t *testing.T, goldenPath string, actual T) {
	if *update {
		if err := WriteJSONFile(goldenPath, actual); err != nil {
			t.Fatal(err)
		}
		return
	}

	// encoding/json uses reflection on a pointer to determine how to deserialize the file. Use
	// generics to create a pointer to the given T so the type matches for deep.Equal.
	var want T
	if err := ReadJSONFile(goldenPath, &want); err != nil {
		t.Fatal(err)
	}

	if diff := deep.Equal(actual, want); diff != nil {
		for _, d := range diff {
			t.Error(d)
		}
		t.Error("Actual result didn't match golden file. Run 'go test ./buildmodel -update' to update golden file.")
	}
}
