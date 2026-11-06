// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
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

func TestFindGoAnnouncements(t *testing.T) {
	const prefix = "https://groups.google.com/g/golang-announce/c/"
	paired := GoAnnouncement{
		Subject: "[security] Go 1.22.4 and Go 1.21.11 are released",
		URL:     prefix + "paired",
	}
	newer := GoAnnouncement{Subject: "Go 1.23.1 is released", URL: prefix + "newer"}
	older := GoAnnouncement{Subject: "Go 1.21.11 is released", URL: prefix + "older"}
	tests := []struct {
		name     string
		versions []string
		security bool
		releases map[string]string
		tags     []string
		fail     bool
		pages    map[string]GoAnnouncement
		queries  []string
		want     []GoAnnouncement
		wantErr  bool
	}{
		{
			name:     "paired security release",
			versions: []string{"1.22.4-1", "1.21.11-1"},
			security: true,
			pages:    map[string]GoAnnouncement{"1.22.4": paired, "1.21.11": paired},
			queries:  []string{"1.22.4", "1.21.11"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "single security release with paired announcement",
			versions: []string{"1.22.4-1"},
			security: true,
			pages:    map[string]GoAnnouncement{"1.22.4": paired},
			queries:  []string{"1.22.4"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "previous version with paired announcement",
			versions: []string{"1.21.11-1"},
			pages:    map[string]GoAnnouncement{"1.21.11": paired},
			queries:  []string{"1.21.11"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "three versions and two distinct announcements",
			versions: []string{"1.23.1-1", "1.22.4-1", "1.21.11-1"},
			pages:    map[string]GoAnnouncement{"1.23.1": newer, "1.22.4": paired, "1.21.11": paired},
			queries:  []string{"1.23.1", "1.22.4", "1.21.11"},
			want:     []GoAnnouncement{newer, paired},
		},
		{
			name:     "only the third version has an announcement",
			versions: []string{"1.23.1-1", "1.22.4-1", "1.21.11-1"},
			pages:    map[string]GoAnnouncement{"1.21.11": older},
			queries:  []string{"1.23.1", "1.22.4", "1.21.11"},
			want:     []GoAnnouncement{older},
			wantErr:  true,
		},
		{
			name:     "revision two without a published revision one",
			versions: []string{"1.22.4-2"},
			tags:     []string{"v1.22.4-1"},
			pages:    map[string]GoAnnouncement{"1.22.4": paired},
			queries:  []string{"1.22.4"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "draft revision one does not suppress the announcement",
			versions: []string{"1.22.4-2"},
			releases: map[string]string{"v1.22.4-1": `{"draft":true}`},
			tags:     []string{"v1.22.4-1"},
			pages:    map[string]GoAnnouncement{"1.22.4": paired},
			queries:  []string{"1.22.4"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "revision two after a published revision one",
			versions: []string{"1.22.4-2"},
			releases: map[string]string{"v1.22.4-1": `{}`},
			tags:     []string{"v1.22.4-1"},
		},
		{
			name:     "skip abandoned revisions and stop at a published release",
			versions: []string{"1.22.4-3", "1.21.11-3"},
			releases: map[string]string{"v1.22.4-1": `{}`, "v1.21.11-2": `{}`},
			tags:     []string{"v1.22.4-2", "v1.22.4-1", "v1.21.11-2"},
		},
		{
			name:     "earlier release of the same variant",
			versions: []string{"1.22.4-2-fips"},
			releases: map[string]string{"v1.22.4-1-fips": `{}`},
			tags:     []string{"v1.22.4-1-fips"},
		},
		{
			name:     "a different variant does not suppress the announcement",
			versions: []string{"1.22.4-2-fips"},
			releases: map[string]string{"v1.22.4-1": `{}`},
			tags:     []string{"v1.22.4-1-fips"},
			pages:    map[string]GoAnnouncement{"1.22.4": paired},
			queries:  []string{"1.22.4"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "earlier release lookup fails but retains other announcements",
			versions: []string{"1.23.1-1", "1.22.4-2"},
			tags:     []string{"v1.22.4-1"},
			fail:     true,
			pages:    map[string]GoAnnouncement{"1.23.1": newer},
			queries:  []string{"1.23.1"},
			want:     []GoAnnouncement{newer},
			wantErr:  true,
		},
		{
			name:     "Microsoft security fix for a regular upstream release",
			versions: []string{"1.23.1-2"},
			security: true,
			tags:     []string{"v1.23.1-1"},
			pages:    map[string]GoAnnouncement{"1.23.1": newer},
			queries:  []string{"1.23.1"},
			want:     []GoAnnouncement{newer},
		},
		{
			name:     "revisions and variants share one lookup",
			versions: []string{"1.22.4-2", "1.22.4-1", "1.22.4-1-fips"},
			tags:     []string{"v1.22.4-1"},
			pages:    map[string]GoAnnouncement{"1.22.4": paired},
			queries:  []string{"1.22.4"},
			want:     []GoAnnouncement{paired},
		},
		{
			name:     "no matching announcement",
			versions: []string{"1.22.4-1"},
			queries:  []string{"1.22.4"},
			wantErr:  true,
		},
		{
			name:    "no upstream versions",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releaseInfo := NewReleaseInfo(
				time.Date(2024, 6, 4, 0, 0, 0, 0, time.UTC),
				tt.versions,
				"test",
				tt.security,
			)
			var queries []string
			var releaseQueries []string
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.github.com" {
					tag, ok := strings.CutPrefix(req.URL.Path, "/repos/microsoft/go/releases/tags/")
					if !ok {
						return nil, fmt.Errorf("unexpected release request: %s", req.URL)
					}
					releaseQueries = append(releaseQueries, tag)
					status := http.StatusOK
					body, found := tt.releases[tag]
					if tt.fail {
						status = http.StatusInternalServerError
						body = `{"message":"release lookup failed"}`
					} else if !found {
						status = http.StatusNotFound
						body = `{"message":"Not Found"}`
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}
				if req.URL.Host != "groups.google.com" {
					return nil, fmt.Errorf("unexpected announcement request: %s", req.URL)
				}
				query := req.URL.Query().Get("q")
				version, ok := strings.CutPrefix(query, `subject:"Go `)
				if !ok {
					return nil, fmt.Errorf("unexpected search query %q", query)
				}
				version, ok = strings.CutSuffix(version, `"`)
				if !ok {
					return nil, fmt.Errorf("unexpected search query %q", query)
				}
				queries = append(queries, version)
				announcement, found := tt.pages[version]
				body := "<html><body></body></html>"
				if found {
					body = fmt.Sprintf(`<html><head><base href="https://groups.google.com/"></head><body><a href=%q>%s</a></body></html>`,
						announcement.URL, announcement.Subject)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})}

			githubClient, err := github.NewClient(github.WithHTTPClient(client))
			if err != nil {
				t.Fatal(err)
			}
			got, err := findGoAnnouncements(context.Background(), releaseInfo.Versions, githubClient, client)
			if (err != nil) != tt.wantErr {
				t.Fatalf("findGoAnnouncements() error = %v, want error %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("findGoAnnouncements() = %+v, want %+v", got, tt.want)
			}
			if !slices.Equal(queries, tt.queries) {
				t.Errorf("queried versions = %v, want %v", queries, tt.queries)
			}
			if !slices.Equal(releaseQueries, tt.tags) {
				t.Errorf("queried releases = %v, want %v", releaseQueries, tt.tags)
			}

			releaseInfo.GoAnnouncements = got
			var announcement bytes.Buffer
			if err := releaseInfo.WriteAnnouncement(&announcement); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				label := want.Subject
				if len(tt.want) == 1 {
					label = "Go release announcement"
				}
				if link := "[" + label + "](" + want.URL + ")"; !strings.Contains(announcement.String(), link) {
					t.Errorf("announcement is missing %q:\n%s", link, announcement.String())
				}
				if count := strings.Count(announcement.String(), want.URL); count != 1 {
					t.Errorf("announcement contains URL %q %d times, want once", want.URL, count)
				}
			}
			if len(tt.want) == 0 && strings.Contains(announcement.String(), "groups.google.com") {
				t.Errorf("announcement contains an unexpected Google Groups link:\n%s", announcement.String())
			}
		})
	}
}

func TestMatchesGoAnnouncementSubject(t *testing.T) {
	tests := []struct {
		subject string
		version string
		want    bool
	}{
		{"Go 1.22.4 is released", "1.22.4", true},
		{"[security] Go 1.22.4 is released", "1.22.4", true},
		{"Go 1.22.4 and Go 1.21.11 are released", "1.21.11", true},
		{"[security] Go 1.22.4 and Go 1.21.11 are released", "1.22.4", true},
		{"Go 1.22.40 is released", "1.22.4", false},
		{"Go 1.22.4 is released", "", false},
		{"Go 1.22.4 are released", "1.22.4", false},
		{"Go 1.22.4 and Go 1.21.11 is released", "1.22.4", false},
		{"Re: Go 1.22.4 is released", "1.22.4", false},
	}
	for _, tt := range tests {
		t.Run(tt.subject+"/"+tt.version, func(t *testing.T) {
			if got := matchesGoAnnouncementSubject(tt.subject, tt.version); got != tt.want {
				t.Errorf("matchesGoAnnouncementSubject(%q, %q) = %v, want %v", tt.subject, tt.version, got, tt.want)
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
