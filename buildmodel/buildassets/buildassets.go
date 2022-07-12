// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package buildassets represents a build asset JSON file that describes the output of a Go build.
// We use this file to update other repos (in particular Go Docker) to that build.
//
// This file's structure is controlled by our team: not .NET Docker, Go, or the official golang
// image team. So, we can choose to reuse parts of other files' schema to keep it simple.
package buildassets

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	"github.com/microsoft/go-infra/goversion"
)

// BuildAssets is the root object of a build asset JSON file.
type BuildAssets struct {
	// Branch that produced this build. This is not used for auto-update.
	Branch string `json:"branch"`
	// BuildID is a link to the build that produced these assets. It is not used for auto-update.
	BuildID string `json:"buildId"`

	// Version of the build, as 'major.minor.patch-revision'. Doesn't include version note (-fips).
	Version string `json:"version"`
	// Arches is the list of artifacts that was produced for this version, typically one per target
	// os/architecture. The name "Arches" is shared with the versions.json format.
	Arches []*dockerversions.Arch `json:"arches"`

	// GoSrcURL is a URL pointing at a tar.gz archive of the pre-patched Go source code.
	GoSrcURL string `json:"goSrcURL"`
}

// GetDockerRepoTargetBranch returns the Go Docker images repo branch that needs to be updated based
// on the branch of the Go repo that was built, or returns empty string if no branch needs to be
// updated.
func (b BuildAssets) GetDockerRepoTargetBranch() string {
	if b.Branch == "main" ||
		strings.HasPrefix(b.Branch, "release-branch.") ||
		strings.HasPrefix(b.Branch, "dev.boringcrypto") {

		return "microsoft/nightly"
	}
	if strings.HasPrefix(b.Branch, "dev/official/") {
		return b.Branch
	}
	return ""
}

// GetDockerRepoVersionsKey gets the Docker Versions key that should be updated with new builds
// listed in this BuildAssets file.
func (b BuildAssets) GetDockerRepoVersionsKey() string {
	return dockerRepoVersionsKey(goversion.New(b.Version), b.Branch)
}

// GetPreviousMinorDockerRepoVersionsKey gets the Docker Versions key of the minor Go release before
// the one in b. This can be used to find the previous release's information when setting up Docker
// images for the next one.
func (b BuildAssets) GetPreviousMinorDockerRepoVersionsKey() (string, error) {
	v := goversion.New(b.Version)

	// If we're looking for the previous version for e.g. 1.18rc1, we want to find 1.17, not 1.17-rc.
	v.Prerelease = ""

	minor, err := strconv.Atoi(v.Minor)
	if err != nil {
		return "", fmt.Errorf("unable to find previous minor version for %q: minor version not an int: %w", v, err)
	}
	v.Minor = strconv.Itoa(minor - 1)
	return dockerRepoVersionsKey(v, b.Branch), nil
}

func dockerRepoVersionsKey(v *goversion.GoVersion, branch string) string {
	key := v.Major + "." + v.Minor
	if v.Major == "main" {
		// Call this "main", not "main.0", for cleaner directory names.
		key = v.Major
	}

	// If there is any prerelease specified (beta or rc), call it "-rc". This matches upstream, e.g.
	// releasing 1.19beta1 and 1.19rc1 Go builds with Dockerfiles in a "1.19-rc" directory.
	if v.Prerelease != "" {
		key += "-rc"
	}

	if strings.HasPrefix(branch, "dev.boringcrypto") {
		key += "-fips"
	}
	return key
}

// GoVersion parses Version in the format that Microsoft builds of Go use. The BuildAssets file
// doesn't include the Note (-fips), so this is added based on the branch.
func (b BuildAssets) GoVersion() *goversion.GoVersion {
	v := b.Version
	if strings.HasPrefix(b.Branch, "dev.boringcrypto") {
		v += "-fips"
	}
	return goversion.New(v)
}

