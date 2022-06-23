// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package executil

import (
	"path"
	"testing"
)

func TestMakeWorkDir(t *testing.T) {
	tests := []struct {
		name    string
		rootDir string
	}{
		{"InsideExistingDir", t.TempDir()},
		{"InsideNonexistentDir", path.Join(t.TempDir(), "nonexistent")},
		{"DeeplyInsideNonexistentDir", path.Join(t.TempDir(), "nonexistent", "a", "b", "c")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MakeWorkDir(tt.rootDir)
			if err != nil {
				t.Error(err)
			}
		})
	}
}
