// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goversion contains utilities to parse and store a Go toolset version. It also handles
// extra parts that are used by the Microsoft build of Go to describe how it was built.
package goversion

import (
	"fmt"
	"strconv"
	"strings"
)

// GoVersion is the parsed representation of a Microsoft-built Go toolset version.
type GoVersion struct {
	// Original is the source data, without any defaults filled in.
	Original string

	// Major is the major version in semver terms, as in "Go 1".
	Major string
	// Minor is the minor version, referred to by Go as "major releases". Default: 0
	Minor string
	// Patch is the patch version, referred to by Go as "minor revisions". Default: 0.
	Patch string
	// Revision is an integer immediately after the first '-' (if any). These are revisions of the
	// Microsoft build and aren't associated with official Go releases. Default: 1.
	Revision string
	// Note is a non-integer string after a '-' separator, or not included. Common use is to
	// specify 'fips'. Default: empty string, indicating not provided.
	Note string
	// Prerelease is a beta or rc version string, for example "rc1" in "1.18rc1" or "beta1" in
	// "2beta1". After normal parsing, the major, minor, and patch version strings are scanned for a
	// non-numeric section. If one is found, it is removed from that part of the version and stored
	// in Prerelease.
	Prerelease string
}

// New parses a version string. Any parts left blank are filled in with default values.
func New(v string) *GoVersion {
	dashParts := strings.Split(v, "-")
	majorMinorPatch := dashParts[0]

	revision := "1"
	note := ""
	// If we have a "-", we need to determine if the remaining text is a revision (-1), revision and
	// note (-1-fips), or just note (-fips). This is done by consuming the first part if it's an
	// int, then the rest must be a note (if anything's left). This only works because a revision
	// must be an int, and a note must not start with an int part.
	if len(dashParts) > 1 {
		noteBegin := 1
		if isInt(dashParts[1]) {
			revision = dashParts[1]
			noteBegin = 2
		}
		note = strings.Join(dashParts[noteBegin:], "-")
	}

	dotParts := strings.Split(majorMinorPatch, ".")
	major := dotParts[0]
	minor := "0"
	if len(dotParts) > 1 {
		minor = dotParts[1]
	}
	patch := "0"
	if len(dotParts) > 2 {
		patch = dotParts[2]
	}

	var prerelease string
	extractPrerelease(&major, &prerelease)
	extractPrerelease(&minor, &prerelease)
	extractPrerelease(&patch, &prerelease)

	return &GoVersion{
		v,
		major, minor, patch,
		revision,
		note,
		prerelease,
	}
}

func (v *GoVersion) String() string {
	return fmt.Sprintf("%v (%v)", v.Original, v.Full())
}

func (v *GoVersion) MajorMinor() string {
	return v.Major + "." + v.Minor
}

func (v *GoVersion) MajorMinorPrerelease() string {
	return v.MajorMinor() + v.Prerelease
}

func (v *GoVersion) MajorMinorPatch() string {
	return v.MajorMinor() + "." + v.Patch
}

func (v *GoVersion) MajorMinorPatchPrerelease() string {
	return v.MajorMinorPatch() + v.Prerelease
}

func (v *GoVersion) MajorMinorPatchPrereleaseRevision() string {
	return v.MajorMinorPatchPrerelease() + "-" + v.Revision
}

// Full returns the full normalized version string, including Note if specified.
func (v *GoVersion) Full() string {
	return v.MajorMinorPatchPrereleaseRevision() + v.NoteWithPrefix()
}

// UpstreamFormatGitTag returns the version in the format upstream uses for Git tags.
func (v *GoVersion) UpstreamFormatGitTag() string {
	// In versions of Go older than 1.21, zeros are omitted. https://github.com/golang/go/issues/57631
	var omitZeros bool
	if v.Major == "1" {
		if minorNum, err := strconv.Atoi(v.Minor); err == nil {
			omitZeros = minorNum < 21
		}
	}
	var n string
	if omitZeros {
		n = v.Major
		if v.Patch != "0" || v.Minor != "0" {
			n += "." + v.Minor
		}
		if v.Patch != "0" {
			n += "." + v.Patch
		}
	} else {
		n = v.Major + "." + v.Minor + "." + v.Patch
	}
	return "go" + n + v.Prerelease
}

// NoteWithPrefix is a utility to help with version string construction. Returns Note with a "-"
// prefix, or empty string if Note isn't specified.
func (v *GoVersion) NoteWithPrefix() string {
	if v.Note == "" {
		return ""
	}
	return "-" + v.Note
}

func isInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// extractPrerelease searches "part" for a prerelease identifier, and if one is found, removes it,
// and sets "prerelease" to what it found.
func extractPrerelease(part, prerelease *string) {
	i := strings.IndexFunc(*part, func(r rune) bool { return !isInt(string(r)) })
	if i != -1 {
		*prerelease = (*part)[i:]
		*part = (*part)[:i]
	}
}

// GoVersions implements [sort.Interface] and sorts versions in descending order.
// If Major, Minor, Patch, or Revision of any GoVersion in the slice can't be parsed by
// [strconv.Atoi], the result of using this type is undefined.
type GoVersions []*GoVersion

func (versions GoVersions) Len() int      { return len(versions) }
func (versions GoVersions) Swap(i, j int) { versions[i], versions[j] = versions[j], versions[i] }
func (versions GoVersions) Less(i, j int) bool {
	less := func(a, b string) bool {
		intA, err := strconv.Atoi(a)
		if err != nil {
			return false
		}

		intB, err := strconv.Atoi(b)
		if err != nil {
			return false
		}
		return intA > intB
	}

	current, next := versions[i], versions[j]

	if current.Major != next.Major {
		return less(current.Major, next.Major)
	}
	if current.Minor != next.Minor {
		return less(current.Minor, next.Minor)
	}
	if current.Patch != next.Patch {
		return less(current.Patch, next.Patch)
	}
	if current.Revision != next.Revision {
		return less(current.Revision, next.Revision)
	}
	if current.Prerelease != next.Prerelease {
		return current.Prerelease < next.Prerelease
	}
	if current.Note != next.Note {
		return current.Note < next.Note
	}
	return false
}
