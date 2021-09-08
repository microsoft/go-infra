// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package buildmodel

import (
	"errors"
	"testing"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
)

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
			gotMajor, gotMinor, gotPatch, gotRevision := parseVersion(tt.version)
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
