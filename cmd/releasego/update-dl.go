// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/internal/infrasort"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-dl",
		Summary: "Add dl packages to the go-lab repository for new Go releases.",
		Description: `
The update-dl command generates dl/msgo<version>/main.go files for each specified
Go release version and creates a pull request on the go-lab repository. It fetches
the source archive SHA256 hash from the GitHub release's assets.json file.
`,
		Handle: updateDL,
	})
}

// dlVersionData holds the data needed to generate a dl package file.
type dlVersionData struct {
	Version string
	SHA256  string
}

//go:embed templates/dl.template.go.tmpl
var dlTemplate string

func updateDL(p subcmd.ParseFunc) error {
	var releaseVersions string
	var dryRun bool
	var org string
	var repo string
	var goOrg string
	var goRepo string

	flag.StringVar(&releaseVersions, "versions", "", "Comma-separated list of version numbers for the Go release (e.g. 1.25.8-1,1.26.1-1).")
	flag.BoolVar(&dryRun, "n", false, "Enable dry run: do not push changes to GitHub.")
	flag.StringVar(&org, "org", "microsoft", "The GitHub organization for the go-lab repository.")
	flag.StringVar(&repo, "repo", "go-lab", "The GitHub repository name for the dl packages.")
	flag.StringVar(&goOrg, "go-org", "microsoft", "The GitHub organization for the Go releases repository.")
	flag.StringVar(&goRepo, "go-repo", "go", "The GitHub repository name for Go releases.")
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	gitHubReviewerAuthFlags := githubutil.BindGitHubAuthFlags("reviewer")

	if err := p(); err != nil {
		return err
	}

	if releaseVersions == "" {
		return fmt.Errorf("no versions specified; use -versions flag")
	}

	ctx := context.Background()

	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	// Parse, validate, and sort versions in descending order.
	rawVersions := strings.Split(releaseVersions, ",")
	goVersions := make(infrasort.GoVersions, 0, len(rawVersions))
	for _, v := range rawVersions {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		gv := goversion.New(v)
		if gv.Major == "" || gv.Minor == "" {
			return fmt.Errorf("invalid version string: %q", v)
		}
		goVersions = append(goVersions, gv)
	}
	if len(goVersions) == 0 {
		return fmt.Errorf("no valid versions found in -versions flag")
	}
	sort.Sort(goVersions)

	// For each version, fetch the SHA256 from the release's assets.json.
	dlVersions := make([]dlVersionData, 0, len(goVersions))
	for _, gv := range goVersions {
		version := gv.Full()
		log.Printf("Fetching SHA256 for version %s...\n", version)
		sha256, err := fetchGoSrcSHA256(ctx, client, goOrg, goRepo, "v"+version)
		if err != nil {
			return fmt.Errorf("error fetching SHA256 for version %s: %w", version, err)
		}
		log.Printf("SHA256 for %s: %s\n", version, sha256)
		dlVersions = append(dlVersions, dlVersionData{
			Version: version,
			SHA256:  sha256,
		})
	}

	// Generate file contents from the template.
	tmpl, err := template.New("dl").Parse(dlTemplate)
	if err != nil {
		return fmt.Errorf("error parsing dl template: %w", err)
	}

	type fileEntry struct {
		path    string
		version string
		content []byte
	}
	files := make([]fileEntry, 0, len(dlVersions))

	for _, dv := range dlVersions {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, dv); err != nil {
			return fmt.Errorf("error executing dl template for version %s: %w", dv.Version, err)
		}
		filePath := dlFilePath(dv.Version)
		files = append(files, fileEntry{path: filePath, version: dv.Version, content: buf.Bytes()})
	}

	if dryRun {
		for _, f := range files {
			fmt.Printf("Would create %s\n", f.path)
			fmt.Println("=====")
			if _, err := os.Stdout.Write(f.content); err != nil {
				return err
			}
			fmt.Println("=====")
		}
		return nil
	}

	auther, err := gitHubAuthFlags.NewAuther()
	if err != nil {
		return fmt.Errorf("failed to get GitHub auther: %w", err)
	}

	reviewAuther, err := gitHubReviewerAuthFlags.NewAuther()
	if err != nil {
		return fmt.Errorf("failed to get GitHub review auther: %w", err)
	}

	// Check that none of the files already exist.
	for _, f := range files {
		if _, err := githubutil.DownloadFile(ctx, client, org, repo, "main", f.path); err != nil {
			if errors.Is(err, githubutil.ErrFileNotExists) {
				// Good.
			} else {
				return fmt.Errorf("error checking if file %s exists: %w", f.path, err)
			}
		} else {
			return fmt.Errorf("file %s already exists in %s/%s", f.path, org, repo)
		}
	}

	// Generate the PR title.
	versionStrings := make([]string, 0, len(dlVersions))
	for _, dv := range dlVersions {
		versionStrings = append(versionStrings, dv.Version)
	}
	title := generateDLPRTitle(versionStrings)

	// Create a feature branch and upload all files.
	slug := "msgo-" + strings.Join(versionStrings, "-")
	prSet := gitpr.PRRefSet{Name: "main", Purpose: fmt.Sprintf("dl/%s/%d", slug, time.Now().Unix())}
	branchName := prSet.PRBranch()

	if err := githubutil.CreateBranch(ctx, client, org, repo, branchName, "main"); err != nil {
		return fmt.Errorf("error creating branch %s: %w", branchName, err)
	}

	for _, f := range files {
		log.Printf("Uploading %s...\n", f.path)
		if err := githubutil.UploadFile(
			ctx,
			client,
			org,
			repo,
			branchName,
			f.path,
			fmt.Sprintf("Add dl package: msgo%s", f.version),
			f.content,
		); err != nil {
			return fmt.Errorf("error uploading file %s to branch %s: %w", f.path, branchName, err)
		}
	}

	ownerRepo := fmt.Sprintf("%s/%s", org, repo)
	prReq := prSet.CreateGitHubPR(
		org,
		title,
		"**Automated Pull Request:** Adds dl packages for new Microsoft Go releases.\n"+
			"This PR was generated automatically using the [`update-dl.go`](https://github.com/microsoft/go-infra/blob/main/cmd/releasego/update-dl.go) script.")
	createdPR, err := gitpr.PostGitHub(ownerRepo, prReq, auther)
	if err != nil {
		return fmt.Errorf("error creating pull request with gitpr: %w", err)
	}

	if err = gitpr.EnablePRAutoMerge(createdPR.NodeID, reviewAuther); err != nil {
		return err
	}

	if err = gitpr.ApprovePR(createdPR.NodeID, reviewAuther); err != nil {
		return err
	}

	return nil
}

