// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"errors"
	"flag"
	"path/filepath"
	"slices"
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
		Env: &dockerversions.ArchEnv{
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
						Env:       &dockerversions.ArchEnv{},
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
		if !errors.Is(err, ErrNoMajorMinorUpgradeMatch) {
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

func TestUpdateVersions_Preferred(t *testing.T) {
	tests := []struct {
		name     string
		assets   buildassets.BuildAssets
		versions dockerversions.Versions
		want     []string
	}{
		{
			name:   "new stable release",
			assets: buildassets.BuildAssets{Version: "1.28.0-1"},
			versions: dockerversions.Versions{
				"1.26": {Version: "1.26.10", PreferredMinor: true, PreferredMajor: true},
				"1.27": {Version: "1.27.3"},
			},
			want: []string{"1.27"},
		},
		{
			name:   "older patch update does not move preference backward",
			assets: buildassets.BuildAssets{Version: "1.26.11-1"},
			versions: dockerversions.Versions{
				"1.25": {Version: "1.25.10"},
				"1.26": {Version: "1.26.10"},
				"1.27": {Version: "1.27.3", PreferredMinor: true, PreferredMajor: true},
				"1.28": {Version: "1.28.0"},
			},
			want: []string{"1.27"},
		},
		{
			name:   "repair existing preferences",
			assets: buildassets.BuildAssets{Version: "1.28.1-1"},
			versions: dockerversions.Versions{
				"1.26": {Version: "1.26.10", PreferredMajor: true},
				"1.27": {Version: "1.27.3"},
				"1.28": {Version: "1.28.0", PreferredMinor: true},
			},
			want: []string{"1.27"},
		},
		{
			name:   "numeric ordering",
			assets: buildassets.BuildAssets{Version: "1.11.1-1"},
			versions: dockerversions.Versions{
				"1.9":  {Version: "1.9.7", PreferredMinor: true, PreferredMajor: true},
				"1.10": {Version: "1.10.3"},
				"1.11": {Version: "1.11.0"},
			},
			want: []string{"1.10"},
		},
		{
			name:   "new beta does not advance preference",
			assets: buildassets.BuildAssets{Version: "1.28beta1-1"},
			versions: dockerversions.Versions{
				"1.26": {Version: "1.26.10", PreferredMinor: true, PreferredMajor: true},
				"1.27": {Version: "1.27.3"},
			},
			want: []string{"1.26"},
		},
		{
			name:   "release candidate is not preferred",
			assets: buildassets.BuildAssets{Version: "1.28rc2-1"},
			versions: dockerversions.Versions{
				"1.26":    {Version: "1.26.10", PreferredMinor: true, PreferredMajor: true},
				"1.27":    {Version: "1.27.3"},
				"1.28-rc": {Version: "1.28rc1"},
			},
			want: []string{"1.26"},
		},
		{
			name:   "prerelease only",
			assets: buildassets.BuildAssets{Version: "1.28rc2-1"},
			versions: dockerversions.Versions{
				"1.28-rc": {Version: "1.28rc1"},
			},
		},
		{
			name:   "main does not advance preference",
			assets: buildassets.BuildAssets{Version: "1.27.4-1"},
			versions: dockerversions.Versions{
				"1.26": {Version: "1.26.10", PreferredMinor: true, PreferredMajor: true},
				"1.27": {Version: "1.27.3"},
				"main": {Version: "main"},
			},
			want: []string{"1.26"},
		},
		{
			name:   "single stable version preserves preference",
			assets: buildassets.BuildAssets{Version: "1.27.4-1"},
			versions: dockerversions.Versions{
				"1.27": {Version: "1.27.3", PreferredMinor: true, PreferredMajor: true},
			},
			want: []string{"1.27"},
		},
		{
			name:   "missing N-1 preserves preference",
			assets: buildassets.BuildAssets{Version: "1.28.1-1"},
			versions: dockerversions.Versions{
				"1.25": {Version: "1.25.10", PreferredMinor: true, PreferredMajor: true},
				"1.28": {Version: "1.28.0"},
			},
			want: []string{"1.25"},
		},
		{
			name:   "branch suffixes are independent",
			assets: buildassets.BuildAssets{Version: "1.28.0-1", Branch: "dev.boringcrypto.go1.28"},
			versions: dockerversions.Versions{
				"1.26":      {Version: "1.26.10", PreferredMinor: true, PreferredMajor: true},
				"1.27":      {Version: "1.27.3"},
				"1.26-fips": {Version: "1.26.10", BranchSuffix: "-fips", PreferredMinor: true, PreferredMajor: true},
				"1.27-fips": {Version: "1.27.3", BranchSuffix: "-fips"},
			},
			want: []string{"1.26", "1.27-fips"},
		},
		{
			name:   "tag prefixes are independent",
			assets: buildassets.BuildAssets{Version: "1.28.0-1"},
			versions: dockerversions.Versions{
				"1.26": {Version: "1.26.10", TagPrefix: "dev-", PreferredMinor: true, PreferredMajor: true},
				"1.27": {Version: "1.27.3"},
			},
			want: []string{"1.26", "1.27"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Repeating the update must not change the selected preference.
			for range 2 {
				if err := UpdateVersions(&tt.assets, tt.versions); err != nil {
					t.Fatal(err)
				}
				for key, v := range tt.versions {
					want := slices.Contains(tt.want, key)
					if v.PreferredMinor != want || v.PreferredMajor != want {
						t.Errorf("%s: preferredMinor=%v, preferredMajor=%v; want both %v",
							key, v.PreferredMinor, v.PreferredMajor, want)
					}
				}
			}
		})
	}
}

func TestUpdateVersions_PreferredManifestTags(t *testing.T) {
	arch := &dockerversions.Arch{
		Env:       &dockerversions.ArchEnv{GOOS: "linux", GOARCH: "amd64"},
		Supported: true,
	}
	versions := dockerversions.Versions{
		"1.26": {
			Version: "1.26.10", PreferredMinor: true, PreferredMajor: true,
			Variants: []string{"bookworm"}, PreferredVariant: "bookworm",
			Arches: map[string]*dockerversions.Arch{"amd64": arch},
		},
		"1.27": {
			Version:  "1.27.3",
			Variants: []string{"bookworm"}, PreferredVariant: "bookworm",
			Arches: map[string]*dockerversions.Arch{"amd64": arch},
		},
	}
	assets := &buildassets.BuildAssets{Version: "1.28.0-1", Arches: []*dockerversions.Arch{arch}}
	if err := UpdateVersions(assets, versions); err != nil {
		t.Fatal(err)
	}
	manifest := &dockermanifest.Manifest{Repos: []*dockermanifest.Repo{{}}}
	UpdateManifest(manifest, versions)
	if len(manifest.Repos[0].Images) != 3 {
		t.Fatalf("Expected all three releases to be retained, got %d images", len(manifest.Repos[0].Images))
	}
	for _, tag := range []string{"1", "latest", "1-bookworm", "bookworm"} {
		var owners []string
		for _, image := range manifest.Repos[0].Images {
			if _, ok := image.SharedTags[tag]; ok {
				owners = append(owners, image.ProductVersion)
			}
		}
		if diff := deep.Equal(owners, []string{"1.27"}); diff != nil {
			t.Errorf("Tag %q: %v", tag, diff)
		}
	}
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
			"linux-arm64 Mariner v8",
			args{"linux", "cbl-mariner2.0", "arm64", ""},
			want{"linux", "cbl-mariner2.0", "arm64", "v8"},
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
