// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubutil

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v65/github"
	"github.com/microsoft/go-infra/gitcmd"
	"github.com/microsoft/go-infra/stringutil"
	"golang.org/x/oauth2"
)

const (
	githubPrefix = "https://github.com/"
)

// HTTPRequestAuther adds some kind of HTTP authentication to a request.
type HTTPRequestAuther interface {
	// InsertHTTPAuth inserts authentication into the request, if applicable.
	InsertHTTPAuth(req *http.Request) error
}

// GitHubAPIAuther authenticates HTTP requests and GitHub URLs using the types of auth that are
// used to auth to the GitHub API.
type GitHubAPIAuther interface {
	// GetIdentity returns the identity (username or app name) that GitHub would refer to this
	// auther by.
	GetIdentity() (string, error)

	HTTPRequestAuther
	gitcmd.URLAuther
}

// GitHubPATAuther adds a username and password into the https-style GitHub URL.
type GitHubPATAuther struct {
	// User to authenticate as. GitHub APIs don't care what this is, but this value is used if set.
	User string
	// PAT to authenticate with.
	PAT string
}

func (a GitHubPATAuther) InsertHTTPAuth(req *http.Request) error {
	if a.PAT == "" {
		return nil
	}
	user := a.User
	if user == "" {
		user = "_" // A placeholder is required, but GitHub doesn't care what it is.
	}
	req.SetBasicAuth(user, a.PAT)
	return nil
}

func (a GitHubPATAuther) InsertAuth(url string) string {
	if a.PAT == "" {
		return url
	}
	user := a.User
	if user == "" {
		user = "_" // A placeholder is required, but GitHub doesn't care what it is.
	}
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("https://%v:%v@github.com/%v", user, a.PAT, after)
	}
	return url
}

func (a GitHubPATAuther) GetIdentity() (string, error) {
	ctx := context.Background()
	client, err := NewClient(ctx, a.PAT)
	if err != nil {
		return "", err
	}
	response, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return response.GetLogin(), nil
}

// GitHubSSHAuther turns an https-style GitHub URL into an SSH-style GitHub URL.
type GitHubSSHAuther struct{}

func (GitHubSSHAuther) InsertAuth(url string) string {
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("git@github.com:%v", after)
	}
	return url
}

// GitHubAppAuther authenticates using a GitHub App instead of a PAT.
type GitHubAppAuther struct {
	ClientID       string
	InstallationID int64
	PrivateKey     string // The GitHub App's private key (PEM format)
}

func (a GitHubAppAuther) InsertAuth(url string) string {
	token, _, err := a.getInstallationToken()
	if err != nil {
		log.Printf("Failed to get GitHub App installation token: %v", err)
		return url
	}
	if after, found := stringutil.CutPrefix(url, githubPrefix); found {
		return fmt.Sprintf("https://x-access-token:%v@github.com/%v", token, after)
	}
	return url
}

func (a GitHubAppAuther) InsertHTTPAuth(req *http.Request) error {
	token, _, err := a.getInstallationToken()
	if err != nil {
		log.Printf("Failed to get GitHub App installation token: %v", err)
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	return nil
}

func (a GitHubAppAuther) GetIdentity() (string, error) {
	ctx := context.Background()
	client, err := NewInstallationClient(ctx, a.ClientID, a.InstallationID, a.PrivateKey)
	if err != nil {
		return "", err
	}
	response, _, err := client.Apps.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return response.GetName(), nil
}

func (a GitHubAppAuther) getInstallationToken() (string, time.Time, error) {
	return GenerateInstallationToken(
		context.Background(),
		a.ClientID,
		a.InstallationID,
		a.PrivateKey)
}

func GenerateInstallationToken(ctx context.Context, clientID string, installationID int64, privateKey string) (string, time.Time, error) {
	jwt, err := generateJWT(clientID, privateKey)
	if err != nil {
		return "", time.Time{}, err
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: jwt})
	tokenClient := oauth2.NewClient(ctx, tokenSource)

	client := github.NewClient(tokenClient)
	installationToken, _, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return "", time.Time{}, err
	}

	return *installationToken.Token, installationToken.ExpiresAt.Time, nil
}

// GenerateJWT generates a JWT for a GitHub App.
func generateJWT(clientID, privateKey string) (string, error) {
	privkey, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to base64-decode private key: %v", err)
	}
	block, _ := pem.Decode(privkey)
	if block == nil {
		return "", fmt.Errorf("failed to decode private key")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse RSA private key: %v", err)
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(now),
		// This token is only used to get an installation token, so we set the expiration to 5 minutes.
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		Issuer:    clientID,
	}
	fmt.Printf("%#v\n", claims)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	fmt.Printf("%#v\n", token)
	signedToken, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %v", err)
	}
	return signedToken, nil
}
