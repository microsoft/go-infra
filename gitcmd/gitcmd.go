// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package gitcmd contains utilities for common Git operations in a local repository, including
// authentication with a remote repository.
package gitcmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/stringutil"
)

const (
	githubPrefix     = "https://github.com/"
	azdoDncengPrefix = "https://dnceng@dev.azure.com/"
)

// URLAuther manipulates a Git repository URL (GitHub, AzDO, ...) such that Git commands taking a
// remote will work with the URL. This is intentionally vague: it could add an access token into the
// URL, or it could simply make the URL compatible with environmental auth on the machine (SSH).
type URLAuther interface {
	// InsertAuth inserts authentication into the URL and returns it, or if the auther doesn't
	// apply, returns the url without any modifications.
	InsertAuth(url string) string
}

// GitHubSSHAuther turns an https-style GitHub URL into an SSH-style GitHub URL.
type GitHubSSHAuther struct{}

func (GitHubSSHAuther) InsertAuth(url string) string {
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("git@github.com:%v", after)
	}
	return url
}

// GitHubPATAuther adds a username and password into the https-style GitHub URL.
type GitHubPATAuther struct {
	User, PAT string
}

func (a GitHubPATAuther) InsertAuth(url string) string {
	if a.User == "" || a.PAT == "" {
		return url
	}
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("https://%v:%v@github.com/%v", a.User, a.PAT, after)
	}
	return url
}

// AzDOPATAuther adds a PAT into the https-style Azure DevOps repository URL.
type AzDOPATAuther struct {
	PAT string
}

func (a AzDOPATAuther) InsertAuth(url string) string {
	if a.PAT == "" {
		return url
	}
	if after, found := stringutil.CutPrefix(url, azdoDncengPrefix); found {
		url = fmt.Sprintf(
			// Username doesn't matter. PAT is identity.
			"https://arbitraryusername:%v@dev.azure.com/%v",
			a.PAT, after)
	}
	return url
}

// NoAuther does nothing to URLs.
type NoAuther struct{}

func (NoAuther) InsertAuth(url string) string {
	return url
}

// MultiAuther tries multiple authers in sequence. Stops and returns the result when any auther
// makes a change to the URL.
type MultiAuther struct {
	Authers []URLAuther
}

func (m MultiAuther) InsertAuth(url string) string {
	for _, a := range m.Authers {
		if authUrl := a.InsertAuth(url); authUrl != url {
			return authUrl
		}
	}
	return url
}

// Poll repeatedly checks using the given checker until it returns a successful result.
func Poll(checker PollChecker, delay time.Duration) string {
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

// PollChecker runs a check that returns a result. This is normally used to check an upstream
// repository for a release, or for go-images dependency flow status.
type PollChecker interface {
	// Check finds the string result associated with the check, or returns an error describing why
	// the result couldn't be found yet.
	Check() (string, error)
}

// CombinedOutput runs "git <args...>" in the given directory and returns the result.
func CombinedOutput(dir string, args ...string) (string, error) {
	return executil.CombinedOutput(executil.Dir(dir, "git", args...))
}

// RevParse runs "git rev-parse <rev>" and returns the result with whitespace trimmed.
func RevParse(dir, rev string) (string, error) {
	return executil.SpaceTrimmedCombinedOutput(executil.Dir(dir, "git", "rev-parse", rev))
}

// Show runs "git show <spec>" and returns the content.
func Show(dir, rev string) (string, error) {
	return CombinedOutput(dir, "show", rev)
}

// ShowQuietPretty runs "git show" with the given format and revision and returns the result.
// See https://git-scm.com/docs/git-show#_pretty_formats
func ShowQuietPretty(dir, format, rev string) (string, error) {
	return CombinedOutput(dir, "show", "--quiet", "--pretty=format:"+format, strings.TrimSpace(rev))
}

// FetchRefCommit effectively runs "git fetch <remote> <ref>" and returns the commit hash.
func FetchRefCommit(dir, remote, ref string) (string, error) {
	output, err := executil.CombinedOutput(executil.Dir(dir, "git", "fetch", remote, "--porcelain", ref))
	if err != nil {
		return "", err
	}
	// https://git-scm.com/docs/git-fetch#_output
	fields := strings.Fields(output)
	if len(fields) != 4 {
		return "", fmt.Errorf("unexpected number of fields in fetch output: %q", output)
	}
	return fields[2], nil
}

// Run runs "git <args>" in the given directory, showing the command to the user in logs for
// diagnosability. Using this func helps make one-line Git commands readable.
func Run(dir string, args ...string) error {
	return executil.Run(executil.Dir(dir, "git", args...))
}

// NewTempGitRepo creates a gitRepo in temp storage. If desired, clean it up with AttemptDelete.
func NewTempGitRepo() (string, error) {
	gitDir, err := os.MkdirTemp("", "releasego-temp-git-*")
	if err != nil {
		return "", err
	}
	if err := executil.Run(exec.Command("git", "init", gitDir)); err != nil {
		return "", err
	}
	log.Printf("Created dir %#q to store temp Git repo.\n", gitDir)
	return gitDir, nil
}

func NewTempCloneRepo(src string) (string, error) {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	d, err := os.MkdirTemp("", "temp-clone-*")
	if err != nil {
		return "", err
	}
	if err := Run(d, "clone", "--no-checkout", absSrc, d); err != nil {
		return "", err
	}
	return d, nil
}

// AttemptDelete tries to delete the git dir. If an error occurs, log it, but this is not fatal.
// gitDir is expected to be in a temp dir, so it will be cleaned up later by the OS anyway.
func AttemptDelete(gitDir string) {
	if err := os.RemoveAll(gitDir); err != nil {
		log.Printf("Unable to clean up git repository directory %#q: %v\n", gitDir, err)
	}
}
