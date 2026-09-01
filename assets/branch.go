// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package assets

// Branch describes the support status and available artifacts for one branch of the Microsoft
// build of Go.
type Branch struct {
	// Version is the Go major and minor version for the branch, such as "1.20".
	Version string `json:"version,omitempty"`

	// Stable indicates that the branch contains a stable release.
	Stable bool `json:"stable,omitempty"`

	// LatestStable indicates that the branch contains the most recent stable release.
	LatestStable bool `json:"latestStable,omitempty"`

	// PreviousStable indicates that the branch contains the stable release immediately before the
	// latest one.
	PreviousStable bool `json:"previousStable,omitempty"`

	// Files contains the links for the latest artifacts available from the branch.
	Files []*LatestLink `json:"files,omitempty"`
}

// ArtifactKind identifies the type of artifact referenced by a LatestLink.
type ArtifactKind string

const (
	// Archive identifies a ".zip" or ".tar.gz" toolset archive for a specific platform.
	Archive ArtifactKind = "archive"

	// Installer identifies a platform-specific installer. The project doesn't currently produce any
	// installers.
	Installer ArtifactKind = "installer"

	// Source identifies a ".tar.gz" source archive.
	Source ArtifactKind = "source"

	// Manifest identifies a JSON file that contains a [ToolsetBuild].
	Manifest ArtifactKind = "manifest"
)

// LatestLink describes the stable URLs for the latest version of one artifact on a branch.
//
// During a release, aka.ms links can change between downloads, so a checksum or signature
// downloaded after the artifact can refer to a different build. To avoid this race, download the
// [Manifest] artifact first and use the URLs from the resulting [ToolsetBuild].
type LatestLink struct {
	// Filename is the name of the artifact.
	Filename string `json:"filename"`

	// OS is the target operating system, if the artifact is platform-specific.
	OS string `json:"os"`

	// Arch is the target architecture, if the artifact is platform-specific.
	Arch string `json:"arch"`

	// Version is the full Go version available through URL.
	Version string `json:"version"`

	// Kind identifies the type of artifact.
	Kind ArtifactKind `json:"kind"`

	// URL is the aka.ms URL for the latest patch version of the artifact.
	URL string `json:"url,omitempty"`

	// ChecksumURL is the aka.ms URL for the artifact's checksum file. ChecksumURL is empty if the
	// artifact has no checksum file.
	ChecksumURL string `json:"checksumURL,omitempty"`

	// SignatureURL is the aka.ms URL for the artifact's signature file. SignatureURL is empty if
	// the artifact has no signature file.
	SignatureURL string `json:"signatureURL,omitempty"`
}
