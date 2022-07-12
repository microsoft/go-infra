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
	"github.com/microsoft/go-infra/stringutil"
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

	t.Run("Generate new entry for minor+1", func(t *testing.T) {
		v := dockerversions.Versions{
			// This is one under 1.42, so updating to include 1.42 should detect 1.41 and copy some
			// data over.
			"1.41": {
				Version:          "1.41.15",
				Revision:         "5",
				Arches:           nil,
				PreferredVariant: "buster",
				Variants:         []string{"buster", "bullseye"},
			},
		}
		if err := UpdateVersions(a, v); err != nil {
			t.Fatal(err)
		}
		original := v["1.41"]
		got := v["1.42"]
		// Check that copies happened.
		if original.PreferredVariant != got.PreferredVariant {
			t.Errorf("Got variant %v, not the same as the copy source %v", got.PreferredVariant, original.PreferredVariant)
		}
		if diff := deep.Equal(got.Variants, original.Variants); diff != nil {
			t.Error(diff)
		}
		// Check that version comes from the build asset file and was not copied.
		if original.Version == got.Version {
			t.Errorf("Version: want != %v, got %v", original.Version, got.Version)
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

	if err := stringutil.ReadJSONFile(filepath.Join(assetDir, "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}
	if err := stringutil.ReadJSONFile(filepath.Join(assetDir, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}

	UpdateManifest(&manifest, versions)

	checkGoldenJSON(t, filepath.Join(assetDir, "updatedManifest.golden.json"), manifest)
}

func TestUpdateVersions(t *testing.T) {
	assetDir := filepath.Join("testdata", "UpdateVersions")
	var buildAssetJSON buildassets.BuildAssets
	var versions dockerversions.Versions

	if err := stringutil.ReadJSONFile(filepath.Join(assetDir, "assets.json"), &buildAssetJSON); err != nil {
		t.Fatal(err)
	}
	if err := stringutil.ReadJSONFile(filepath.Join(assetDir, "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}

	if err := UpdateVersions(&buildAssetJSON, versions); err != nil {
		t.Fatal(err)
	}

	checkGoldenJSON(t, filepath.Join(assetDir, "updatedVersions.golden.json"), versions)
}

func checkGoldenJSON[T any](t *testing.T, goldenPath string, actual T) {
	if *update {
		if err := stringutil.WriteJSONFile(goldenPath, actual); err != nil {
			t.Fatal(err)
		}
		return
	}

	// encoding/json uses reflection on a pointer to determine how to deserialize the file. Use
	// generics to create a pointer to the given T so the type matches for deep.Equal.
	var want T
	if err := stringutil.ReadJSONFile(goldenPath, &want); err != nil {
		t.Fatal(err)
	}

	if diff := deep.Equal(actual, want); diff != nil {
		for _, d := range diff {
			t.Error(d)
		}
		t.Error("Actual result didn't match golden file. Run 'go test ./buildmodel -update' to update golden file.")
	}
}