// Basic information about how the build output assets are formatted by Microsoft builds of Go. The
// archiving infra is stored in each release branch to make it local to the code it operates on and
// less likely to unintentionally break, so some of that information is duplicated here.
var archiveSuffixes = []string{".tar.gz", ".zip"}
var checksumSuffix = ".sha256"
var sourceArchiveSuffix = ".src.tar.gz"

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
	// Look for VERSION files in the submodule and the source repo. Prefer the source repo.
	goVersion, err := getVersion(filepath.Join(b.SourceDir, "VERSION"), "main")
	if err != nil {
		return nil, err
	}
	if goVersion == "main" {
		goVersion, err = getVersion(filepath.Join(b.SourceDir, "go", "VERSION"), "main")
		if err != nil {
			return nil, err
		}
	}

	goRevision, err := getVersion(filepath.Join(b.SourceDir, "MICROSOFT_REVISION"), "1")
	if err != nil {
		return nil, err
	}

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

	var goSrcURL string

	if b.ArtifactsDir != "" {
		entries, err := os.ReadDir(b.ArtifactsDir)
		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			fmt.Printf("Artifact file: %v\n", e.Name())

			fullPath := filepath.Join(b.ArtifactsDir, e.Name())

			// Is it a source archive file?
			if strings.HasSuffix(e.Name(), sourceArchiveSuffix) {
				goSrcURL = b.DestinationURL + "/" + e.Name()
				continue
			}
			// Is it a source archive file checksum?
			if strings.HasSuffix(e.Name(), sourceArchiveSuffix+checksumSuffix) {
				// The build asset JSON doesn't keep track of this info.
				continue
			}
			// Is it a checksum file?
			if strings.HasSuffix(e.Name(), checksumSuffix) {
				// Find/create the arch that matches up with this checksum file.
				a := getOrCreateArch(strings.TrimSuffix(e.Name(), checksumSuffix))
				// Extract the checksum column from the file and store it in the summary.
				checksumLine, err := os.ReadFile(fullPath)
				if err != nil {
					return nil, fmt.Errorf("unable to read checksum file '%v': %w", fullPath, err)
				}
				a.SHA256 = strings.Fields(string(checksumLine))[0]
				continue
			}
			// Is it an archive?
			for _, suffix := range archiveSuffixes {
				if strings.HasSuffix(e.Name(), suffix) {
					// Extract OS/ARCH from the end of a filename like:
					// "go.12.{...}.3.4.{GOOS}-{GOARCH}.tar.gz"
					extensionless := strings.TrimSuffix(e.Name(), suffix)
					lastDotIndex := strings.LastIndex(extensionless, ".")
					if lastDotIndex == -1 {
						return nil, fmt.Errorf(
							"expected '.' in %q after removing extension from archive %q, but found none",
							extensionless,
							e.Name(),
						)
					}

					osArch := extensionless[lastDotIndex+1:]
					osArchParts := strings.Split(osArch, "-")
					if len(osArchParts) != 2 {
						return nil, fmt.Errorf(
							"expected two parts separated by '-' in last segment %q of archive %q, but found %v",
							osArch,
							e.Name(),
							len(osArchParts),
						)
					}

					goOS, goArch := osArchParts[0], osArchParts[1]
					var goARM string

					// "armv6l" in the filename is a special case: it represents GOARCH=arm GOARM=6.
					// There are no other active values of GOARM, so it's not worth generalizing.
					if goArch == "armv6l" {
						goArch = "arm"
						goARM = "6"
					}

					a := getOrCreateArch(e.Name())
					a.URL = b.DestinationURL + "/" + e.Name()
					a.Env = dockerversions.ArchEnv{
						GOOS:   goOS,
						GOARCH: goArch,
						GOARM:  goARM,
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
		Branch:   b.Branch,
		BuildID:  b.BuildID,
		Version:  goVersion + "-" + goRevision,
		Arches:   arches,
		GoSrcURL: goSrcURL,
	}, nil
}

// getVersion reads the file at path, if it exists. If it doesn't exist, returns the default
// provided by the caller. If the file cannot be read for some other reason, return the error. This
// logic helps with the "VERSION" files that are only present in Go release branches, and handles
// unusual VERSION files that may contain a newline by only reading the first line.
func getVersion(path string, defaultVersion string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultVersion, nil
		}
		return "", fmt.Errorf("unable to open VERSION file '%v': %w", path, err)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	_ = s.Scan()
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("unable to read VERSION file '%v': %w", path, err)
	}
	return s.Text(), nil
}
