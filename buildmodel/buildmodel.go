// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildmodel contains utilities to read, write, and modify files that are related to the
// Microsoft build of Go.
package buildmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/goversion"
)

// ReadJSONFile reads one JSON value from the specified file.
func ReadJSONFile(path string, i interface{}) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("unable to open JSON file %v for reading: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	d := json.NewDecoder(f)
	if err := d.Decode(i); err != nil {
		return fmt.Errorf("unable to decode JSON file %v: %w", path, err)
	}
	return nil
}

// WriteJSONFile writes one specified value to a file as indented JSON with a trailing newline.
func WriteJSONFile(path string, i interface{}) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to open JSON file %v for writing: %w", path, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	d := json.NewEncoder(f)
	d.SetIndent("", "  ")
	if err := d.Encode(i); err != nil {
		return fmt.Errorf("unable to encode model into JSON file %v: %w", path, err)
	}
	return nil
}

// UpdateManifest takes a 'versions.json' model and updates a build manifest to make it build and
// tag all versions specified. Slices in the generated model are sorted, for diff stability. Map
// stability is handled by the Go JSON library when the model is serialized.
func UpdateManifest(manifest *dockermanifest.Manifest, versions dockerversions.Versions) {
	sortedMajorMinorKeys := make([]string, 0, len(versions))
	for key := range versions {
		sortedMajorMinorKeys = append(sortedMajorMinorKeys, key)
	}
	sort.Strings(sortedMajorMinorKeys)

	var images []*dockermanifest.Image

	for _, key := range sortedMajorMinorKeys {
		v := versions[key]
		// Remove branch suffix from the key to find the version part.
		majorMinor := strings.TrimSuffix(key, v.BranchSuffix)

		applyVersionAffixes := func(version string) string {
			return v.TagPrefix + version + v.BranchSuffix
		}

		// The key always contains a major.minor version. Split out the major part.
		major := goversion.New(majorMinor).Major

		majorMinorPatchRevision := v.Version + "-" + v.Revision

		for _, variant := range v.Variants {
			os := "linux"
			osVersion := variant
			if strings.HasPrefix(variant, "windows/") {
				os = "windows"
				osVersion = strings.TrimPrefix(variant, "windows/")
			}

			// If the versions.json doesn't specify a revision, default to "1". (1 is the
			// default/initial revision for Deb/RPM packages, and we may as well follow that.)
			if v.Revision == "" {
				v.Revision = "1"
			}

			// The main tag that is shared by all architectures.
			mainSharedTagVersion := applyVersionAffixes(majorMinorPatchRevision) + "-" + osVersion

			tagVersions := []string{
				mainSharedTagVersion,
				// Revisionless tag.
				applyVersionAffixes(v.Version) + "-" + osVersion,
				// We only maintain one patch version, so it's always preferred. Add major.minor tag.
				applyVersionAffixes(majorMinor) + "-" + osVersion,
			}

			// If this is a preferred major.minor version, create major-only tag.
			if v.PreferredMinor {
				tagVersions = append(tagVersions, applyVersionAffixes(major)+"-"+osVersion)
			}
			// If this is the preferred major version, create versionless tag.
			if v.PreferredMajor {
				tagVersions = append(tagVersions, applyVersionAffixes("")+osVersion)
			}

			// If this is the preferred variant, create tags without the variant (OS) part.
			if v.PreferredVariant == variant {
				tagVersions = append(tagVersions, applyVersionAffixes(majorMinorPatchRevision))
				tagVersions = append(tagVersions, applyVersionAffixes(v.Version))
				tagVersions = append(tagVersions, applyVersionAffixes(majorMinor))

				if v.PreferredMinor {
					tagVersions = append(tagVersions, applyVersionAffixes(major))
				}
				if v.PreferredMajor {
					tagVersions = append(tagVersions, applyVersionAffixes("latest"))
				}
			}

			sharedTags := make(map[string]dockermanifest.Tag, len(tagVersions))
			for _, tag := range tagVersions {
				sharedTags[tag] = dockermanifest.Tag{}
			}

			// Normally, no build args are necessary and this is nil in the output model.
			var buildArgs map[string]string

			// The nanoserver Dockerfile requires a build arg to connect it properly to its
			// dependency, windowsservercore. The version (1809, ltsc2022, ...) needs to match,
			// because CI splits up platform builds onto independent machines based on Windows
			// version, and the nanoserver image build needs to access the windowsservercore image.
			nanoserverPrefix := "nanoserver-"
			if strings.HasPrefix(osVersion, nanoserverPrefix) {
				windowsVersion := strings.TrimPrefix(osVersion, nanoserverPrefix)
				buildArgs = map[string]string{
					// nanoserver doesn't have good download capability, so it copies the Go install
					// from the windowsservercore image.
					"DOWNLOADER_TAG": applyVersionAffixes(majorMinorPatchRevision) + "-windowsservercore-" + windowsVersion + "-amd64",
					// The nanoserver Dockerfile needs to know what repository we're building for so
					// it can figure out the windowsservercore tag's full name.
					"REPO": "$(Repo:golang)",
				}
			}

			images = append(images, &dockermanifest.Image{
				ProductVersion: majorMinor,
				SharedTags:     sharedTags,
				Platforms: []*dockermanifest.Platform{
					{
						Dockerfile: "src/microsoft/" + key + "/" + variant,
						OS:         os,
						OSVersion:  osVersion,

						BuildArgs: buildArgs,

						Tags: map[string]dockermanifest.Tag{
							// We only build amd64 at the moment. The way to implement other
							// architectures in the future is to add more Platform entries.
							mainSharedTagVersion + "-amd64": {},
						},
					},
				},
			})
		}
	}

	// If no existing manifest or list of repos was provided, set up default values.
	if manifest == nil {
		manifest = &dockermanifest.Manifest{
			Readme:    "README.md",
			Registry:  "mcr.microsoft.com",
			Variables: map[string]interface{}{},
			Includes:  []string{},
		}
	}

	if len(manifest.Repos) == 0 {
		manifest.Repos = []*dockermanifest.Repo{
			{
				ID:   "golang",
				Name: "oss/go/microsoft/golang/alpha",
			},
		}
	}

	// Always update the 0th repo. Only one repo per branch is supported by auto-update.
	manifest.Repos[0].Images = images
}

