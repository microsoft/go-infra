// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/goversion"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "get-upstream-commit",
		Summary: "Get the upstream commit for a given release version. Poll if not available.",
		Description: `

If the given version is an ordinary release, wait for the upstream Git tag. If it's a FIPS release,
wait until the boring releases file lists this release.

If the version contains a revision, it is ignored. The microsoft/go repository uses revision numbers
so it can release multiple times per upstream version. The build note must either be unspecified (to
indicate an ordinary release) or "fips" (to indicate the release is based on the boring branch).
`,
		Handle: handleWaitUpstream,
	})
}

func handleWaitUpstream(p subcmd.ParseFunc) error {
	version := flag.String("version", "", "[Required] A full or partial microsoft/go version number (major.minor.patch[-revision[-suffix]]).")
	upstream := flag.String("upstream", "https://go.googlesource.com/go", "The upstream Git repo to check.")
	azdoVarName := flag.String("set-azdo-variable", "", "An AzDO variable name to set to the commit hash using a logging command.")
	keepTemp := flag.Bool("w", false, "Keep the temporary repository used for polling, rather than cleaning it up.")
	pollDelaySeconds := flag.Int("poll-delay", 5, "Number of seconds to wait between each poll attempt.")

	if err := p(); err != nil {
		return err
	}

	if *version == "" {
		return errors.New("no version specified")
	}
	if *upstream == "" {
		return errors.New("no upstream specified")
	}
	pollDelay := time.Duration(*pollDelaySeconds) * time.Second

	gitDir, err := os.MkdirTemp("", "releasego-get-upstream-commit-*")
	if err != nil {
		return err
	}

	repo := gitRepo{gitDir, *upstream}
	v := goversion.New(*version)
	var checker upstreamChecker
	switch v.Note {
	case "":
		checker = &tagChecker{
			gitRepo: repo,
			Tag:     v.UpstreamFormatGitTag(),
		}
	case "fips":
		checker = &boringChecker{
			gitRepo: repo,
			Version: v.MajorMinorPatch(),
		}
	default:
		return fmt.Errorf("unable to check for version with note %q", v.Note)
	}

	if *keepTemp {
		log.Printf("Created dir %#q to store polling Git repo.\n", gitDir)
	} else {
		log.Printf("Created temp dir %#q to store polling Git repo. The dir will be deleted when the command completes.\n", gitDir)
		defer func() {
			if err := os.RemoveAll(gitDir); err != nil {
				log.Printf("Unable to clean up temp directory %#q: %v\n", gitDir, err)
			}
		}()
	}

	if err := executil.Run(exec.Command("git", "init", gitDir)); err != nil {
		return err
	}

	result := poll(checker, pollDelay)
	if *azdoVarName != "" {
		setAzDOPipelineVariable(*azdoVarName, result)
	}
	return nil
}

func poll(checker upstreamChecker, delay time.Duration) string {
	for {
		result, err := checker.Check()
		if err == nil {
			log.Printf("Check suceeded, result: %q.\n", result)
			return result
		}
		log.Printf("Failed check: %v, next poll in %v...", err, delay)
		time.Sleep(delay)
	}
}

// gitRepo wraps a Git repository directory and provides a few utility methods. By embedding this
// struct, a checker is able to use Git.
type gitRepo struct {
	GitDir   string
	Upstream string
}

func (g *gitRepo) gitCmd(args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	c.Dir = g.GitDir
	return c
}

func (g *gitRepo) runGitCmd(args ...string) error {
	return executil.Run(g.gitCmd(args...))
}

func (g *gitRepo) revParse(rev string) (string, error) {
	commit, err := executil.CombinedOutput(g.gitCmd("rev-parse", rev))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(commit), nil
}

// upstreamChecker checks an upstream repository for a release.
type upstreamChecker interface {
	// Check finds the commit associated with a release, or returns an error.
	Check() (string, error)
}

// tagChecker checks for an upstream release by looking for a Git tag.
type tagChecker struct {
	gitRepo
	Tag string
}

func (c *tagChecker) Check() (string, error) {
	if err := c.runGitCmd("fetch", "--depth", "1", c.Upstream, "refs/tags/"+c.Tag+":refs/tags/"+c.Tag); err != nil {
		return "", err
	}
	return c.revParse(c.Tag)
}

// boringChecker checks for an upstream release by reading the boring releases file and looking for
// a line that matches the given version.
type boringChecker struct {
	gitRepo
	Version string
}

func (c *boringChecker) Check() (string, error) {
	// Fetch the tip commit of the boring branch to check the RELEASES file.
	if err := c.runGitCmd("fetch", "--depth", "1", c.Upstream, "+dev.boringcrypto:boring"); err != nil {
		return "", err
	}

	// Find the boring release file content and find a matching release.
	releases, err := executil.CombinedOutput(c.gitCmd("show", "boring:misc/boring/RELEASES"))
	if err != nil {
		return "", err
	}
	shortCommit, err := c.findBoringReleaseCommit(releases)
	if err != nil {
		return "", err
	}

	// Fill in history to find the full commit hash.
	if err := c.runGitCmd("fetch", c.Upstream, "+refs/heads/dev.boringcrypto*:refs/heads/boring*"); err != nil {
		return "", err
	}
	return c.revParse(shortCommit)
}

// findBoringReleaseCommit finds a line in the RELEASES file that matches our tag, or returns an
// error. This method is lenient with unusual lines, skipping them.
func (c *boringChecker) findBoringReleaseCommit(content string) (string, error) {
	s := bufio.NewScanner(strings.NewReader(content))
	for s.Scan() {
		// Ignore comments.
		if strings.HasPrefix(s.Text(), "#") {
			continue
		}
		parts := strings.Fields(s.Text())
		if len(parts) < 3 {
			continue
		}
		version, shortCommit, platform := parts[0], parts[1], parts[2]
		// Each release has a "src" and "linux-amd64" line. (And more, in the future?) They're the
		// same as far as we care, but we're building from source, so might as well stick to src.
		if platform != "src" {
			continue
		}
		// Take the version part and remove "b7" or similar suffix to correspond to the Go version.
		i := strings.LastIndex(version, "b")
		if i == -1 {
			continue
		}
		version = version[0:i]

		if "go"+c.Version != version {
			continue
		}
		log.Printf("Found matching line: %v\n", s.Text())
		return shortCommit, nil
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("reached end of boring RELEASES file without finding target release %v", c.Version)
}
