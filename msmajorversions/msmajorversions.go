// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package msmajorversions describes the support status of specific versions and
// platforms of the Microsoft build of Go.
package msmajorversions

import (
	_ "embed"
	"fmt"
	"go/version"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/assets"
	"github.com/microsoft/go-infra/goversion"
)

// Previous is the stable branch immediately before Current.
var Previous = previousStable()

// Current is the latest stable branch of the Microsoft build of Go.
var Current = latestStable()

// Next describes the planned major release immediately after Current. It has
// not been published or promoted.
//
// Some release tools check to see if we're in the process of producing Next and
// behave differently in that case.
//
// The information in Next (specifically the list of supported platforms) may be
// updated as the development of the Next release progresses.
var Next = nextStable()

//go:embed CURRENT_MAJOR
var currentMajor string

func mustCurrentMajorInt() int {
	m, err := strconv.Atoi(strings.TrimSpace(currentMajor))
	if err != nil {
		panic(fmt.Sprintf("invalid current major version: %v", err))
	}
	return m
}

func previousStable() assets.Branch {
	m := mustCurrentMajorInt()
	m--
	branch := parseBranch(goversion.New(fmt.Sprintf("1.%v", m)))
	branch.Stable = true
	branch.PreviousStable = true
	return branch
}

func latestStable() assets.Branch {
	branch := parseBranch(goversion.New(fmt.Sprintf("1.%v", mustCurrentMajorInt())))
	branch.Stable = true
	branch.LatestStable = true
	return branch
}

func nextStable() assets.Branch {
	m := mustCurrentMajorInt()
	m++
	return parseBranch(goversion.New(fmt.Sprintf("1.%v", m)))
}

func parseBranch(v *goversion.GoVersion) assets.Branch {
	return assets.Branch{
		Version: v.MajorMinor(),
		Files:   files(v.MajorMinor()),
	}
}

var basePlatforms = []string{
	"src",
	"assets",
	"darwin-amd64",
	"darwin-arm64",
	"linux-amd64",
	"linux-arm64",
	"linux-armv6l",
	"windows-amd64",
}

var linuxLikeFiles = []fileType{
	{
		kind:      assets.Archive,
		extension: ".tar.gz",
		checksum:  true,
		signature: true,
	},
}

var windowsFiles = []fileType{
	{
		kind:      assets.Archive,
		extension: ".zip",
		checksum:  true,
	},
}

var sourceFiles = []fileType{
	{
		kind:      assets.Source,
		extension: ".tar.gz",
		checksum:  true,
		signature: true,
	},
}

var manifestFiles = []fileType{
	{
		kind:      assets.Manifest,
		extension: ".json",
	},
}

type fileType struct {
	kind      assets.ArtifactKind
	extension string
	checksum  bool
	signature bool
}

func files(goVersion string) []*assets.LatestLink {
	var result []*assets.LatestLink
	for _, platform := range platforms(goVersion) {
		os, arch, _ := strings.Cut(platform, "-")
		if platform == "src" || platform == "assets" {
			os = ""
		}
		for _, file := range fileTypes(platform) {
			result = append(result, file.latestLink(goVersion, platform, os, arch))
		}
	}
	return result
}

func platforms(goVersion string) []string {
	result := append([]string(nil), basePlatforms...)
	if version.Compare("go"+goVersion, "go1.26") >= 0 {
		result = append(result, "windows-arm64")
	}
	return result
}

func fileTypes(platform string) []fileType {
	switch {
	case strings.HasPrefix(platform, "darwin-"), strings.HasPrefix(platform, "linux-"):
		return linuxLikeFiles
	case strings.HasPrefix(platform, "windows-"):
		return windowsFiles
	case platform == "src":
		return sourceFiles
	case platform == "assets":
		return manifestFiles
	default:
		return nil
	}
}

func (f fileType) latestLink(goVersion, platform, os, arch string) *assets.LatestLink {
	filename := "go" + goVersion + "." + platform + f.extension
	link := &assets.LatestLink{
		Filename: filename,
		OS:       os,
		Arch:     arch,
		Version:  goVersion,
		Kind:     f.kind,
		URL:      latestBaseURL + filename,
	}
	if f.checksum {
		link.ChecksumURL = link.URL + ".sha256"
	}
	if f.signature {
		link.SignatureURL = link.URL + ".sig"
	}
	return link
}

const latestBaseURL = "https://aka.ms/golang/release/latest/"
