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

	return &GoVersion{
		v,
		major, minor, patch,
		revision,
		note,
	}
}

func (v *GoVersion) String() string {
	return fmt.Sprintf("%v (%v)", v.Original, v.Full())
}

func (v *GoVersion) MajorMinor() string {
	return v.Major + "." + v.Minor
}

func (v *GoVersion) MajorMinorPatch() string {
	return v.MajorMinor() + "." + v.Patch
}

func (v *GoVersion) MajorMinorPatchRevision() string {
	return v.MajorMinorPatch() + "-" + v.Revision
}

// Full returns the full normalized version string, including Note if specified.
func (v *GoVersion) Full() string {
	return v.MajorMinorPatchRevision() + v.NoteWithPrefix()
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
