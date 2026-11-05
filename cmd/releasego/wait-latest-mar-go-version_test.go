// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"testing"

	"github.com/microsoft/go-infra/goversion"
)

func TestExpectedMarGoVersion(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		want          string
		checkRevision bool
	}{
		{
			name:    "older Go versions keep the upstream check",
			version: "1.25.8-2",
			want:    "go1.25.8 ",
		},
		{
			name:    "before revision reporting is reliable",
			version: "1.26.0-2",
			want:    "go1.26.0 ",
		},
		{
			name:          "revision reporting begins at 1.26.1",
			version:       "1.26.1-3",
			want:          "go1.26.1-3-microsoft ",
			checkRevision: true,
		},
		{
			name:          "omitted revision defaults to one",
			version:       "1.26.1",
			want:          "go1.26.1-1-microsoft ",
			checkRevision: true,
		},
		{
			name:          "later Go versions use revision reporting",
			version:       "1.27.0-2",
			want:          "go1.27.0-2-microsoft ",
			checkRevision: true,
		},
		{
			name:          "partial version keeps normalized defaults",
			version:       "1.27",
			want:          "go1.27.0-1-microsoft ",
			checkRevision: true,
		},
		{
			name:          "later semantic major versions use revision reporting",
			version:       "2.0.0-2",
			want:          "go2.0.0-2-microsoft ",
			checkRevision: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, checkRevision := expectedMarGoVersion(goversion.New(tt.version))
			if got != tt.want {
				t.Errorf("expectedMarGoVersion() = %q, want %q", got, tt.want)
			}
			if checkRevision != tt.checkRevision {
				t.Errorf("expectedMarGoVersion() checkRevision = %t, want %t", checkRevision, tt.checkRevision)
			}
		})
	}
}
