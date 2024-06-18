// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"testing"
)

func TestPublishAnnouncement(t *testing.T) {

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
