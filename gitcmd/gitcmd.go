// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package gitcmd contains utilities for common Git operations in a local repository, including
// authentication with a remote repository.
package gitcmd

import (
	"fmt"
	"strings"
)

const githubPrefix = "https://github.com/"
const azdoDncengPrefix = "https://dnceng@dev.azure.com/"

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
	if after, found := cutPrefix(url, githubPrefix); found {
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
	if after, found := cutPrefix(url, githubPrefix); found {
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
	if after, found := cutPrefix(url, azdoDncengPrefix); found {
		url = fmt.Sprintf(
			// Username doesn't matter. PAT is identity.
			"https://arbitraryusername:%v@dev.azure.com%v",
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

func cutPrefix(s, prefix string) (after string, found bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
