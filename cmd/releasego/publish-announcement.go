// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/internal/infrasort"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "publish-announcement",
		Summary: "Generate a markdown-formatted blog post for Go version releases and publish it to the go-devblog repo.",
		Description: `
The publish-announcement command automates the creation of Markdown-formatted blog 
posts for Microsoft's Go releases. It generates a post containing release details, 
relevant links, and metadata, then commits it to the go-devblog repository for 
subsequent drafting as a WordPress article.
`,
		Handle: publishAnnouncement,
	})
}

// add github username and wordpress username in case they are different
var githubToWordpressUsernames = map[string]string{
	"gdams":   "gadams",
	"qmuntal": "qmuntaldiaz",
}

type ReleaseInfo struct {
	Title           string
	Author          string
	Slug            string
	Categories      []string
	Tags            []string
	FeaturedImage   string // Add this field for featured_image
	Versions        []GoVersionData
	SecurityRelease bool
}

// take all this methods and make it one constructor function for ReleaseInfo
func NewReleaseInfo(releaseDate time.Time, versions []string, author string, security bool) *ReleaseInfo {
	ri := new(ReleaseInfo)
	goVersions := make(infrasort.GoVersions, 0)
	for _, version := range versions {
		goVersions = append(goVersions, goversion.New(version))
	}

	// Sort the versions in descending order
	sort.Sort(goVersions)

	// Generate human-readable title and URL-friendly slug from the Go versions.
	ri.Title = generateBlogPostTitle(versions)
	ri.Slug = generateSlug(ri.Title)

	// Set default categories and tags for the blog post.
	ri.Categories = []string{"Microsoft for Go Developers"}
	ri.Tags = []string{"go", "release"}

	// Process each Go version, extracting release information and generating links.
	for _, goVersion := range goVersions {
		ri.Versions = append(ri.Versions, GoVersionData{
			MSGoVersion:     "v" + goVersion.MajorMinorPatchPrereleaseRevision(),
			MSGoVersionLink: createMSGoReleaseLinkFromVersion(goVersion.MajorMinorPatchPrereleaseRevision()),
			GoVersion:       goVersion.UpstreamFormatGitTag(),
			GoVersionLink:   createGoReleaseLinkFromVersion(goVersion.UpstreamFormatGitTag()),
		})
	}

	// Map the author's username to the appropriate format for the blog post.
	ri.Author = mapUsernames(author)

	// If this is a security release, add the "Security" category and tag to the post.
	if security {
		ri.Categories = append(ri.Categories, "Security")
		ri.Tags = append(ri.Tags, "security")
		ri.SecurityRelease = true
	}

	return ri
}

func (ri ReleaseInfo) CategoriesString() string {
	return strings.Join(ri.Categories, ", ")
}

func (ri ReleaseInfo) TagsString() string {
	return strings.Join(ri.Tags, ", ")
}

type GoVersionData struct {
	MSGoVersion     string
	MSGoVersionLink string
	GoVersion       string
	GoVersionLink   string
}

func (r *ReleaseInfo) WriteAnnouncement(wr io.Writer) error {
	tmpl, err := template.New("announcement.template.md").Parse(announcementTemplate)
	if err != nil {
		return fmt.Errorf("error parsing announcement template: %w", err)
	}

	if err := tmpl.Execute(wr, r); err != nil {
		return fmt.Errorf("error executing announcement template: %w", err)
	}

	return nil
}

//go:embed templates/announcement.template.md
var announcementTemplate string

