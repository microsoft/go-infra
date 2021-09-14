// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package dockerversions represents a Go Docker 'versions.json' file. It maps a 'major.minor' key
// to the details of that version.
package dockerversions

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
	Revision string `json:"revision"`

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
	GOOS   string
}
