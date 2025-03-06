// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azurelinux

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
)

const (
	CGManifestPath = "cgmanifest.json"
	golangDir      = "SPECS/golang"
)

// RepositoryModel stores all the file content from the Azure Linux repository that's relevant to
// the Microsoft build of Go. An update flow involves reading all this data, updating it as needed
// using the utility methods, then writing it back in some way.
type RepositoryModel struct {
	CGManifest []byte
	Versions   []*Version
}

func ReadModel(f githubutil.SimplifiedFS) (*RepositoryModel, error) {
	var rm RepositoryModel

	var err error
	if rm.CGManifest, err = f.ReadFile(CGManifestPath); err != nil {
		return nil, err
	}

	golangFiles, err := f.ReadDir(golangDir)
	if err != nil {
		return nil, err
	}

	// Find golang[-]{version}.spec files.
	for _, file := range golangFiles {
		after, ok := strings.CutPrefix(file.Name(), "golang")
		if !ok {
			continue
		}
		dashVersion, ok := strings.CutSuffix(after, ".spec")
		if !ok {
			continue
		}
		version := strings.TrimPrefix(dashVersion, "-")

		v, err := ReadVersion(f, version)
		if err != nil {
			return nil, err
		}
		rm.Versions = append(rm.Versions, v)
	}

	return &rm, nil
}

func (rm *RepositoryModel) UpdateCGManifest(buildAssets *buildassets.BuildAssets) error {
	if len(rm.CGManifest) == 0 {
		return fmt.Errorf("provided CG manifest content is empty")
	}

	type Registration struct {
		Component struct {
			Type    string `json:"type"`
			Comment string `json:"comment,omitempty"`
			Other   struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				DownloadURL string `json:"downloadUrl"`
			} `json:"other"`
		} `json:"component"`
	}

	type CGManifest struct {
		Registrations []Registration `json:"Registrations"`
		Version       int            `json:"Version"`
	}

	var cgManifest CGManifest
	if err := json.Unmarshal(rm.CGManifest, &cgManifest); err != nil {
		return fmt.Errorf("failed to parse cgmanifest.json: %w", err)
	}

	// First, remove all registrations with the same major version as the new Go version. Normally
	// there will either be 1 or 0. If there is a "golang" registration (there should be), keep
	// track of its location so we can insert a new entry at the same place if necessary. This
	// helps avoid touching the rest of the file and conflicting with other packages' new entries.
	var foundIndex int
	for i, reg := range cgManifest.Registrations {
		if reg.Component.Other.Name != "golang" {
			continue
		}
		foundIndex = i
		// Azure Linux maintains two major versions of Go. Only update the current one.
		regVersion := goversion.New(reg.Component.Other.Version)
		if regVersion.MajorMinor() != buildAssets.GoVersion().MajorMinor() {
			continue
		}
		cgManifest.Registrations = slices.Delete(cgManifest.Registrations, i, i+1)

		reg.Component.Other.Version = buildAssets.GoVersion().MajorMinorPatch()
		reg.Component.Other.DownloadURL = githubReleaseDownloadURL(buildAssets)
		break
	}

	// Then, add the new registration.
	var r Registration
	r.Component.Type = "other"
	r.Component.Other.Name = "golang"
	r.Component.Other.Version = buildAssets.GoVersion().MajorMinorPatch()
	r.Component.Other.DownloadURL = githubReleaseDownloadURL(buildAssets)
	cgManifest.Registrations = slices.Insert(cgManifest.Registrations, foundIndex, r)

	// Serialize the updated cgManifest back to JSON
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	// Indent the way we know Azure Linux does.
	encoder.SetIndent("", "  ")

	err := encoder.Encode(cgManifest)
	if err != nil {
		return fmt.Errorf("failed to marshal updated cgmanifest.json: %w", err)
	}
	rm.CGManifest = buf.Bytes()
	return nil
}

// UpdateMatchingVersion updates the version matching the assets/latestMajor using
// assets/changelogDate, then returns the matched Version. The matched Version may be useful to
// figure out what files must be written to apply the update.
//
// This is the only method necessary to run a full update flow. If an error occurs, the changes
// made up to that point are applied and if multiple errors occur, they are joined. This allows for
// partial upgrades to be applied, which is useful for testing and manual dev work when (e.g.) the
// Azure Linux repository has partially conflicting changes.
func (rm *RepositoryModel) UpdateMatchingVersion(assets *buildassets.BuildAssets, latestMajor bool, changelogDate time.Time, author string) (*Version, error) {
	v := rm.matchingVersion(assets, latestMajor)
	if v == nil {
		return nil, fmt.Errorf("failed to find matching version for %q", assets.GoVersion().Full())
	}
	err := errors.Join(
		rm.UpdateCGManifest(assets),
		v.Update(assets, changelogDate, author),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update the model: %v", err)
	}
	return v, nil
}

