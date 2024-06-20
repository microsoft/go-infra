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
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/microsoft/go-infra/githubutil"
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
		Categories: []string{"Microsoft for Go Developers"},
		Tags:       []string{"go", "release"},
	}
}

type GoVersionData struct {
	MSGoVersion     string
	MSGoVersionLink string
	GoVersion       string
	GoVersionLink   string
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

func (r *ReleaseInfo) SetTitle(versions []string) {
	r.Title = generateBlogPostTitle(versions)
	r.Slug = generateSlug(r.Title)
}

func (r *ReleaseInfo) SetAuthor(author string) {
	r.Author = mapUsernames(author)
}

func (r *ReleaseInfo) IsSecurityRelease(IsSecurityRelease bool) {
	if IsSecurityRelease {
		r.Categories = append(r.Categories, "Security")
		r.Tags = append(r.Tags, "security")
	}
}

func (r *ReleaseInfo) WriteAnnouncement(wr io.Writer) error {
	tmpl, err := template.New("announcement.template.md").Parse(announcementTemplate)
	if err != nil {
		return err
	}

	return tmpl.Execute(wr, r)
}

//go:embed templates/announcement.template.md
var announcementTemplate string

func publishAnnouncement(p subcmd.ParseFunc) (err error) {
	releaseInfo := NewReleaseInfo()

	var releaseDateStr string
	var releaseVersions string
	var author string
	var security bool
	var test bool

	flag.StringVar(&releaseDateStr, "release-date", "", "The release date of the Microsoft Go version in YYYY-MM-DD format.")
	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release.")
	flag.StringVar(&author, "author", "", "GitHub username of the author of the blog post. This will be used to attribute the post to the correct author in WordPress.")
	flag.BoolVar(&security, "security", false, "Specify if the release is a security release. Use this flag to mark the release as a security update. Defaults to false.")
	flag.BoolVar(&test, "test", false, "Test the announcement template. This will output the generated announcement to stdout.")
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

	releaseInfo.SetTitle(versionsList)
	releaseInfo.ParseGoVersions(versionsList)
	releaseInfo.SetAuthor(author)
	releaseInfo.IsSecurityRelease(security)

	ctx := context.Background()
	client, err := githubutil.NewClient(ctx, *pat)
	if err != nil {
		return err
	}

	if test {
		return releaseInfo.WriteAnnouncement(os.Stdout)
	}

	content := new(bytes.Buffer)
	if err := releaseInfo.WriteAnnouncement(content); err != nil {
		return fmt.Errorf("error executing template: %w", err)
	}

	blogFilePath := generateBlogFilePath(releaseDate, releaseInfo.Slug)

	// check if the file already exists in the go-devblog repository
	exists, err := githubutil.FileExists(ctx, client, "microsoft", "go-devblog", blogFilePath)
	if err != nil {
		return fmt.Errorf("error checking if file exists in go-devblog repository : %w", err)
	}
	if exists {
		return fmt.Errorf("file %s already exists in go-devblog repository", blogFilePath)
	}

	// Upload the announcement to the go-devblog repository
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
		return fmt.Sprintf("Go %s and %s Microsoft builds now available", allExceptLast, versions[count-1])
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
	// add github username and wordpress username in case they are different
	usernames := map[string]string{
		"gdams":   "gadams",
		"qmuntal": "qmuntaldiaz",
	}

	if wordpressUsername, exists := usernames[githubUsername]; exists {
		return wordpressUsername
	}

	return githubUsername
}

func generateBlogFilePath(releaseDate time.Time, slug string) string {
	return fmt.Sprintf("%d/%s/%s.md", releaseDate.Year(), releaseDate.Month().String(), slug)
}
