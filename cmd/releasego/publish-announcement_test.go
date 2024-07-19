// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/go-infra/goldentest"
)

func Test_ReleaseInfo_WriteAnnouncement(t *testing.T) {
	var (
		// testTime is June 4, 2024
		testTime = time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC)
		author   = "Test Author"
	)

	tests := []struct {
		name string
		ri   *ReleaseInfo
	}{
		{"2024-06-04-real", NewReleaseInfo(testTime, []string{"1.22.4-1", "1.21.11-1"}, author, true)},
		{"2024-06-04-nonsecurity", NewReleaseInfo(testTime, []string{"1.22.4-1", "1.21.11-1"}, author, false)},
		{"only-one-branch", NewReleaseInfo(testTime, []string{"1.22.8-3"}, author, true)},
		{"three-branches", NewReleaseInfo(testTime, []string{"1.22.8-1", "1.23.1-1", "1.21.11-16"}, author, true)},
		{"2024-06-04-note", NewReleaseInfo(testTime, []string{"1.22.4-1-fips", "1.22.4-1", "1.21.11-1", "1.21.11-1-fips"}, author, false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := tt.ri.WriteAnnouncement(&b); err != nil {
				t.Errorf("WriteAnnouncement() error = %v", err)
				return
			}
			goldentest.Check(
				t, "Test_ReleaseInfo_WriteAnnouncement ",
				filepath.Join("testdata", "publish-announcement", tt.name+".golden.md"),
				b.String())
		})
	}
}

func TestGenerateSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello, world!", "hello-world"},
		{"This is a test: replace all punctuation with hyphens.", "this-is-a-test-replace-all-punctuation-with-hyphens"},
		{"Multiple    spaces should become one-hyphen", "multiple-spaces-should-become-one-hyphen"},
		{"!Trailing and leading punctuation!!!", "trailing-and-leading-punctuation"},
		{"dots.are.replaced.to-hyphens", "dots-are-replaced-to-hyphens"},
		{"Special characters #$&*^ are removed", "special-characters-are-removed"},
		{"Remove -- consecutive hyphens", "remove-consecutive-hyphens"},
		{"  Trim leading and trailing spaces and hyphens  ", "trim-leading-and-trailing-spaces-and-hyphens"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := generateSlug(tt.input)
			if got != tt.expected {
				t.Errorf("generateSlug(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGoReleaseVersionLink(t *testing.T) {
	releaseID := "go1.22.3"
	expected := "https://go.dev/doc/devel/release#go1.22.3"

	result := createGoReleaseLinkFromVersion(releaseID)
	if result != expected {
		t.Errorf("expected the release link to be %q, but got %q", expected, result)
	}
}