func (rm *RepositoryModel) matchingVersion(assets *buildassets.BuildAssets, latestMajor bool) *Version {
	for _, v := range rm.Versions {
		if v.SpecPath == golangSpecFilepath(assets, latestMajor) {
			return v
		}
	}
	return nil
}

// Version stores the content for a specific version of Go.
type Version struct {
	// SpecPath is the full path of this spec file inside the repository, e.g.
	// "SPECS/golang/golang.spec".
	SpecPath string
	// Spec content.
	Spec []byte

	// SignaturesPath is the full path of this signatures file inside the repository.
	SignaturesPath string
	// Signatures content.
	Signatures []byte
}

func ReadVersion(f githubutil.SimplifiedFS, version string) (*Version, error) {
	name := "golang"
	if version != "" {
		name += "-" + version
	}

	v := &Version{
		SpecPath:       path.Join(golangDir, name+".spec"),
		SignaturesPath: path.Join(golangDir, name+".signatures.json"),
	}
	var err error
	if v.Spec, err = f.ReadFile(v.SpecPath); err != nil {
		return nil, err
	}
	if v.Signatures, err = f.ReadFile(v.SignaturesPath); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *Version) Update(assets *buildassets.BuildAssets, changelogDate time.Time, author string) error {
	oldFilename, err := v.parseGoArchiveName()
	if err != nil {
		return err
	}

	// Do as much work as we can and return all encountered errors.
	// This helps a dev if they want to take the result and run with it.
	return errors.Join(
		v.updateSignatures(oldFilename, path.Base(assets.GoSrcURL), assets.GoSrcSHA256),
		v.updateSpec(assets, changelogDate, author),
	)
}

func (v *Version) updateSpec(assets *buildassets.BuildAssets, changelogDate time.Time, author string) error {
	if len(v.Spec) == 0 {
		return fmt.Errorf("provided spec file content is empty")
	}

	// Gather the old spec file data we need to make the updated one.
	oldVersion, oldRelease, err := v.parseSpecVersion()
	if err != nil {
		return err
	}

	newVersion, newRelease := updateSpecVersion(assets, oldVersion, oldRelease)

	// Perform updates with a series of replacements.
	if err := v.updateGoArchiveName(path.Base(assets.GoSrcURL)); err != nil {
		return fmt.Errorf("error updating Go archive name in spec file: %w", err)
	}
	if err := v.updateGoRevision(assets.GoVersion().Revision); err != nil {
		return fmt.Errorf("error updating Go revision in spec file: %w", err)
	}
	if err := v.updateVersion(newVersion); err != nil {
		return err
	}
	if err := v.updateRelease(newRelease); err != nil {
		return err
	}
	if err := v.addChangelog(changelogDate, assets, author, newVersion, newRelease); err != nil {
		return err
	}
	return nil
}

func (v *Version) parseSpecVersion() (version *goversion.GoVersion, revision int, err error) {
	version, err = v.parseVersion()
	if err != nil {
		return nil, 0, err
	}
	revision, err = v.parseRelease()
	if err != nil {
		return nil, 0, err
	}
	return version, revision, nil
}

func (v *Version) regexpReplace(re *regexp.Regexp, replacement string) error {
	if !re.Match(v.Spec) {
		return fmt.Errorf("no match found in spec content")
	}
	v.Spec = re.ReplaceAll(v.Spec, []byte(replacement))
	return nil
}

var specFileVersionRegex = regexp.MustCompile(`(Version: +)(.+)`)

func (v *Version) parseVersion() (*goversion.GoVersion, error) {
	matches := specFileVersionRegex.FindSubmatch(v.Spec)
	if matches == nil {
		return nil, fmt.Errorf("no Version declaration found in spec content")
	}
	return goversion.New(string(matches[2])), nil
}

func (v *Version) updateVersion(newVersion string) error {
	return v.regexpReplace(specFileVersionRegex, "${1}"+escapeRegexReplacementValue(newVersion))
}

var specFileReleaseRegex = regexp.MustCompile(`(Release: +)(.+)(%\{\?dist\})`)

func (v *Version) parseRelease() (int, error) {
	matches := specFileReleaseRegex.FindSubmatch(v.Spec)
	if matches == nil {
		return 0, fmt.Errorf("no Release declaration found in spec content")
	}
	releaseInt, err := strconv.Atoi(string(matches[2]))
	if err != nil {
		return 0, fmt.Errorf("failed to parse old Release number: %v", err)
	}
	return releaseInt, nil
}

func (v *Version) updateRelease(newRelease string) error {
	return v.regexpReplace(
		specFileReleaseRegex,
		"${1}"+escapeRegexReplacementValue(newRelease)+"${3}")
}

var specFileGoFilenameRegex = regexp.MustCompile(`(%global ms_go_filename +)(.+)`)

