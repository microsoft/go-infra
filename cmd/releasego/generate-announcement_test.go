package main

import (
	"testing"
)

func TestGenerateAnnouncement(t *testing.T) {

}

func TestVersionDetails(t *testing.T) {
	tests := []struct {
		versions []string
		expected string
	}{
		{
			versions: []string{},
			expected: "",
		},
		{
			versions: []string{"1.22.3-1"},
			expected: "[Go 1.22.3-1 is released]",
		},
		{
			versions: []string{"1.22.3-1", "1.21.10-1"},
			expected: "[Go 1.22.3-1 and Go 1.21.10-1 are released]",
		},
		{
			versions: []string{"1.22.3-1", "1.21.10-1", "1.20.9-1"},
			expected: "[Go 1.22.3-1, Go 1.21.10-1, and Go 1.20.9-1 are released]",
		},
		{
			versions: []string{"1.22.3-1", "1.21.10-1", "1.20.9-1", "1.19.8-1"},
			expected: "[Go 1.22.3-1, Go 1.21.10-1, Go 1.20.9-1, and Go 1.19.8-1 are released]",
		},
	}

	for _, tt := range tests {
		releaseInfo := new(ReleaseInfo)
		releaseInfo.Versions = tt.versions
		result := releaseInfo.VersionDetails()
		if result != tt.expected {
			t.Errorf("got %q, want %q", result, tt.expected)
		}
	}
}
