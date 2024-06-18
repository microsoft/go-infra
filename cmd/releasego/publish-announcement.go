// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "publish-announcement",
		Summary: "Generate an markdown formatted blog post for Go version releases and publish it to go-devblog repo.",
		Description: `
The 'publish-announcement' command is used to create an HTML formatted 
blog post for the Microsoft builds of Go security patch releases. This command requires 
the release date, the list of released versions, and a release label. It outputs an 
HTML formatted blog post that includes the release date, the versions released, 
and a link to the upstream Go announcement.
`,
		Handle: publishAnnouncement,
	})
}

type ReleaseInfo struct {
	Title         string
	Author        string
	Slug          string
	Categories    []string
	Tags          []string
	ReleaseDate   string
	FeaturedImage string // Add this field for featured_image
	Versions      []GoVersionData
}

func (ri ReleaseInfo) CategoriesString() string {
	return strings.Join(ri.Categories, ", ")
}

func (ri ReleaseInfo) TagsString() string {
	return strings.Join(ri.Tags, ", ")
}

func NewReleaseInfo() *ReleaseInfo {
	return &ReleaseInfo{
		Categories: []string{"Microsoft for Go Developers", "Security"},
		Tags:       []string{"go", "release", "security"},
	}
}

type GoVersionData struct {
	MSGoVersion     string
	MSGoVersionLink string
	GoVersion       string
	GoVersionLink   string
}

func (r *ReleaseInfo) SetReleaseDate(dateStr string) error {
	const inputLayout = "2006-01-02"
	if dateStr == "" {
		return errors.New("release date cannot be empty")
	}

	parsedTime, err := time.Parse(inputLayout, dateStr)
	if err != nil {
		return fmt.Errorf("invalid date format for release date %q: %w", dateStr, err)
	}
	r.ReleaseDate = parsedTime.Format("January 2")

	return nil
}

func (r *ReleaseInfo) ParseGoVersions(goVersions []string) {
	for _, version := range goVersions {
		goVersion := goversion.New(version).UpstreamFormatGitTag()
		r.Versions = append(r.Versions, GoVersionData{
			MSGoVersion:     "v" + version,
			MSGoVersionLink: createMSGoReleaseLinkFromVersion(version),
			GoVersion:       goVersion,
			GoVersionLink:   createGoReleaseLinkFromVersion(goVersion),
		})
	}
}

type GoVersion struct {
	URL     string
	Version string
}

//go:embed templates/announcement.template.md
var announcementTemplate string

func publishAnnouncement(p subcmd.ParseFunc) (err error) {
	releaseInfo := NewReleaseInfo()
	var releaseDate string
	var releaseVersions string
	var tags string

	flag.StringVar(&releaseDate, "release-date", "", "The release date of the Go version in YYYY-MM-DD format.")
	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release.")
	flag.StringVar(&tags, "tags", "", "Comma-separated list of tags for the Go release.")

	if err := p(); err != nil {
		return err
	}

	if err := releaseInfo.SetReleaseDate(releaseDate); err != nil {
		return fmt.Errorf("failed to set release date: %w", err)
	}
	releaseInfo.ParseGoVersions(strings.Split(releaseVersions, ","))

	var output io.WriteCloser = os.Stdout

	tmpl, err := template.New("announcement.template.md").Parse(announcementTemplate)
	if err != nil {
		return err
	}

	if err := tmpl.Execute(output, releaseInfo); err != nil {
		return err
	}

	return nil
}

func generateSlug(input string) string {
	// Convert to lowercase
	input = strings.ToLower(input)

	// Replace spaces and punctuation with hyphens
	result := strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return '-'
		}
		return r
	}, input)

	// Remove any remaining non-alphanumeric characters (excluding hyphens)
	result = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(result, "")

	// Remove multiple consecutive hyphens
	result = regexp.MustCompile(`-+`).ReplaceAllString(result, "-")

	// Trim hyphens from start and end
	result = strings.Trim(result, "-")

	return result
}

func createMSGoReleaseLinkFromVersion(releaseID string) string {
	return "https://github.com/microsoft/go/releases/tag/v" + releaseID
}

func createGoReleaseLinkFromVersion(releaseID string) string {
	return "https://go.dev/doc/devel/release#" + releaseID
}
