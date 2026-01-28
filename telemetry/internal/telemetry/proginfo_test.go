// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry_test

import (
	"os"
	"runtime/debug"
	"testing"

	"github.com/microsoft/go-infra/telemetry/internal/telemetry"
)

func TestProgramInfoVersion(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{
			version: "go1.21.0",
			want:    "go1.21.0",
		},
		{
			version: "devel +abc123",
			want:    "devel",
		},
		{
			version: "go1.21.0-microsoft",
			want:    "go1.21.0",
		},
		{
			version: "go1.21.1-0-microsoft",
			want:    "go1.21.1-0",
		},
		{
			version: "go1.21.1-0-microsoft ABC",
			want:    "go1.21.1-0",
		},
		{
			version: "go1.21.1-0",
			want:    "devel",
		},
		{
			version: "invalid-version",
			want:    "devel",
		},

		// Main branch devel version.
		{
			version: "go1.21-devel_123abcdef timestamp",
			want:    "devel",
		},
		{
			version: "go1.21-microsoft-devel_123abcdef timestamp",
			want:    "devel",
		},

		// RC version.
		{
			version: "go1.21rc1",
			want:    "go1.21rc1",
		},
		{
			version: "go1.21rc1-microsoft",
			want:    "go1.21rc1",
		},

		// Other vendor.
		{
			version: "go1.21.0-somevendor",
			want:    "devel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			gotGoVers, _ := telemetry.ProgramInfo(&debug.BuildInfo{
				GoVersion: tt.version,
			})

			if gotGoVers != tt.want {
				t.Errorf("ProgramInfo() goVers = %v, want %v", gotGoVers, tt.want)
			}
		})
	}
}

func TestProgramInfoPath(t *testing.T) {
	// Save original os.Args and restore after test
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	tests := []struct {
		name         string
		args         []string
		wantProgPath string
	}{
		{
			name:         "exe extension stripped",
			args:         []string{"testprog.exe"},
			wantProgPath: "testprog",
		},
		{
			name:         "no exe extension",
			args:         []string{"testprog"},
			wantProgPath: "testprog",
		},
		{
			name:         "full path with exe",
			args:         []string{"/usr/local/bin/myapp.exe"},
			wantProgPath: "myapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Args = tt.args
			_, gotProgPath := telemetry.ProgramInfo(&debug.BuildInfo{
				GoVersion: "go1.21.0",
				Path:      "",
			})

			if gotProgPath != tt.wantProgPath {
				t.Errorf("ProgramInfo() progPath = %v, want %v", gotProgPath, tt.wantProgPath)
			}
		})
	}
}
