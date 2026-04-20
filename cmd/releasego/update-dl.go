// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/internal/infrasort"
	"github.com/microsoft/go-infra/subcmd"
)

var downloadHTTPClient = http.Client{Timeout: 30 * time.Second}

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-dl",
		Summary: "Add dl packages to the go-lab repository for new Go releases.",
		Description: `
The update-dl command generates dl/msgo<version>/main.go files for each specified
Go release version and creates a pull request on the go-lab repository. It fetches
the assets.json SHA256 hash from the GitHub release.
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
	releaseVersions := flag.String("versions", "", "Comma-separated list of version numbers for the Go release (e.g. 1.25.8-1,1.26.1-1).")
	dryRun := flag.Bool("n", false, "Enable dry run: do not push changes to GitHub.")
	dlRepo := flag.String("repo", "microsoft/go-lab", "The GitHub repository for the dl packages, in '{owner}/{repo}' form.")
	goRepo := flag.String("go-repo", "microsoft/go", "The GitHub repository for Go releases, in '{owner}/{repo}' form.")
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	gitHubReviewerAuthFlags := githubutil.BindGitHubAuthFlags("reviewer")

	if err := p(); err != nil {
		return err
	}

	if *releaseVersions == "" {
		return fmt.Errorf("no versions specified; use -versions flag")
	}

	labOwner, labName, err := githubutil.ParseRepoFlag(dlRepo)
	if err != nil {
		return fmt.Errorf("invalid -repo: %w", err)
	}
	goOwner, goName, err := githubutil.ParseRepoFlag(goRepo)
	if err != nil {
		return fmt.Errorf("invalid -go-repo: %w", err)
	}

	ctx := context.Background()

	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}

	// Parse and validate versions, fetching the SHA256 for each from the release's assets.json.
	rawVersions := strings.Split(*releaseVersions, ",")
	dlVersions := make([]dlVersionData, 0, len(rawVersions))
	for _, v := range rawVersions {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		gv := goversion.New(v)
		if gv.Major == "" || gv.Minor == "" {
			return fmt.Errorf("invalid version string: %q", v)
		}

		version := gv.Full()
		log.Printf("Fetching assets.json SHA256 for version %s...\n", version)
		assetsJSONSHA256, err := fetchAssetsJSONSHA256(ctx, client, goOwner, goName, "v"+version)
		if err != nil {
			return fmt.Errorf("error fetching assets.json SHA256 for version %s: %w", version, err)
		}
		log.Printf("assets.json SHA256 for %s: %s\n", version, assetsJSONSHA256)
		dlVersions = append(dlVersions, dlVersionData{
			Version: version,
			SHA256:  assetsJSONSHA256,
		})
	}
	if len(dlVersions) == 0 {
		return fmt.Errorf("no valid versions found in -versions flag")
	}
	// Sort descending so PR title lists versions in a consistent order.
	sort.Slice(dlVersions, func(i, j int) bool {
		return infrasort.GoVersionLess(goversion.New(dlVersions[i].Version), goversion.New(dlVersions[j].Version))
	})

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

	if *dryRun {
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

	// Check that none of the files already exist.
	refFS := githubutil.NewRefFS(ctx, client, labOwner, labName, "main")
	filePaths := make([]string, len(files))
	for i, f := range files {
		filePaths[i] = f.path
	}
	if err := checkDLFilesNotExist(refFS, filePaths); err != nil {
		return err
	}

	// Generate the PR title.
	versionStrings := make([]string, 0, len(dlVersions))
	for _, dv := range dlVersions {
		versionStrings = append(versionStrings, dv.Version)
	}
	title := generateDLPRTitle(versionStrings)

	prBody := "**Automated Pull Request:** Adds dl packages for new Microsoft build of Go releases.\n" +
		"This PR was generated automatically using the [`update-dl.go`](https://github.com/microsoft/go-infra/blob/main/cmd/releasego/update-dl.go) script."

	// Use the tree API to create a single commit with all files, avoiding noisy history
	// and simplifying retries. If anything fails, retry from the beginning with a fresh base.
	slug := "msgo-" + strings.Join(versionStrings, "-")
	branchName := "dev/dl/" + slug + "/" + fmt.Sprintf("%d", time.Now().Unix())

	var pr *github.PullRequest
	if err := githubutil.Retry(func() error {
		// Get the base branch ref and commit to pin the tree base.
		baseRef, _, err := client.Git.GetRef(ctx, labOwner, labName, "heads/main")
		if err != nil {
			return fmt.Errorf("error getting main ref: %w", err)
		}
		baseCommitSHA := baseRef.Object.GetSHA()
		baseCommit, _, err := client.Git.GetCommit(ctx, labOwner, labName, baseCommitSHA)
		if err != nil {
			return fmt.Errorf("error getting commit %s: %w", baseCommitSHA, err)
		}

		// Build tree entries for all files.
		treeEntries := make([]*github.TreeEntry, 0, len(files))
		for _, f := range files {
			treeEntries = append(treeEntries, &github.TreeEntry{
				Path:    github.String(f.path),
				Content: github.String(string(f.content)),
				Mode:    github.String(githubutil.TreeModeFile),
			})
		}

		createTree, _, err := client.Git.CreateTree(ctx, labOwner, labName, baseCommit.Tree.GetSHA(), treeEntries)
		if err != nil {
			return fmt.Errorf("error creating tree: %w", err)
		}

		createCommit, _, err := client.Git.CreateCommit(ctx, labOwner, labName, &github.Commit{
			Message: github.String(title),
			Parents: []*github.Commit{baseCommit},
			Tree:    createTree,
		}, &github.CreateCommitOptions{})
		if err != nil {
			return fmt.Errorf("error creating commit: %w", err)
		}

		newRef := &github.Reference{
			Ref:    github.String("refs/heads/" + branchName),
			Object: &github.GitObject{SHA: createCommit.SHA},
		}
		if _, _, err = client.Git.CreateRef(ctx, labOwner, labName, newRef); err != nil {
			return fmt.Errorf("error creating ref %s: %w", branchName, err)
		}
		// Ref is created; can't retry from here (name is taken).
		return nil
	}); err != nil {
		return err
	}

	// Create the pull request.
	if err := githubutil.Retry(func() error {
		pr, _, err = client.PullRequests.Create(ctx, labOwner, labName, &github.NewPullRequest{
			Title: github.String(title),
			Head:  github.String(branchName),
			Base:  github.String("main"),
			Body:  github.String(prBody),
		})
		if err != nil {
			return fmt.Errorf("error creating pull request: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	log.Printf("Pull request created: %s\n", pr.GetHTMLURL())

	reviewAuther, err := gitHubReviewerAuthFlags.NewAuther()
	if err != nil {
		return fmt.Errorf("failed to get GitHub review auther: %w", err)
	}

	if err := githubutil.Retry(func() error {
		return gitpr.EnablePRAutoMerge(pr.GetNodeID(), reviewAuther)
	}); err != nil {
		return err
	}

	if err := githubutil.Retry(func() error {
		return gitpr.ApprovePR(pr.GetNodeID(), reviewAuther)
	}); err != nil {
		return err
	}

	return nil
}

// fetchAssetsJSONSHA256 downloads the assets.json from a GitHub release and returns its content SHA256.
// The dl tool in go-lab uses this hash to verify the integrity of assets.json before extracting
// platform-specific download URLs and hashes from it.
func fetchAssetsJSONSHA256(ctx context.Context, client *github.Client, owner, repo, tag string) (string, error) {
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
	var rc io.ReadCloser
	if err := githubutil.Retry(func() error {
		var err error
		rc, _, err = client.Repositories.DownloadReleaseAsset(ctx, owner, repo, assetsAsset.GetID(), &downloadHTTPClient)
		return err
	}); err != nil {
		return "", fmt.Errorf("error downloading assets.json from release %s: %w", tag, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("error reading assets.json: %w", err)
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:]), nil
}

// dlFilePath returns the path for a dl package's main.go file within the go-lab repo.
func dlFilePath(version string) string {
	return fmt.Sprintf("dl/msgo%s/main.go", version)
}

// checkDLFilesNotExist checks that none of the given file paths already exist in the filesystem.
// fsys can be a githubutil.NewRefFS (remote GitHub) or os.DirFS (local).
func checkDLFilesNotExist(fsys githubutil.SimplifiedFS, paths []string) error {
	for _, path := range paths {
		_, err := fsys.ReadFile(path)
		if err == nil {
			return fmt.Errorf("file %s already exists", path)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("error checking if file %s exists: %w", path, err)
		}
	}
	return nil
}

// generateDLPRTitle generates a human-readable PR title for the dl update.
func generateDLPRTitle(versions []string) string {
	return "Update dl for Go " + strings.Join(versions, ", ")
}
