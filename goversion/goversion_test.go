// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goversion

import "testing"

func TestVersion_parseVersion(t *testing.T) {
	tests := []struct {
		name                                string
		version                             string
		major, minor, patch, revision, note string
	}{
		{
			"Full version",
			"1.2.3-4",
			"1", "2", "3", "4", "",
		},
		{
			"Major only",
			"1",
			"1", "0", "0", "1", "",
		},
		{
			"Major.minor",
			"1.42",
			"1", "42", "0", "1", "",
		},
		{
			"Major.minor-revision",
			"1.42-6",
			"1", "42", "0", "6", "",
		},
		{
			"Too many dotted parts",
			"1.2.3.4.5.6",
			"1", "2", "3", "1", "",
		},
		{
			"Many dashed parts",
			"1-2-3-4",
			"1", "0", "0", "2", "3-4",
		},
		{
			"Note without much else",
			"1-note",
			"1", "0", "0", "1", "note",
		},
		{
			"Note with revision",
			"1-2-note",
			"1", "0", "0", "2", "note",
		},
		{
			"Note with number after a dash",
			"1-note-2",
			"1", "0", "0", "1", "note-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.version)
			if got.Major != tt.major {
				t.Errorf("parseVersion() gotMajor = %q, major %q", got.Major, tt.major)
			}
			if got.Minor != tt.minor {
				t.Errorf("parseVersion() gotMinor = %q, minor %q", got.Minor, tt.minor)
			}
			if got.Patch != tt.patch {
				t.Errorf("parseVersion() gotPatch = %q, patch %q", got.Patch, tt.patch)
			}
			if got.Revision != tt.revision {
				t.Errorf("parseVersion() gotRevision = %q, revision %q", got.Revision, tt.revision)
			}
			if got.Note != tt.note {
				t.Errorf("parseVersion() gotNote = %q, note %q", got.Note, tt.note)
			}
		})
	}
}
