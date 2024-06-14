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
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "generate-announcement",
		Summary: "Generate an HTML formatted blog post for Go version releases.",
		Description: `
The 'generate-announcement' command is used to create an HTML formatted 
blog post for the Microsoft builds of Go security patch releases. This command requires 
the release date, the list of released versions, and a release label. It outputs an 
HTML formatted blog post that includes the release date, the versions released, 
and a link to the upstream Go announcement.
`,
		Handle: generateAnnouncement,
	})
}

type ReleaseInfo struct {
	ReleaseDate  string
	MSGoVersions []string
	GoVersions   []GoVersion
}

func (r *ReleaseInfo) SetReleaseDate(dateStr string) error {
	const inputLayout = "02-01-2006"
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
	r.MSGoVersions = append(r.MSGoVersions, goVersions...)
	for _, version := range goVersions {
		version = truncateMSGoVersionTag(version)
		r.GoVersions = append(r.GoVersions, GoVersion{
			URL:     createGoReleaseLinkFromVersion(version),
			Version: version,
		})
	}
}

type GoVersion struct {
	URL     string
	Version string
}

//go:embed templates/announcement.template.html
var announcementTemplate string

func generateAnnouncement(p subcmd.ParseFunc) error {
	releaseInfo := new(ReleaseInfo)
	var releaseDate string
	var releaseVersions string
	var outputPath string

	flag.StringVar(&releaseDate, "release-date", "", "The release date of the Go version in DD-MM-YYYY format.")
	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release.")
	flag.StringVar(&outputPath, "o", "", "Comma-separated list of version numbers for the Go release.")
	if err := p(); err != nil {
		return err
	}

	if err := releaseInfo.SetReleaseDate(releaseDate); err != nil {
		return fmt.Errorf("failed to set release date: %w", err)
	}

	releaseInfo.ParseGoVersions(strings.Split(releaseVersions, ","))

	output, err := generateOutput(outputPath)
	if err != nil {
		return fmt.Errorf("error creating output file: %w", err)
	}
	defer output.Close()

	tmpl, err := template.New("announcement.template.html").Funcs(template.FuncMap{"isLast": isLast}).Parse(announcementTemplate)
	if err != nil {
		return err
	}

	if err := tmpl.Execute(output, releaseInfo); err != nil {
		return err
	}

	return nil
}

func generateOutput(path string) (io.WriteCloser, error) {
	if path == "" {
		return os.Stdout, nil
	}

	dirPath := filepath.Dir(path)
	if err := os.MkdirAll(dirPath, os.ModePerm); err != nil {
		return nil, err
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create file at path %q: %w", path, err)
	}
	return file, nil
}

func createGoReleaseLinkFromVersion(releaseID string) string {
	return "https://go.dev/doc/devel/release#go" + releaseID
}

func truncateMSGoVersionTag(goVersion string) string {
	parts := strings.Split(goVersion, "-")
	return parts[0]
}

func isLast(index int, versions []GoVersion) bool {
	return index == len(versions)-1
}
