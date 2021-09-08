// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildassets represents a build asset JSON file that describes the output of a Go build.
// We use this file to update other repos (in particular Go Docker) to that build.
//
// This file's structure is controlled by our team: not .NET Docker, Go, or the official golang
// image team. So, we can choose to reuse parts of other files' schema to keep it simple.
package buildassets

import "github.com/microsoft/go-infra/buildmodel/dockerversions"

// BuildAssets is the root object of a build asset JSON file.
type BuildAssets struct {
	// Branch that produced this build. This is not used for auto-update.
	Branch string `json:"branch"`
	// BuildID is a link to the build that produced these assets. It is not used for auto-update.
	BuildID string `json:"buildId"`

	// Version of the build, as 'major.minor.patch-revision'.
	Version string `json:"version"`
	// Arches is the list of artifacts that was produced for this version, typically one per target
	// os/architecture. The name "Arches" is shared with the versions.json format.
	Arches []*dockerversions.Arch `json:"arches"`
}