func publishAnnouncement(p subcmd.ParseFunc) (err error) {
	var releaseDateStr string
	var releaseVersions string
	var author string
	var security bool
	var dryRun bool

	flag.StringVar(&releaseDateStr, "release-date", "", "The release date of the Microsoft Go version in YYYY-MM-DD format.")
	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release.")
	flag.StringVar(&author, "author", "", "GitHub username of the author of the blog post. This will be used to attribute the post to the correct author in WordPress.")
	flag.BoolVar(&security, "security", false, "Specify if the release is a security release. Use this flag to mark the release as a security update. Defaults to false.")
	flag.BoolVar(&dryRun, "n", false, "Enable dry run: do not push blog post to GitHub.")
	pat := githubutil.BindPATFlag()

	if err := p(); err != nil {
		return err
	}

	const inputLayout = "2006-01-02"
	if releaseDateStr == "" {
		releaseDateStr = time.Now().Format(inputLayout)
	}

	releaseDate, err := time.Parse(inputLayout, releaseDateStr)
	if err != nil {
		return fmt.Errorf("invalid date format for release date %q: %w", releaseDateStr, err)
	}
	versionsList := strings.Split(releaseVersions, ",")

	releaseInfo := NewReleaseInfo(releaseDate, versionsList, author, security)

	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Printf("Would have submitted at path '%s'\n", generateBlogFilePath(releaseDate, releaseInfo.Slug))
		fmt.Println("=====")
		return releaseInfo.WriteAnnouncement(os.Stdout)
	}

	content := new(bytes.Buffer)
	if err := releaseInfo.WriteAnnouncement(content); err != nil {
		return err
	}

	blogFilePath := generateBlogFilePath(releaseDate, releaseInfo.Slug)

	if err := githubutil.Retry(func() error {
		// check if the file already exists in the go-devblog repository
		exists, err := githubutil.FileExists(ctx, client, "microsoft", "go-devblog", blogFilePath)
		if err != nil {
			return fmt.Errorf("error checking if file exists in go-devblog repository : %w", err)
		}
		if exists {
			return fmt.Errorf("file %q already exists in go-devblog repository", blogFilePath)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("error checking if file exists in go-devblog repository : %w", err)
	}

	if err := githubutil.Retry(func() error {
		// Upload the announcement to the go-devblog repositoryy main branch with proper commit message
		if err := githubutil.UploadFile(
			ctx,
			client,
			"microsoft",
			"go-devblog",
			"main",
			blogFilePath,
			fmt.Sprintf("Add new blog post for new release in %s", releaseDate.Format("2006-01-02")),
			content.Bytes()); err != nil {
			return fmt.Errorf("error uploading file to go-devblog repository : %w", err)
		}

		return nil
	}); err != nil {
		return fmt.Errorf("error checking if file exists in go-devblog repository : %w", err)
	}

	return nil
}

func generateBlogPostTitle(versions []string) string {
	count := len(versions)
	if count == 0 {
		return ""
	} else if count == 1 {
		return fmt.Sprintf("Go %s Microsoft build now available", versions[0])
	} else if count == 2 {
		return fmt.Sprintf("Go %s and %s Microsoft builds now available", versions[0], versions[1])
	} else {
		allExceptLast := strings.Join(versions[:count-1], ", ")
		return fmt.Sprintf("Go %s, and %s Microsoft builds now available", allExceptLast, versions[count-1])
	}
}

func generateSlug(input string) string {
	// Convert to lowercase
	input = strings.ToLower(input)

	// Replace spaces and punctuation with hyphens
	result := strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return '-'
		}

		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return -1
		}

		return r
	}, input)

	// Remove any remaining non-alphanumeric characters (excluding hyphens)
	result = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(result, "")

	// Remove multiple consecutive hyphens
	result = regexp.MustCompile(`-+`).ReplaceAllString(result, "-")

	// Add -- between version numbers like go-1-23-1-1 and 1-22-8-1
	re := regexp.MustCompile(`(\d+-\d+-\d+-\d+)-(\d+-\d+-\d+-\d+)`)
	result = re.ReplaceAllString(result, "$1--$2")

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

func mapUsernames(githubUsername string) string {
	if wordpressUsername, exists := githubToWordpressUsernames[githubUsername]; exists {
		return wordpressUsername
	}

	return githubUsername
}

func generateBlogFilePath(releaseDate time.Time, slug string) string {
	return fmt.Sprintf("%d/%s/%s.md", releaseDate.Year(), releaseDate.Month().String(), slug)
}
