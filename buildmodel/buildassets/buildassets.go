// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildassets represents a build asset JSON file that describes the output of a Go build.
// We use this file to update other repos (in particular Go Docker) to that build.
//
// This file's structure is controlled by our team: not .NET Docker, Go, or the official golang
// image team. So, we can choose to reuse parts of other files' schema to keep it simple.
package buildassets

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
)

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

// Basic information about how the build output assets are formatted by Microsoft builds of Go. The
// archiving infra is stored in each release branch to make it local to the code it operates on and
// less likely to unintentionally break, so some of that information is duplicated here.
var archiveSuffixes = []string{".tar.gz", ".zip"}
var checksumSuffix = ".sha256"

// BuildResultsDirectoryInfo points to locations in the filesystem that contain a Go build from
// source, and includes extra information that helps make sense of the build results.
type BuildResultsDirectoryInfo struct {
	// SourceDir is the path to the source code that was built. This is checked for files that
	// indicate what version of Go was built.
	SourceDir string
	// ArtifactsDir is the path to the directory that contains the artifacts (.tar.gz, .zip,
	// .sha256) that were built.
	ArtifactsDir string
	// DestinationURL is the URL where the assets will be uploaded, if this is an internal build
	// that will be published somewhere. This lets us include the final URL in the build asset data
	// so auto-update can pick it up easily.
	DestinationURL string
	// Branch is the Git branch this build was built with. In many cases it can be determined with
	// Git commands, but this is not always possible (or reliable), so we pass it through as a
	// simple arg.
	Branch string
	// BuildID uniquely identifies the CI pipeline build that produced this result. This allows devs
	// to quickly trace back to the originating build if something goes wrong later on.
	BuildID string
}

// CreateSummary scans the paths/info from a BuildResultsDirectoryInfo to summarize the outputs of
// the build in a BuildAssets struct. The result can be used later to perform an auto-update.
func (b BuildResultsDirectoryInfo) CreateSummary() (*BuildAssets, error) {
	goVersion := getVersion(path.Join(b.SourceDir, "VERSION"), "main")
	goRevision := getVersion(path.Join(b.SourceDir, "MICROSOFT_REVISION"), "1")

	// Go version file content begins with "go", matching the tags, but we just want numbers.
	goVersion = strings.TrimPrefix(goVersion, "go")

	// Store the set of artifacts discovered in a map. This lets us easily associate a "go.tar.gz"
	// with its "go.tar.gz.sha256" file.
	archMap := make(map[string]*dockerversions.Arch)
	getOrCreateArch := func(name string) *dockerversions.Arch {
		if arch, ok := archMap[name]; ok {
			return arch
		}
		a := &dockerversions.Arch{}
		archMap[name] = a
		return a
	}

	if b.ArtifactsDir != "" {
		entries, err := os.ReadDir(b.ArtifactsDir)
		if err != nil {
			log.Panic(err)
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			fmt.Printf("Artifact file: %v\n", e.Name())

			fullPath := path.Join(b.ArtifactsDir, e.Name())

			// Is it a checksum file?
			if strings.HasSuffix(e.Name(), checksumSuffix) {
				// Find/create the arch that matches up with this checksum file.
				a := getOrCreateArch(strings.TrimSuffix(e.Name(), checksumSuffix))
				// Extract the checksum column from the file and store it in the summary.
				a.SHA256 = strings.Fields(readFileOrPanic(fullPath))[0]
				continue
			}
			// Is it an archive?
			for _, suffix := range archiveSuffixes {
				if strings.HasSuffix(e.Name(), suffix) {
					// Extract OS/ARCH from the end of a filename like:
					// "go.12.{...}.3.4.{GOOS}-{GOARCH}.tar.gz"
					extensionless := strings.TrimSuffix(e.Name(), suffix)
					osArch := extensionless[strings.LastIndex(extensionless, ".")+1:]
					osArchParts := strings.Split(osArch, "-")
					goOS, goArch := osArchParts[0], osArchParts[1]

					a := getOrCreateArch(e.Name())
					a.URL = b.DestinationURL + "/" + e.Name()
					a.Env = dockerversions.ArchEnv{
						GOOS:   goOS,
						GOARCH: goArch,
					}
					break
				}
			}
		}
	}

	arches := make([]*dockerversions.Arch, 0, len(archMap))
	for _, v := range archMap {
		arches = append(arches, v)
	}

	// Sort arch entries by unique field (URL) for stable order.
	sort.Slice(arches, func(i, j int) bool {
		return arches[i].URL < arches[j].URL
	})

	return &BuildAssets{
		Branch:  b.Branch,
		BuildID: b.BuildID,
		Version: goVersion + "-" + goRevision,
		Arches:  arches,
	}, nil
}

// getVersion reads the file at path, if it exists. If it doesn't exist, returns the default
// provided by the caller. If the file cannot be read for some other reason, panics. This logic
// helps with the "VERSION" files that are only present in Go release branches.
func getVersion(path string, defaultVersion string) (version string) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultVersion
		}
		log.Panic(err)
	}
	return string(bytes)
}

func readFileOrPanic(path string) string {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		log.Panic(err)
	}
	return string(bytes)
}
