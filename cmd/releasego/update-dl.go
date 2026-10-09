// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/gitpr"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/internal/infrasort"
	"github.com/microsoft/go-infra/subcmd"
)

var downloadHTTPClient = http.Client{Timeout: 30 * time.Second}

// updateDLRepo is replaceable so request-level tests don't need to run Git or Go.
var updateDLRepo = updateDLRepository

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "update-dl",
		Summary: "Add packages to the go-dl repository for new Go releases.",
		Description: `
The update-dl command downloads assets.json for each specified Go release,
adds the manifests to the go-dl database, regenerates the packages, and creates
a pull request on the go-dl repository.
`,
		Handle: updateDL,
	})
}

type dlRelease struct {
	Version    string
	AssetsJSON []byte
}

type dlRepoUpdate struct {
	RepoURL   string
	PushURL   string
	Branch    string
	Title     string
	Releases  []dlRelease
	DryRun    bool
	KeepClone bool
}

func updateDL(p subcmd.ParseFunc) error {
	releaseVersions := flag.String("versions", "", "Comma-separated list of version numbers for the Go release (e.g. 1.25.8-1,1.26.1-1).")
	dryRun := flag.Bool("n", false, "Enable dry run: clone, generate, and commit changes locally, but do not push or create a pull request.")
	keepTemp := flag.Bool("w", false, "Keep the temporary go-dl clone after the command exits.")
	dlRepo := flag.String("repo", "microsoft/go-dl", "The GitHub repository for the dl packages, in '{owner}/{repo}' form.")
	goRepo := flag.String("go-repo", "microsoft/go", "The GitHub repository for Go releases, in '{owner}/{repo}' form.")
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	gitHubReviewerAuthFlags := githubutil.BindGitHubAuthFlags("reviewer")

	if err := p(); err != nil {
		return err
	}

	if *releaseVersions == "" {
		return fmt.Errorf("no versions specified; use -versions flag")
	}

	dlOwner, dlName, err := githubutil.ParseRepoFlag(dlRepo)
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
		if !*dryRun || !errors.Is(err, githubutil.ErrNoAuthProvided) {
			return err
		}

		// Dry runs only read public release data and clone the public go-dl repository.
		client, err = github.NewClient()
		if err != nil {
			return fmt.Errorf("create unauthenticated GitHub client: %w", err)
		}
	}

	// Parse and validate versions, downloading the published manifest for each release.
	rawVersions := strings.Split(*releaseVersions, ",")
	releases := make([]dlRelease, 0, len(rawVersions))
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
		log.Printf("Fetching assets.json for version %s...\n", version)
		assetsJSON, err := fetchAssetsJSON(ctx, client, goOwner, goName, "v"+version)
		if err != nil {
			return fmt.Errorf("error fetching assets.json for version %s: %w", version, err)
		}
		releases = append(releases, dlRelease{
			Version:    version,
			AssetsJSON: assetsJSON,
		})
	}
	if len(releases) == 0 {
		return fmt.Errorf("no valid versions found in -versions flag")
	}

	// Keep titles and branch names stable regardless of the input order.
	sort.Slice(releases, func(i, j int) bool {
		return infrasort.GoVersionLess(goversion.New(releases[i].Version), goversion.New(releases[j].Version))
	})

	// Generate the commit and pull request title and a unique update branch.
	versionStrings := make([]string, 0, len(releases))
	for _, release := range releases {
		versionStrings = append(versionStrings, release.Version)
	}
	title := "Update dl for Microsoft build of Go " + strings.Join(versionStrings, ", ")
	branchName := "dev/dl/msgo-" + strings.Join(versionStrings, "-") + "/" + fmt.Sprintf("%d", time.Now().Unix())

	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", dlOwner, dlName)
	var pushURL string
	if !*dryRun {
		// Keep credentials out of the configured remote. The authenticated URL is used only by git push.
		auther, err := gitHubAuthFlags.NewAuther()
		if err != nil {
			return fmt.Errorf("failed to get GitHub auther: %w", err)
		}
		pushURL = auther.InsertAuth(repoURL)
	}

	// Clone go-dl, add the manifests, regenerate its packages, and commit the result.
	// A non-dry run also pushes the update branch.
	if err := updateDLRepo(dlRepoUpdate{
		RepoURL:   repoURL,
		PushURL:   pushURL,
		Branch:    branchName,
		Title:     title,
		Releases:  releases,
		DryRun:    *dryRun,
		KeepClone: *keepTemp,
	}); err != nil {
		return err
	}
	if *dryRun {
		return nil
	}

	// Create the pull request for the branch pushed above.
	prBody := "**Automated Pull Request:** Adds packages for new Microsoft builds of Go.\n" +
		"This PR was generated automatically using the [`update-dl.go`](https://github.com/microsoft/go-infra/blob/main/cmd/releasego/update-dl.go) script."

	var pr *github.PullRequest
	if err := githubutil.Retry(func() error {
		pr, _, err = client.PullRequests.Create(ctx, dlOwner, dlName, github.CreatePullRequest{
			Title: new(title),
			Head:  branchName,
			Base:  "main",
			Body:  new(prBody),
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

func fetchAssetsJSON(ctx context.Context, client *github.Client, owner, repo, tag string) ([]byte, error) {
	var release *github.RepositoryRelease
	if err := githubutil.Retry(func() error {
		var err error
		release, _, err = client.Repositories.GetReleaseByTag(ctx, owner, repo, tag)
		return err
	}); err != nil {
		return nil, fmt.Errorf("error getting release for tag %s: %w", tag, err)
	}

	var assetsAsset *github.ReleaseAsset
	for i := range release.Assets {
		if release.Assets[i].GetName() == "assets.json" {
			assetsAsset = release.Assets[i]
			break
		}
	}
	if assetsAsset == nil {
		return nil, fmt.Errorf("assets.json not found in release %s", tag)
	}

	var rc io.ReadCloser
	if err := githubutil.Retry(func() error {
		var err error
		rc, _, err = client.Repositories.DownloadReleaseAsset(ctx, owner, repo, assetsAsset.GetID(), &downloadHTTPClient)
		return err
	}); err != nil {
		return nil, fmt.Errorf("error downloading assets.json from release %s: %w", tag, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("error reading assets.json: %w", err)
	}
	return data, nil
}

func updateDLRepository(update dlRepoUpdate) error {
	repoDir, err := os.MkdirTemp("", "update-go-dl-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	if !update.KeepClone {
		defer gitcmd.AttemptDelete(repoDir)
	}
	log.Printf("Temporary go-dl clone: %s\n", repoDir)

	// Generation rewrites several directory trees, so use the repository's generator in a clone
	// rather than trying to reproduce its output with the GitHub tree API.
	if err := executil.Run(exec.Command("git", "clone", "--branch", "main", "--single-branch", update.RepoURL, repoDir)); err != nil {
		return fmt.Errorf("clone %s: %w", update.RepoURL, err)
	}
	if err := gitcmd.Run(repoDir, "checkout", "-b", update.Branch); err != nil {
		return fmt.Errorf("create branch %s: %w", update.Branch, err)
	}
	if err := writeDLManifests(repoDir, update.Releases); err != nil {
		return err
	}

	generateDir := filepath.Join(repoDir, "internal", "gen")
	if err := executil.Run(executil.Dir(generateDir, "go", "generate")); err != nil {
		return fmt.Errorf("generate go-dl packages: %w", err)
	}
	if err := gitcmd.Run(repoDir, "add", "--all"); err != nil {
		return fmt.Errorf("stage go-dl changes: %w", err)
	}
	if err := gitcmd.Run(repoDir, "diff", "--cached", "--quiet"); err == nil {
		return errors.New("go-dl generation produced no changes")
	} else if _, ok := err.(*exec.ExitError); !ok {
		return fmt.Errorf("check generated go-dl changes: %w", err)
	}
	if err := gitcmd.Run(repoDir, "commit", "-m", update.Title); err != nil {
		return fmt.Errorf("commit go-dl changes: %w", err)
	}
	if update.DryRun {
		fmt.Println("Dry run created this local commit:")
		if err := gitcmd.Run(repoDir, "show", "--stat", "--oneline", "HEAD"); err != nil {
			return fmt.Errorf("show generated go-dl commit: %w", err)
		}
		return nil
	}
	if err := gitcmd.Run(repoDir, "push", update.PushURL, "HEAD:refs/heads/"+update.Branch); err != nil {
		return fmt.Errorf("push go-dl changes: %w", err)
	}
	return nil
}

func writeDLManifests(repoDir string, releases []dlRelease) error {
	for _, release := range releases {
		path := filepath.Join(repoDir, dlManifestPath(release.Version))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create manifest directory for %s: %w", release.Version, err)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("manifest for %s already exists", release.Version)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check manifest for %s: %w", release.Version, err)
		}
		// Keep the published bytes unchanged. The database is the record of the release manifest.
		if err := os.WriteFile(path, release.AssetsJSON, 0o644); err != nil {
			return fmt.Errorf("write manifest for %s: %w", release.Version, err)
		}
	}
	return nil
}

func dlManifestPath(version string) string {
	series := goversion.New(version).MajorMinor()
	return filepath.Join("internal", "gen", "db", "go"+series, "go"+version+".assets.json")
}
