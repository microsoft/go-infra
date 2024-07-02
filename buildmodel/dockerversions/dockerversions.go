// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package dockerversions represents a Go Docker 'versions.json' file. It maps a 'major.minor' key
// to the details of that version.
package dockerversions

import "github.com/microsoft/go-infra/goversion"

// Versions is the root of a 'versions.json' file.
//
// Note: this type is an alias of a map, so it's essentially a pointer. Use dockerversions.Versions,
// not *dockerversions.Versions. The 'versions.json' file is also used by upstream infrastructure,
// so this model is designed to be compatible with it.
type Versions map[string]*MajorMinorVersion

// MajorMinorVersion contains information about a major.minor version.
type MajorMinorVersion struct {
	// Arches is the list of architectures that should be built.
	Arches map[string]*Arch `json:"arches"`
	// Variants lists OS variants that should be built. It must be provided in dependency order.
	Variants []string `json:"variants"`
	// Version is the current major.minor.patch version of this major.minor version.
	Version string `json:"version"`

	// Revision extends the upstream model by adding the Microsoft revision of the Go version. The
	// Microsoft build might get new versions that aren't associated with an upstream version bump.
	Revision string `json:"revision,omitempty"`

	// PreferredMajor extends the upstream model by marking this major version as "preferred" over
	// other major versions. This is used when generating the manifest to create the "latest" tags.
	PreferredMajor bool `json:"preferredMajor,omitempty"`
	// PreferredMinor extends the upstream model by marking this minor version as "preferred" over
	// other minor versions. For example, if "1.42" is preferred, this would generate a "1" tag in
	// the manifest that people can use to pull "1.42" rather than "1.41".
	PreferredMinor bool `json:"preferredMinor,omitempty"`
	// PreferredVariant extends the upstream model and specifies the variant that should be
	// "preferred" in the tagging structure. For example, if buster is preferred over stretch, the
	// generated "1.16.6" tag will point at a buster image.
	PreferredVariant string `json:"preferredVariant,omitempty"`

	// TagPrefix extends the upstream model, specifying a prefix to include in every tag when
	// generating the 'manifest.json' file.
	//
	// This field is intended to be used for dev branches. When creating a dev branch, filter the
	// Versions entries down the ones that are necessary and add a TagPrefix to each one to
	// distinguish them from the main branch.
	TagPrefix string `json:"tagPrefix,omitempty"`

	// BranchSuffix	extends the upstream model by indicating a suffix on the Versions key that is
	// related to the branch this build came from, such as "-fips". This suffix must be removed
	// before interpreting the Versions key as a Go version.
	//
	// This field is intended to be used for stable branches that have forked from the release
	// branches in microsoft/go and are maintained in the main branch in microsoft/go-images. A
	// BranchSuffix allows 1.17 and 1.17-fips to have distinct Versions objects, for example, which
	// is necessary to have different arches and variants for each branch.
	BranchSuffix string `json:"branchSuffix,omitempty"`
}

// GoVersion returns the parsed Go version this MajorMinorVersion will build.
func (m *MajorMinorVersion) GoVersion() *goversion.GoVersion {
	return goversion.New(m.Version + "-" + m.Revision + m.BranchSuffix)
}

// Arch points at the publicly accessible artifacts for a specific OS/arch.
type Arch struct {
	Env       ArchEnv `json:"env"`
	SHA256    string  `json:"sha256"`
	Supported bool    `json:"supported,omitempty"`
	URL       string  `json:"url"`
}

type ArchEnv struct {
	GOARCH string
	GOARM  string `json:"GOARM,omitempty"`
	GOOS   string
}

// GoImageArchVersionSuffix is the string used in docker-library/golang and .NET Docker infrastructure to
// specify an arch's version. Normally, empty string. For ARM, something like "v7" or "v8".
func (a *ArchEnv) GoImageArchVersionSuffix() string {
	// If arch is arm/arm64, need a version suffix.
	if a.GOARCH == "arm" {
		// arm always has a GOARM version.
		return "v" + a.GOARM
	}
	if a.GOARCH == "arm64" {
		// arm64 is always v8. GOARM is no longer specified.
		return "v8"
	}
	return ""
}

// GoImageOSArchKey replicates upstream behavior, creating the string used by docker-library/golang
// to identify this OS/arch in its versions.json file. Linux is the default OS, so if it is the
// ArchEnv's OS, it isn't included in this string.
func (a *ArchEnv) GoImageOSArchKey() string {
	s := a.GoImageArchKey()
	if a.GOOS != "linux" {
		s = a.GOOS + "-" + s
	}
	return s
}

// GoImageArchKey replicates upstream behavior, creating the arch string used by
// docker-library/golang and .NET Docker to identify this arch in the versions.json and
// manifest.json files.
func (a *ArchEnv) GoImageArchKey() string {
	// Non-arm64 arm needs "32" included.
	if a.GOARCH == "arm" {
		return a.GOARCH + "32" + a.GoImageArchVersionSuffix()
	}
	return a.GOARCH + a.GoImageArchVersionSuffix()
}
