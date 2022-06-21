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
	"github.com/microsoft/go-infra/stringutil"
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

		// The key always contains a major.minor version. Split out the major part.
		major := goversion.New(majorMinor).Major

		majorMinorPatchRevision := joinTag(v.Version, v.Revision)

		for _, variant := range v.Variants {
			// Create applyVersionAffixes func. This may be overwritten for some variants (FIPS), so
			// recreate it each iteration.
			applyVersionAffixes := func(version string) string {
				return v.TagPrefix + version + v.BranchSuffix
			}

			os := "linux"
			osVersion := variant
			if after, ok := stringutil.CutPrefix(variant, "windows/"); ok {
				os = "windows"
				osVersion = after
			}

			// The non-FIPS Docker tag that this FIPS image wraps, or empty string if not.
			var fipsWrapTag string
			if after, ok := stringutil.CutPrefix(variant, "fips-linux/"); ok {
				osVersion = after

				// Figure out the non-FIPS tag name so that we can wrap it.
				fipsWrapTag = joinTag(applyVersionAffixes(majorMinorPatchRevision), osVersion)
				// Replace applyVersionAffixes with a new func that preserves the existing behavior,
				// but also adds "-fips" to the end.
				oldApply := applyVersionAffixes
				applyVersionAffixes = func(version string) string {
					return joinTag(oldApply(version), "fips")
				}
			}

			dockerfileDir := "src/microsoft/" + key + "/" + variant

			// The main tag that is shared by all architectures.
			mainSharedTagVersion := joinTag(applyVersionAffixes(majorMinorPatchRevision), osVersion)

			tagVersions := []string{
				mainSharedTagVersion,
				// Revisionless tag.
				joinTag(applyVersionAffixes(v.Version), osVersion),
				// We only maintain one patch version, so it's always preferred. Add major.minor tag.
				joinTag(applyVersionAffixes(majorMinor), osVersion),
			}

			// If this is a preferred major.minor version, create major-only tag.
			if v.PreferredMinor {
				tagVersions = append(tagVersions, joinTag(applyVersionAffixes(major), osVersion))
			}
			// If this is the preferred major version, create versionless tag.
			if v.PreferredMajor {
				tagVersions = append(tagVersions, joinTag(applyVersionAffixes(""), osVersion))
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
			// If buildArgs is specified to point at another image, it will also need this repo
			// variable (passed by .NET Docker) to figure out the other tag's full name.
			const repoVariable = "$(Repo:golang)"

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
					"DOWNLOADER_TAG": joinTag(applyVersionAffixes(majorMinorPatchRevision), "windowsservercore", windowsVersion, "amd64"),
					"REPO":           repoVariable,
				}
			}

			// Add one Platform for each OS/ARCH this variant supports.
			platforms := make([]*dockermanifest.Platform, 0, 3)
			for _, arch := range v.Arches {
				// Skip unsupported arches/platforms.
				if !arch.Supported {
					continue
				}
				// Skip platforms that don't match the current variant. v.Arches is actually a list
				// of OS/ARCHes, not just architectures.
				if arch.Env.GOOS != os {
					continue
				}
				// Skip arm (arm32) on CBL-Mariner 1.0. The base image doesn't exist. Excluding it
				// here is better than excluding the platform from the versions.json file:
				// specializing versions.json for CBL-Mariner requires a lot of duplication and the
				// templates would generate Dockerfiles in a different, less clear folder structure.
				if osVersion == "cbl-mariner1.0" && arch.Env.GOARCH == "arm" {
					continue
				}

				if fipsWrapTag != "" {
					buildArgs = map[string]string{
						"FROM_TAG": joinTag(fipsWrapTag, arch.Env.GoImageArchKey()),
						"REPO":     repoVariable,
					}
				}

				p := makeOSArchPlatform(os, osVersion, &arch.Env)
				p.BuildArgs = buildArgs
				p.Dockerfile = dockerfileDir
				p.Tags = map[string]dockermanifest.Tag{
					joinTag(mainSharedTagVersion, arch.Env.GoImageArchKey()): {},
				}
				platforms = append(platforms, p)
			}
			// Sort to make the ordering consistent between runs.
			sort.Slice(platforms, func(i, j int) bool {
				return platforms[i].Architecture+platforms[i].Variant <
					platforms[j].Architecture+platforms[j].Variant
			})

			productVersion := majorMinor
			// .NET Docker infra parses ProductVersion with .NET System.Version. If the image is for
			// a main branch build, provide a parseable but never-expected-to-be-seen version. We
			// could calculate "last release branch version"++, but we don't have good reason to do
			// this, it's not necessarily true, and it could be misleading.
			if productVersion == "main" {
				productVersion = "42.42"
			}

			images = append(images, &dockermanifest.Image{
				ProductVersion: productVersion,
				SharedTags:     sharedTags,
				Platforms:      platforms,
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

		if key == "main" {
			// We could call this "main.0.0-0", but that makes the Docker tag names complicated.
			// Stick with simple "main".
			v.Version = key
			v.Revision = ""
		} else {
			v.Version = vNew.MajorMinorPatch()
			v.Revision = vNew.Revision
		}

		// Look through the asset arches, find an arch in the versions file that matches each asset,
		// and update its info.
		for _, arch := range assets.Arches {
			// Special case for arm artifacts: change it to arm32v7. We produce arm32v6 builds of Go
			// but package them in arm/v7 (armhf) Docker images. The upstream Go official image repo
			// does this in their versions.json file: there are v6 and v7 Dockerfile arches that
			// both carry the v6 Go. We only care about arm/v7, so only include that one.
			if arch.Env.GOARCH == "arm" {
				arch.Env.GOARM = "7"
			}

			archKey := arch.Env.GoImageOSArchKey()
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

// makeOSArchPlatform creates a Docker manifest platform based on the given OS, OS version, and
// architecture information. This func processes the info to present it in the way .NET Docker's
// build infrastructure expects.
func makeOSArchPlatform(os, osVersion string, env *dockerversions.ArchEnv) *dockermanifest.Platform {
	// In .NET Docker, if GOARCH is not specific enough (like "arm" or "arm64"), we need
	// to specify more info: a version. .NET Docker infra calls this a "variant". This
	// is not the same as the Official Go Image "variant" (OS name/version).
	archVariant := env.GoImageArchVersionSuffix()
	// CBL-Mariner 1.0 and 2.0 don't specify an ARM arch variant (version) in the Docker manifest,
	// so we must omit it, too: .NET Docker infra checks they match.
	if strings.HasPrefix(osVersion, "cbl-mariner") {
		archVariant = ""
	}
	return &dockermanifest.Platform{
		Architecture: env.GOARCH,
		Variant:      archVariant,
		OS:           os,
		OSVersion:    osVersion,
	}
}

// joinTag joins the given strings with "-" to form a Docker tag (or partial tag). Empty strings are
// ignored and do not result in extra "-" characters. This is especially useful when it would be
// inconvenient for the caller to keep track of which elements might be an empty string.
func joinTag(s ...string) string {
	if len(s) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	for i := 0; i < len(s); i++ {
		if s[i] == "" {
			continue
		}
		if !first {
			b.WriteRune('-')
		}
		b.WriteString(s[i])
		first = false
	}
	return b.String()
}
