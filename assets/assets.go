// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package assets

// ToolsetBuild describes the artifacts produced by a toolset build of the Microsoft build of Go.
type ToolsetBuild struct {
	// Branch is the Git branch that the build used. Only includes the last segment, e.g.
	// "release-branch.go1.27".
	Branch string `json:"branch"`

	// BuildID identifies the Azure DevOps pipeline build that produced the artifacts.
	BuildID string `json:"buildId"`

	// Version is the Go version and Microsoft revision, such as "1.24.1-2" or "main-1".
	Version string `json:"version"`

	// Arches contains the artifacts produced for this version. Each binary artifact has a target
	// operating system and architecture. A source archive has no Env. The field name matches the
	// versions.json format.
	Arches []*Arch `json:"arches"`

	// GoSrcURL is the download URL for the source archive used by the build. Arches can also
	// contain this source archive as an entry with no Env.
	GoSrcURL string `json:"goSrcURL"`

	// GoSrcSHA256 is the hexadecimal SHA-256 digest of the archive at GoSrcURL.
	GoSrcSHA256 string `json:"goSrcSHA256"`
}

// Arch describes one downloadable build artifact.
type Arch struct {
	// Env identifies the target environment for a binary artifact.
	// Env is nil for a source archive.
	Env *ArchEnv `json:"env,omitempty"`

	// SHA256 is the hexadecimal SHA-256 digest of the artifact at URL.
	SHA256 string `json:"sha256"`

	// Supported indicates that Docker tooling should generate an image for this artifact. The field
	// name matches the upstream Go image format.
	Supported bool `json:"supported,omitempty"`

	// URL is the download URL for the artifact.
	URL string `json:"url"`

	// SHA256ChecksumURL is the download URL for a checksum file accepted by "sha256sum -c". An
	// empty value indicates URL with ".sha256" appended.
	SHA256ChecksumURL string `json:"sha256ChecksumUrl,omitempty"`

	// PGPSignatureURL is the download URL for the artifact's PGP signature. An empty value
	// indicates URL with ".sig" appended.
	PGPSignatureURL string `json:"pgpSignatureUrl,omitempty"`
}

// ArchEnv identifies the Go target environment for a binary artifact.
type ArchEnv struct {
	// GOARCH is the target architecture.
	GOARCH string `json:",omitempty"`

	// GOARM is the target ARM architecture version.
	GOARM string `json:",omitempty"`

	// GOOS is the target operating system.
	GOOS string `json:",omitempty"`
}