// NoMajorMinorUpgradeMatchError indicates that while running UpdateVersions, the input assets file
// didn't match any major.minor versions and no update could be performed.
var NoMajorMinorUpgradeMatchError = errors.New("no match found in existing versions.json file")

// UpdateVersions takes a build asset file containing a list of build outputs and updates a
// versions.json model to consume the new build.
func UpdateVersions(assets *buildassets.BuildAssets, versions dockerversions.Versions) error {
	key := assets.GetDockerRepoVersionsKey()
	if v, ok := versions[key]; ok {
		vNew := goversion.New(assets.Version)

		v.Version = vNew.MajorMinorPatch()
		v.Revision = vNew.Revision

		// Look through the asset arches, find an arch in the versions file that matches each asset,
		// and update its info.
		for _, arch := range assets.Arches {
			// The versions file has a map of "GOOS-GOARCH" keys, but the key omits "linux-" if
			// included. This is upstream behavior we are conforming to.
			archKey := arch.Env.GOOS + "-"
			if archKey == "linux-" {
				archKey = ""
			}
			archKey += arch.Env.GOARCH

			if match, ok := v.Arches[archKey]; ok {
				// Copy over the previous value of keys that aren't specific to an asset, but
				// actually indicate the state of the Dockerfile. All other values come from the new
				// asset's data.
				arch.Supported = match.Supported
			}
			// Copy the asset data into the versions file whether it's a new arch or not.
			v.Arches[archKey] = arch
		}
	} else {
		return fmt.Errorf("%v: %w", key, NoMajorMinorUpgradeMatchError)
	}
	return nil
}
