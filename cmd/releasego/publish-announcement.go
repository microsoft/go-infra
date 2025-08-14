// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
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
	"github.com/microsoft/go-infra/gitpr"
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

	// Recreate versions slice with sorted info.
	versions = versions[:0]
	for _, goVersion := range goVersions {
		versions = append(versions, goVersion.Full())
	}

	// Generate human-readable title and URL-friendly slug from the Go versions.
	ri.Title = generateBlogPostTitle(versions)
	ri.Slug = generateSlug(ri.Title)

	// Set default categories and tags for the blog post.
	ri.Categories = []string{"Microsoft for Go Developers"}
	ri.Tags = []string{"go", "release"}

	// Process each Go version, extracting release information and generating links.
	for _, goVersion := range goVersions {
		ri.Versions = append(ri.Versions, GoVersionData{
			MSGoVersion:     "v" + goVersion.Full(),
			MSGoVersionLink: createMSGoReleaseLinkFromVersion(goVersion.Full()),
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
	var org string
	var repo string

	flag.StringVar(&releaseDateStr, "release-date", "", "The release date of the Microsoft build of Go version in YYYY-MM-DD format.")
	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release.")
	flag.StringVar(&author, "author", "", "GitHub username of the author of the blog post. This will be used to attribute the post to the correct author in WordPress.")
	flag.BoolVar(&security, "security", false, "Specify if the release is a security release. Use this flag to mark the release as a security update. Defaults to false.")
	flag.BoolVar(&dryRun, "n", false, "Enable dry run: do not push blog post to GitHub.")
	flag.StringVar(&org, "org", "microsoft", "The GitHub organization to push the blog post to.")
	flag.StringVar(&repo, "repo", "go-devblog", "The GitHub repository name to push the blog post to.")
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")

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
	client, err := gitHubAuthFlags.NewClient(ctx)
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

	// check if the file already exists in the go-devblog repository
	if _, err := githubutil.DownloadFile(ctx, client, org, repo, "main", blogFilePath); err != nil {
		if errors.Is(err, githubutil.ErrFileNotExists) {
			// Good.
		} else {
			return fmt.Errorf("error checking if file exists in go-devblog repository : %w", err)
		}
	} else {
		return fmt.Errorf("file %s already exists in go-devblog repository", blogFilePath)
	}

	// Create a feature branch (gitpr convention: dev/<purpose>/<name>) and open a PR to main.
	prSet := gitpr.PRRefSet{Name: "main", Purpose: fmt.Sprintf("blog-%d", time.Now().Unix())}
	branchName := prSet.PRBranch()

	if err := githubutil.CreateBranch(ctx, client, org, repo, branchName, "main"); err != nil {
		return fmt.Errorf("error creating branch %s: %w", branchName, err)
	}

	if err := githubutil.Retry(func() error {
		if err := githubutil.UploadFile(
			ctx,
			client,
			org,
			repo,
			branchName,
			blogFilePath,
			fmt.Sprintf("Add blog post: %s", releaseInfo.Title),
			content.Bytes()); err != nil {
			return fmt.Errorf("error uploading file to branch %s: %w", branchName, err)
		}
		return nil
	}); err != nil {
		return err
	}

	// Create PR using gitpr.
	auther, err := gitHubAuthFlags.NewAuther()
	if err != nil {
		return fmt.Errorf("failed to get GitHub auther: %w", err)
	}
	ownerRepo := fmt.Sprintf("%s/%s", org, repo)
	prReq := prSet.CreateGitHubPR(org, releaseInfo.Title, "Automated PR: add Microsoft Go release announcement.")
	createdPR, err := gitpr.PostGitHub(ownerRepo, prReq, auther)
	if err != nil {
		return fmt.Errorf("error creating pull request with gitpr: %w", err)
	}

	if err = gitpr.ApprovePR(createdPR.NodeID, auther); err != nil {
		return err
	}

	if err = gitpr.EnablePRAutoMerge(createdPR.NodeID, auther); err != nil {
		return err
	}

	return nil
}

func generateBlogPostTitle(versions []string) string {
	count := len(versions)
	switch count {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("Go %s Microsoft build now available", versions[0])
	case 2:
		return fmt.Sprintf("Go %s and %s Microsoft builds now available", versions[0], versions[1])
	default:
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
	// E.g. "2024/01-January/announcing-go-1-23-1-1.md"
	return fmt.Sprintf(
		"%d/%02d-%s/%s.md",
		releaseDate.Year(), releaseDate.Month(), releaseDate.Month().String(), slug)
}
