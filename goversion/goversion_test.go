// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goversion

import (
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name                                            string
		version                                         string
		major, minor, patch, revision, note, prerelease string
	}{
		{
			"Full version",
			"1.2.3-4",
			"1", "2", "3", "4", "", "",
		},
		{
			"Major only",
			"1",
			"1", "0", "0", "1", "", "",
		},
		{
			"Major.minor",
			"1.42",
			"1", "42", "0", "1", "", "",
		},
		{
			"Major.minor-revision",
			"1.42-6",
			"1", "42", "0", "6", "", "",
		},
		{
			"Too many dotted parts",
			"1.2.3.4.5.6",
			"1", "2", "3", "1", "", "",
		},
		{
			"Many dashed parts",
			"1-2-3-4",
			"1", "0", "0", "2", "3-4", "",
		},
		{
			"Note without much else",
			"1-note",
			"1", "0", "0", "1", "note", "",
		},
		{
			"Note with revision",
			"1-2-note",
			"1", "0", "0", "2", "note", "",
		},
		{
			"Note with number after a dash",
			"1-note-2",
			"1", "0", "0", "1", "note-2", "",
		},
		{
			"Prerelease version",
			"1.18rc1",
			"1", "18", "0", "1", "", "rc1",
		},
		{
			"Major beta version",
			"2beta1",
			"2", "0", "0", "1", "", "beta1",
		},
		{
			// This case should never happen, but the current behavior is that the parser removes
			// all prerelease parts and only preserves the last-specified prerelease identifier.
			"Pick most minor prerelease part",
			"2beta1.42rc2",
			"2", "42", "0", "1", "", "rc2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.version)
			if got.Major != tt.major {
				t.Errorf("New() gotMajor = %q, major %q", got.Major, tt.major)
			}
			if got.Minor != tt.minor {
				t.Errorf("New() gotMinor = %q, minor %q", got.Minor, tt.minor)
			}
			if got.Patch != tt.patch {
				t.Errorf("New() gotPatch = %q, patch %q", got.Patch, tt.patch)
			}
			if got.Revision != tt.revision {
				t.Errorf("New() gotRevision = %q, revision %q", got.Revision, tt.revision)
			}
			if got.Note != tt.note {
				t.Errorf("New() gotNote = %q, note %q", got.Note, tt.note)
			}
			if got.Prerelease != tt.prerelease {
				t.Errorf("New() gotPrerelease = %q, note %q", got.Prerelease, tt.prerelease)
			}
		})
	}
}

func TestGoVersion_UpstreamFormatGitTag(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"drop all zeros", "1.0.0", "go1"},
		{"do not drop middle zero", "1.0.1", "go1.0.1"},
		{"drop ending zero", "1.1.0", "go1.1"},
		{"never drop ones", "1.1.1", "go1.1.1"},
		{"drop zeroes for v2", "2.0.0", "go2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := New(tt.version)
			if got := v.UpstreamFormatGitTag(); got != tt.want {
				t.Errorf("UpstreamFormatGitTag() = %v, want %v", got, tt.want)
			}
		})
	}
}
