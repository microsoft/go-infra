// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package dockermanifest represents the .NET Docker tooling's 'manifest.json' file. This file
// guides the .NET Docker infra to build and tag the Go Docker images correctly.
//
// For more details about this model, see the dotnet/docker-tools C# implementation:
// https://github.com/dotnet/docker-tools/blob/main/src/Microsoft.DotNet.ImageBuilder/src/Models/Manifest/Manifest.cs
// This implementation in Go only contains the subset of syntax that we actually use in the Go
// Docker repository.
package dockermanifest

// Manifest is the root object of a 'manifest.json' file.
type Manifest struct {
	Readme    string                 `json:"readme"`
	Registry  string                 `json:"registry"`
	Variables map[string]interface{} `json:"variables"`
	Includes  []string               `json:"includes"`
	Repos     []*Repo                `json:"repos"`
}

// Repo is a Docker repository: the 'oss/go/microsoft/golang' part of a tag name.
type Repo struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Images []*Image `json:"images"`
}

// Image represents the build for a given version of Go. It contains the set of tags for this
// version, which may include multiple images for various OS/architectures.
type Image struct {
	ProductVersion string         `json:"productVersion"`
	SharedTags     map[string]Tag `json:"sharedTags"`
	Platforms      []*Platform    `json:"platforms"`
}

// Platform is one OS+arch combination, and it maps to a specific Dockerfile in the Git repo.
type Platform struct {
	BuildArgs map[string]string `json:"buildArgs,omitempty"`

	// Architecture	is the processor/os architecture the image should build for. This follows
	// .NET Docker tooling values, which in turn follows GOARCH:
	// https://go.dev/doc/install/source#environment
	Architecture string `json:"architecture,omitempty"`
	// Variant is a string used to further specify architecture. For example: "v8", for ARM.
	Variant string `json:"variant,omitempty"`

	Dockerfile string `json:"dockerfile"`
	OS         string `json:"os"`
	OSVersion  string `json:"osVersion"`
	// Tags is a map of tag names to Tag metadata.
	Tags map[string]Tag `json:"tags"`
}

// Tag is the metadata about a tag. Intentionally empty: we don't use any metadata yet.
type Tag struct{}
