// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/go-infra/goldentest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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
			goldentest.Check(t, tt.name+".golden.md", b.String())
		})
	}
}

func TestFindGoAnnouncementThread(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		wantText string
	}{
		{
			name:     "paired security release",
			versions: []string{"1.22.4-1", "1.21.11-1"},
			wantText: "[security] Go 1.22.4 and Go 1.21.11 are released",
		},
		{
			name:     "single security release with paired announcement",
			versions: []string{"1.22.4-1"},
			wantText: "[security] Go 1.22.4 and Go 1.21.11 are released",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releaseInfo := NewReleaseInfo(
				time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC),
				tt.versions,
				"test",
				true,
			)
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				query := req.URL.Query().Get("q")
				if wantQuery := `subject:"Go 1.22.4"`; query != wantQuery {
					return nil, fmt.Errorf("search query = %q, want %q", query, wantQuery)
				}
				body := `<html><head><base href="https://groups.google.com/"></head><body>` +
					`<a href="/g/golang-announce/c/thread-id">` + tt.wantText + `</a>` +
					`</body></html>`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})}

			got, err := findGoAnnouncementThread(context.Background(), releaseInfo, client)
			if err != nil {
				t.Fatal(err)
			}
			if want := "https://groups.google.com/g/golang-announce/c/thread-id"; got != want {
				t.Fatalf("findGoAnnouncementThread() = %q, want %q", got, want)
			}

			releaseInfo.GoAnnouncementLink = got
			var announcement bytes.Buffer
			if err := releaseInfo.WriteAnnouncement(&announcement); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(announcement.String(), "[Go release announcement]("+got+")") {
				t.Errorf("announcement does not contain the Go release announcement link:\n%s", announcement.String())
			}
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

func TestGenerateBlogFilePath(t *testing.T) {
	tests := []struct {
		releaseDate time.Time
		slug        string
		expected    string
	}{
		{
			releaseDate: time.Date(2024, 2, 4, 0, 0, 0, 0, time.UTC),
			slug:        "go-release-1-21-7-1-and-1-20-14-1",
			expected:    "2024/02-February/go-release-1-21-7-1-and-1-20-14-1.md",
		},
		{
			releaseDate: time.Date(2024, 12, 25, 0, 0, 0, 0, time.UTC),
			slug:        "go",
			expected:    "2024/12-December/go.md",
		},
	}

	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			got := generateBlogFilePath(tt.releaseDate, tt.slug)
			if got != tt.expected {
				t.Errorf("generateBlogFilePath(%v, %q) = %q; want %q", tt.releaseDate, tt.slug, got, tt.expected)
			}
		})
	}
}