// fetchGoSrcSHA256 downloads the assets.json from a GitHub release and returns the GoSrcSHA256.
func fetchGoSrcSHA256(ctx context.Context, client *github.Client, owner, repo, tag string) (string, error) {
	var release *github.RepositoryRelease
	if err := githubutil.Retry(func() error {
		var err error
		release, _, err = client.Repositories.GetReleaseByTag(ctx, owner, repo, tag)
		return err
	}); err != nil {
		return "", fmt.Errorf("error getting release for tag %s: %w", tag, err)
	}

	// Find assets.json in the release assets.
	var assetsAsset *github.ReleaseAsset
	for i := range release.Assets {
		if release.Assets[i].GetName() == "assets.json" {
			assetsAsset = release.Assets[i]
			break
		}
	}
	if assetsAsset == nil {
		return "", fmt.Errorf("assets.json not found in release %s", tag)
	}

	// Download the assets.json file.
	downloadClient := &http.Client{Timeout: 30 * time.Second}
	var rc io.ReadCloser
	if err := githubutil.Retry(func() error {
		var err error
		rc, _, err = client.Repositories.DownloadReleaseAsset(ctx, owner, repo, assetsAsset.GetID(), downloadClient)
		return err
	}); err != nil {
		return "", fmt.Errorf("error downloading assets.json from release %s: %w", tag, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("error reading assets.json: %w", err)
	}

	var assets buildassets.BuildAssets
	if err := json.Unmarshal(data, &assets); err != nil {
		return "", fmt.Errorf("error parsing assets.json: %w", err)
	}

	if assets.GoSrcSHA256 == "" {
		return "", fmt.Errorf("GoSrcSHA256 is empty in assets.json for release %s", tag)
	}

	return assets.GoSrcSHA256, nil
}

// dlFilePath returns the path for a dl package's main.go file within the go-lab repo.
func dlFilePath(version string) string {
	return fmt.Sprintf("dl/msgo%s/main.go", version)
}

// generateDLPRTitle generates a human-readable PR title for the dl update.
func generateDLPRTitle(versions []string) string {
	count := len(versions)
	switch count {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("Add dl package for Go %s", versions[0])
	case 2:
		return fmt.Sprintf("Add dl packages for Go %s and %s", versions[0], versions[1])
	default:
		allExceptLast := strings.Join(versions[:count-1], ", ")
		return fmt.Sprintf("Add dl packages for Go %s, and %s", allExceptLast, versions[count-1])
	}
}
