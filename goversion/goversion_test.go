// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goversion

import (
	"sort"
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
		// 1.21 changed zero behavior. https://github.com/golang/go/issues/57631
		{"no zero in 1.20", "1.20.0", "go1.20"},
		{"zero in 1.21", "1.21.0", "go1.21.0"},
		{"zero in 1.22", "1.22.0", "go1.22.0"},
		{"same 1.21 patching behavior", "1.21.1", "go1.21.1"},
		{"don't drop zeros for v2", "2.0.0", "go2.0.0"},
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

func TestGoVersions_Sort(t *testing.T) {
	tests := []struct {
		name     string
		versions GoVersions
		expected GoVersions
	}{
		{
			name: "basic version sorting",
			versions: GoVersions{
				New("1.2.3"),
				New("1.2.1"),
				New("1.3.0"),
				New("1.2.3-2"),
				New("1.2.3-1"),
			},
			expected: GoVersions{
				New("1.3.0"),
				New("1.2.3-2"),
				New("1.2.3"),
				New("1.2.3-1"),
				New("1.2.1"),
			},
		},
		{
			name: "version with prerelease and note",
			versions: GoVersions{
				New("1.2.3-beta"),
				New("1.2.3-rc1"),
				New("1.2.3-1-fips"),
				New("1.2.3"),
				New("1.2.3-2"),
			},
			expected: GoVersions{
				New("1.2.3-2"),
				New("1.2.3"),
				New("1.2.3-beta"),
				New("1.2.3-1-fips"),
				New("1.2.3-rc1"),
			},
		},
		{
			name: "sorting with major and minor versions",
			versions: GoVersions{
				New("2.0.0"),
				New("1.10.0"),
				New("1.2.3"),
				New("1.2.0"),
				New("1.3.0"),
			},
			expected: GoVersions{
				New("2.0.0"),
				New("1.10.0"),
				New("1.3.0"),
				New("1.2.3"),
				New("1.2.0"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Sort the versions
			sort.Sort(tt.versions)
			for i, v := range tt.versions {
				if *v != *tt.expected[i] {
					t.Errorf("expected %v at index %d, got %v", tt.expected[i].Original, i, v.Original)
				}
			}
		})
	}
}
