// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package gitcmd contains utilities for common Git operations in a local repository, including
// authentication with a remote repository.
package gitcmd

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/stringutil"
)

const (
	githubPrefix     = "https://github.com/"
	azdoDncengPrefix = "https://dnceng@dev.azure.com/"
	githubAPI        = "https://api.github.com"
)

// URLAuther manipulates a Git repository URL (GitHub, AzDO, ...) such that Git commands taking a
// remote will work with the URL. This is intentionally vague: it could add an access token into the
// URL, or it could simply make the URL compatible with environmental auth on the machine (SSH).
type URLAuther interface {
	// InsertAuth inserts authentication into the URL and returns it, or if the auther doesn't
	// apply, returns the url without any modifications.
	InsertAuth(url string) string
	InsertHTTPAuth(req *http.Request)
}

// GitHubSSHAuther turns an https-style GitHub URL into an SSH-style GitHub URL.
type GitHubSSHAuther struct{}

func (GitHubSSHAuther) InsertHTTPAuth(req *http.Request) {
	// No-op
}

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

func (a GitHubPATAuther) InsertHTTPAuth(req *http.Request) {
	if a.PAT == "" {
		return
	}
	req.SetBasicAuth(a.User, a.PAT)
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

// GitHubAppAuther authenticates using a GitHub App instead of a PAT.
type GitHubAppAuther struct {
	AppID          int64
	InstallationID int64
	PrivateKey     string // The GitHub App's private key (PEM format)
}

func (a GitHubAppAuther) InsertAuth(url string) string {
	token, err := a.getInstallationToken()
	if err != nil {
		log.Printf("Failed to get GitHub App installation token: %v", err)
		return url
	}
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("https://x-access-token:%v@github.com/%v", token, after)
	}
	return url
}

func (a GitHubAppAuther) InsertHTTPAuth(req *http.Request) {
	token, err := a.getInstallationToken()
	if err != nil {
		log.Printf("Failed to get GitHub App installation token: %v", err)
		return
	}
	req.Header.Set("Authorization", "token "+token)
}

func (a GitHubAppAuther) getInstallationToken() (string, error) {
	// Generate a JWT using the private key
	jwt, err := generateJWT(a.AppID, a.PrivateKey)
	if err != nil {
		return "", err
	}

	// Exchange JWT for an installation token
	token, err := fetchInstallationToken(jwt, a.InstallationID)
	if err != nil {
		return "", err
	}
	return token, nil
}

// generateJWT creates a JWT for the GitHub App.
func generateJWT(appID int64, privateKey string) (string, error) {
	privkey, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to decode private key: %v", err)
	}
	block, _ := pem.Decode(privkey)
	if block == nil {
		return "", fmt.Errorf("failed to decode private key")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse RSA private key: %v", err)
	}

	now := time.Now().Unix()
	claims := jwt.MapClaims{
		"iat": now,       // Issued at time
		"exp": now + 600, // Expiration time (10 min)
		"iss": appID,     // GitHub App ID
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedToken, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %v", err)
	}
	return signedToken, nil
}

// fetchInstallationToken exchanges a JWT for an installation access token.
func fetchInstallationToken(jwt string, installationID int64) (string, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", githubAPI, installationID)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("failed to get installation token, status: %d", resp.StatusCode)
	}

	var result struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return "", err
	}

	return result.Token, nil
}

// AzDOPATAuther adds a PAT into the https-style Azure DevOps repository URL.
type AzDOPATAuther struct {
	PAT string
}

func (a AzDOPATAuther) InsertHTTPAuth(req *http.Request) {
	// No-op
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

func (NoAuther) InsertHTTPAuth(req *http.Request) {
	// No-op
}

func (NoAuther) InsertAuth(url string) string {
	return url
}

// MultiAuther tries multiple authers in sequence. Stops and returns the result when any auther
// makes a change to the URL.
type MultiAuther struct {
	Authers []URLAuther
}

func (m MultiAuther) InsertHTTPAuth(req *http.Request) {
	for _, a := range m.Authers {
		a.InsertHTTPAuth(req)
	}
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