func (v *Version) parseGoArchiveName() (string, error) {
	matches := specFileGoFilenameRegex.FindSubmatch(v.Spec)
	if matches == nil {
		return "", fmt.Errorf("no Go archive filename declaration found in spec content")
	}
	return strings.TrimSpace(string(matches[2])), nil
}

func (v *Version) updateGoArchiveName(newArchiveName string) error {
	return v.regexpReplace(
		specFileGoFilenameRegex,
		"${1}"+escapeRegexReplacementValue(newArchiveName))
}

var specFileRevisionRegex = regexp.MustCompile(`(%global ms_go_revision +)(.+)`)

func (v *Version) updateGoRevision(newRevisionVersion string) error {
	if !specFileRevisionRegex.Match(v.Spec) {
		return fmt.Errorf("no Go revision version declaration found in spec content")
	}
	v.Spec = specFileRevisionRegex.ReplaceAll(
		v.Spec,
		[]byte("${1}"+escapeRegexReplacementValue(newRevisionVersion)),
	)
	return nil
}

func (v *Version) addChangelog(changelogDate time.Time, assets *buildassets.BuildAssets, author, newVersion, newRelease string) error {
	const template = `%%changelog
* %s %s - %s-%s
- Bump version to %s
`
	formattedTime := changelogDate.Format("Mon Jan 02 2006")

	changelog := fmt.Sprintf(
		template,
		formattedTime, author, newVersion, newRelease,
		assets.GoVersion().Full())

	before := string(v.Spec)
	after := strings.Replace(before, "%changelog", changelog, 1)
	if before == after {
		return fmt.Errorf("failed to add changelog entry to spec file")
	}
	v.Spec = []byte(after)
	return nil
}

func (v *Version) updateSignatures(oldFilename, newFilename, newHash string) error {
	if len(v.Signatures) == 0 {
		return fmt.Errorf("provided signatures file content is empty")
	}

	type JSONSignature struct {
		Signatures map[string]string `json:"Signatures"`
	}

	var data JSONSignature
	if err := json.Unmarshal(v.Signatures, &data); err != nil {
		return err
	}

	// Check if the oldFilename exists in the map
	if _, exists := data.Signatures[oldFilename]; !exists {
		return fmt.Errorf("old filename %q not found in signatures", oldFilename)
	}

	// Update the filename and hash in the map
	delete(data.Signatures, oldFilename) // Remove the old entry
	// add new filename and hash
	data.Signatures[newFilename] = newHash

	// Marshal the updated JSON back to a byte slice
	updatedJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	updatedJSON = append(updatedJSON, '\n') // Add a newline at the end

	v.Signatures = updatedJSON
	return nil
}

// updateSpecVersion decides on the new version and release numbers, given the old numbers and the
// build to update to.
func updateSpecVersion(assets *buildassets.BuildAssets, oldVersion *goversion.GoVersion, oldRelease int) (version, release string) {
	// Decide on the new Azure Linux golang package release number.
	//
	// We don't use assets.GoVersion().Revision because Azure Linux may have incremented the
	// release version manually for an Azure-Linux-specific fix, or they may have skipped one of
	// our releases.
	var newRelease int
	if assets.GoVersion().MajorMinorPatch() != oldVersion.MajorMinorPatch() {
		// When updating to a new upstream Go version, reset release number to 1.
		newRelease = 1
	} else {
		// When the upstream Go version didn't change, increment the release number. This means
		// there has been a patch specific to Microsoft build of Go.
		newRelease = oldRelease + 1
	}

	return assets.GoVersion().MajorMinorPatch(), strconv.Itoa(newRelease)
}

// escapeRegexReplacementValue returns s where all "$" signs are replaced with with "$$" for the
// purposes of passing the result to ReplaceAllString. Use this to make sure a replacement value
// doesn't get interpreted as part of a regex replacement pattern in [Regexp.Expand].
//
// As of writing, we don't expect "$" in the replacement values. However, it's easy to handle, and
// it's cleaner than rejecting "$" and propagating an error.
func escapeRegexReplacementValue(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

func githubReleaseURL(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf(
		"https://github.com/microsoft/go/releases/tag/v%s",
		assets.GoVersion().Full(),
	)
}

func githubReleaseDownloadURL(assets *buildassets.BuildAssets) string {
	return fmt.Sprintf(
		"https://github.com/microsoft/go/releases/download/v%s/%s",
		assets.GoVersion().Full(),
		path.Base(assets.GoSrcURL),
	)
}

func golangSpecFilepath(assets *buildassets.BuildAssets, latestMajor bool) string {
	return "SPECS/golang/" + golangSpecName(assets, latestMajor) + ".spec"
}

func golangSpecName(assets *buildassets.BuildAssets, latestMajor bool) string {
	if latestMajor {
		return "golang"
	}
	return "golang-" + assets.GoVersion().MajorMinor()
}
