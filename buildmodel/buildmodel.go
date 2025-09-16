// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildmodel contains utilities to read, write, and modify files that are related to the
// Microsoft build of Go.
package buildmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/buildmodel/dockermanifest"
	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/stringutil"
)

// fipsTagPrefixes is a list of prefixes that indicate a tag specifies an image
// wrapping another image for the purpose of modifying it to support FIPS.
var fipsTagPrefixes = []string{
	"fips-linux/",
	"fips/",
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
			for _, fipsPrefix := range fipsTagPrefixes {
				if after, ok := stringutil.CutPrefix(osVersion, fipsPrefix); ok {
					osVersion = after

					// Figure out the non-FIPS tag name so that we can wrap it.
					fipsWrapTag = joinTag(applyVersionAffixes(majorMinorPatchRevision), osVersion)
					// Replace applyVersionAffixes with a new func that preserves the existing behavior,
					// but also adds "-fips" to the end.
					oldApply := applyVersionAffixes
					applyVersionAffixes = func(version string) string {
						return joinTag(oldApply(version), "fips")
					}
					break
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
			if after, ok := strings.CutPrefix(osVersion, nanoserverPrefix); ok {
				windowsVersion := after
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
				// Skip src.
				if arch.Env == nil {
					continue
				}
				// Skip platforms that don't match the current variant. v.Arches is actually a list
				// of OS/ARCHes, not just architectures.
				if arch.Env.GOOS != os {
					continue
				}
				// Skip arm (arm32) on certain OSes. Excluding it here is better than excluding the
				// platform from the versions.json file: specializing versions.json to exclude a
				// platform requires a lot of duplication and the templates would generate
				// Dockerfiles in a different, less clear folder structure.
				if arch.Env.GOARCH == "arm" {
					// On Azure Linux/CBL-Mariner, the base image doesn't exist.
					if strings.HasPrefix(osVersion, "cbl-mariner") ||
						strings.HasPrefix(osVersion, "azurelinux") {

						continue
					}
				}

				if fipsWrapTag != "" {
					buildArgs = map[string]string{
						"FROM_TAG": joinTag(fipsWrapTag, arch.Env.GoImageArchKey()),
						"REPO":     repoVariable,
					}
				}

				p := makeOSArchPlatform(os, osVersion, arch.Env)
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
			Variables: map[string]any{},
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

// ErrNoMajorMinorUpgradeMatch indicates that while running UpdateVersions, the input assets file
// didn't match any major.minor versions and no update could be performed.
var ErrNoMajorMinorUpgradeMatch = errors.New("no match found in existing versions.json file")

// UpdateVersions takes a build asset file containing a list of build outputs and updates a
// versions.json model to consume the new build.
func UpdateVersions(assets *buildassets.BuildAssets, versions dockerversions.Versions) error {
	// First, try to update an existing major.minor version in the Docker versions file. If it
	// doesn't exist, try to find the previous version and create a new Docker versions file entry
	// based on that. If that doesn't exist either, fail.
	key := assets.GetDockerRepoVersionsKey()
	v, ok := versions[key]
	if !ok {
		// Make a copy of BuildAssets and decrement the minor version to find the previous key.
		prevKey, err := assets.GetPreviousMinorDockerRepoVersionsKey()
		if err != nil {
			return fmt.Errorf("unable to calculate previous version key to use as a basis for the new version: %w", err)
		}
		v, ok = versions[prevKey]
		if !ok {
			return fmt.Errorf(
				"checked current version %v and previous version %v, however: %w",
				key, prevKey, ErrNoMajorMinorUpgradeMatch)
		}
		// Create a new Docker versions file entry for the new branch/major.minor version. Copy the
		// data (such as the list of variants) from the previous version. Use JSON serialization to
		// do a deep copy and prevent accidentally modifying shared old data in the following code.
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("unable to clone/marshal previous Docker versions file entry: %w", err)
		}
		// Create fresh struct so json.Unmarshal doesn't reuse the slices/maps.
		v = new(dockerversions.MajorMinorVersion)
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("unable to clone/unmarshal previous Docker versions file entry: %w", err)
		}
		versions[key] = v
		// Never prefer the freshly generated new version. If we let these remain 'true', the Docker
		// tags would overlap with the previous version's tags, where it was also 'true'.
		v.PreferredMinor = false
		v.PreferredMajor = false
		// Clear out the arches. These are always version-specific.
		v.Arches = nil
	}

	vNew := goversion.New(assets.Version)

	if key == "main" {
		// We could call this "main.0.0-0", but that makes the Docker tag names complicated.
		// Stick with simple "main".
		v.Version = key
		v.Revision = ""
	} else {
		v.Version = vNew.MajorMinorPatch() + vNew.Prerelease
		v.Revision = vNew.Revision
	}

	// Look through the asset arches, find an arch in the versions file that matches each asset,
	// and update its info.
	for _, arch := range assets.Arches {
		// Skip src.
		if arch.Env == nil {
			continue
		}
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
		} else {
			// By default, enable builds for new architectures that show up.
			arch.Supported = true
		}
		if v.Arches == nil {
			v.Arches = make(map[string]*dockerversions.Arch, 1)
		}
		// Copy the asset data into the versions file whether it's a new arch or not.
		v.Arches[archKey] = arch
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
	for i := range s {
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
