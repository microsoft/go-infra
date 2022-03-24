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

// GitURLAuther manipulates a Git repository URL (GitHub, AzDO, ...) such that Git commands taking a
// remote will work with the URL. This is intentionally vague: it could add an access token into the
// URL, or it could simply make the URL compatible with environmental auth on the machine (SSH).
type GitURLAuther interface {
	// InsertAuth inserts authentication into the URL and returns it, or if the auther doesn't
	// apply, returns the url without any modifications.
	InsertAuth(url string) string
}

// GitHubSSHAuther turns an https-style GitHub URL into an SSH-style GitHub URL.
type GitHubSSHAuther struct{}

func (GitHubSSHAuther) InsertAuth(url string) string {
	if strings.HasPrefix(url, githubPrefix) {
		return fmt.Sprintf("git@github.com:%v", strings.TrimPrefix(url, githubPrefix))
	}
	return url
}

// GitHubPATAuther adds a username and password into the https-style GitHub URL.
type GitHubPATAuther struct {
	User, PAT string
}

func (a GitHubPATAuther) InsertAuth(url string) string {
	if a.User != "" && a.PAT != "" && strings.HasPrefix(url, githubPrefix) {
		return fmt.Sprintf(
			"https://%v:%v@github.com/%v",
			a.User, a.PAT, strings.TrimPrefix(url, githubPrefix))
	}
	return url
}

// AzDOPATAuther adds a PAT into the https-style Azure DevOps repository URL.
type AzDOPATAuther struct {
	PAT string
}

func (a AzDOPATAuther) InsertAuth(url string) string {
	if a.PAT != "" && strings.HasPrefix(url, azdoDncengPrefix) {
		url = fmt.Sprintf(
			// Username doesn't matter. PAT is identity.
			"https://arbitraryusername:%v@dev.azure.com%v",
			a.PAT, strings.TrimPrefix(url, azdoDncengPrefix))
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
	Authers []GitURLAuther
}

func (m MultiAuther) InsertAuth(url string) string {
	for _, a := range m.Authers {
		if authUrl := a.InsertAuth(url); authUrl != url {
			return authUrl
		}
	}
	return url
}
