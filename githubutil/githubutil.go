// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package githubutil

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v65/github"
	"golang.org/x/oauth2"
)

// Types of error that may be returned from the GitHub API that the caller may want to handle.
var (
	// ErrFileNotExists indicates that the requested file does not exist in the specified GitHub repository and branch.
	ErrFileNotExists = errors.New("file does not exist in the given repository and branch")
	// ErrRepositoryNotExists indicates that the requested repository does not exist.
	ErrRepositoryNotExists = errors.New("repository does not exist")
	// ErrNoAuthProvided indicates that no GitHubAuthFlags fields are filled in. It can be used to
	// skip actions that require auth when no auth is provided.
	//
	// This error is not used if any of the flags are set, e.g. if app auth is partially
	// configured. This indicates an issue with the command call and needs to be fixed.
	ErrNoAuthProvided = errors.New("no GitHub authentication provided")
)

// NewClient creates a GitHub client using the given personal access token.
func NewClient(ctx context.Context, pat string) (*github.Client, error) {
	if pat == "" {
		return nil, errors.New("no GitHub PAT specified")
	}
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pat})
	tokenClient := oauth2.NewClient(ctx, tokenSource)
	return github.NewClient(tokenClient), nil
}

// NewInstallationClient creates a GitHub client using the given GitHub Client ID, installation ID, and private key.
func NewInstallationClient(ctx context.Context, clientID string, installationID int64, privateKey string) (*github.Client, error) {
	if clientID == "" {
		return nil, errors.New("no GitHub App Client ID specified")
	}
	if installationID == 0 {
		return nil, errors.New("no GitHub App Installation ID specified")
	}
	if privateKey == "" {
		return nil, errors.New("no GitHub App private key specified")
	}

	installationToken, err := GenerateInstallationToken(ctx, clientID, installationID, privateKey)
	if err != nil {
		return nil, err
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: installationToken})
	tokenClient := oauth2.NewClient(ctx, tokenSource)
	return github.NewClient(tokenClient), nil
}

type GitHubAuthFlags struct {
	GitHubPat             *string
	GitHubAppClientID     *string
	GitHubAppInstallation *int64
	GitHubAppPrivateKey   *string
}

// BindGitHubAuthFlags creates gitHubAuthFlags with the 'flag' package, globally registering them
// in the flag package so ParseBoundFlags will find them.
//
// If user is specified, it is inserted into the middle of the flag name. This lets the func be
// called multiple times to set up multiple users/apps. It's inserted into the middle so the sorted
// "-h" output shows all the user/app's auth flags adjacent to each other, and so we can include a
// reminder about which values are associated with each other.
func BindGitHubAuthFlags(user string) *GitHubAuthFlags {
	prefix := "github"
	if user != "" {
		prefix += "-" + user
	}
	appAuthTogether := "If specified, all " + prefix + "-app-* flags must be specified."
	return &GitHubAuthFlags{
		GitHubPat: flag.String(
			prefix+"-pat", "",
			"The GitHub PAT to use. Exclusive with "+prefix+"-app-*"),
		GitHubAppClientID: flag.String(
			prefix+"-app-client-id"+user, "",
			"Use this GitHub App Client ID to authenticate to GitHub. "+appAuthTogether),
		GitHubAppInstallation: flag.Int64(
			prefix+"-app-installation"+user, 0,
			"Use this GitHub App Installation ID to authenticate to GitHub. "+appAuthTogether),
		GitHubAppPrivateKey: flag.String(
			prefix+"-app-private-key"+user, "",
			"Use this GitHub App Private Key to authenticate to GitHub, provided in base64 PEM format. "+appAuthTogether),
	}
}

// NewClient returns a GitHub client based on the flags (e.g. PAT, GitHub App).
func (f *GitHubAuthFlags) NewClient(ctx context.Context) (*github.Client, error) {
	if *f.GitHubPat != "" {
		return NewClient(ctx, *f.GitHubPat)
	}
	for _, appFlagSet := range []bool{
		*f.GitHubAppClientID != "",
		*f.GitHubAppInstallation != 0,
		*f.GitHubAppPrivateKey != "",
	} {
		if appFlagSet {
			return NewInstallationClient(
				ctx,
				*f.GitHubAppClientID,
				*f.GitHubAppInstallation,
				*f.GitHubAppPrivateKey)
		}
	}
	return nil, ErrNoAuthProvided
}

// NewAuther returns an auther based on the flags (e.g. PAT, GitHub App).
func (f *GitHubAuthFlags) NewAuther() (GitHubAPIAuther, error) {
	if *f.GitHubPat != "" {
		return &GitHubPATAuther{
			PAT: *f.GitHubPat,
		}, nil
	}
	for _, appFlagSet := range []bool{
		*f.GitHubAppClientID != "",
		*f.GitHubAppInstallation != 0,
		*f.GitHubAppPrivateKey != "",
	} {
		if appFlagSet {
			if *f.GitHubAppClientID == "" {
				return nil, errors.New("no GitHub App Client ID specified")
			}
			if *f.GitHubAppInstallation == 0 {
				return nil, errors.New("no GitHub App Installation ID specified")
			}
			if *f.GitHubAppPrivateKey == "" {
				return nil, errors.New("no GitHub App private key specified")
			}
			return &GitHubAppAuther{
				ClientID:       *f.GitHubAppClientID,
				InstallationID: *f.GitHubAppInstallation,
				PrivateKey:     *f.GitHubAppPrivateKey,
			}, nil
		}
	}
	return nil, ErrNoAuthProvided
}

// BindPATFlag returns a flag to specify the personal access token.
// If possible, a command should instead use BindGitHubAuthFlags to support both PAT and app auth.
func BindPATFlag() *string {
	return flag.String("github-pat", "", "[Required] The GitHub PAT to use.")
}

// BindRepoFlag returns a flag to specify a GitHub repo to target. Parse it with ParseRepoFlag.
func BindRepoFlag() *string {
	return flag.String("repo", "", "[Required] The target repo, in '{owner}/{repo}' form.")
}

// ParseRepoFlag splits a given repo (owner/name) into owner and name, or returns an error.
func ParseRepoFlag(repo *string) (owner, name string, err error) {
	if *repo == "" {
		return "", "", errors.New("repo not specified")
	}
	owner, name, found := strings.Cut(*repo, "/")
	if !found {
		return "", "", fmt.Errorf("unable to split repo into owner and name: %v", repo)
	}
	return owner, name, nil
}

const (
	retryAttempts           = 5
	maxRateLimitResetWait   = time.Minute * 15
	rateLimitResetWaitSlack = time.Second * 5
)

// Retry runs f up to 'retryAttempts' times, printing the error if one is encountered. Handles
// GitHub rate limit exceeded errors by waiting, if the reset will happen reasonably soon.
func Retry(f func() error) error {
	i := 0
	for ; i < retryAttempts; i++ {
		log.Printf("   attempt %v/%v...\n", i+1, retryAttempts)
		err := f()
		if err != nil {
			log.Printf("...attempt %v/%v failed with error: %v\n", i+1, retryAttempts, err)
			if i+1 < retryAttempts {
				var rateErr *github.RateLimitError
				if errors.As(err, &rateErr) {
					resetDuration := time.Until(rateErr.Rate.Reset.Time)

					log.Printf("...rate limit exceeded. Reset at %v, %v from now.\n", rateErr.Rate.Reset, resetDuration)
					if resetDuration > maxRateLimitResetWait {
						log.Printf("...rate limit reset is too far away to reasonably wait. Aborting.")
						return err
					}

					// Sleep until the reset, plus some extra in case our clocks aren't synchronized.
					wait := resetDuration + rateLimitResetWaitSlack
					log.Printf("...waiting %v before next retry.\n", wait)
					time.Sleep(wait)
				}
				continue
			}
			log.Printf("...no retries remaining.\n")
			return err
		}
		break
	}
	log.Printf("...attempt %v/%v successful.\n", i+1, retryAttempts)
	return nil
}

// FetchEachPage helps fetch all data from a GitHub API call that may or may not span multiple
// pages. FetchEachPage initially calls f with no paging parameters, then inspects the GitHub
// response to see if there are more pages to fetch. If so, it constructs paging parameters that
// will fetch the next page and calls f again. This repeats until there aren't any more pages.
//
// Note that FetchEachPage doesn't process any of the result data, and doesn't actually call the
// GitHub API. f must do this itself. This allows FetchEachPage to work with any GitHub API.
func FetchEachPage(f func(options github.ListOptions) (*github.Response, error)) error {
	var options github.ListOptions
	for {
		log.Printf("Fetching page %v...\n", options.Page)
		resp, err := f(options)
		if err != nil {
			return err
		}
		if resp.NextPage == 0 {
			return nil
		}
		options.Page = resp.NextPage
	}
}

// UploadFile is a function that will upload a file to a given repository.
func UploadFile(ctx context.Context, client *github.Client, owner, repo, branch, path, message string, content []byte) error {
	err := Retry(func() error {
		_, _, err := client.Repositories.CreateFile(ctx, owner, repo, path, &github.RepositoryContentFileOptions{
			Message: &message,
			Content: content,
			Branch:  &branch,
		})

		return err
	})

	return err
}

// DownloadFile downloads a file from a given repository.
func DownloadFile(ctx context.Context, client *github.Client, owner, repo, ref, path string) ([]byte, error) {
	var fileContent *github.RepositoryContent
	var resp *github.Response
	var err error

	if err = Retry(func() error {
		fileContent, _, resp, err = client.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{
			Ref: ref,
		})
		return err
	}); err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, ErrFileNotExists
		}
		return nil, err
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return nil, err
	}

	return []byte(content), nil
}

// FullyCreateFork creates a fork of the given repository under the PAT owner's account, waits for
// the fork to be fully created, and returns its full information.
func FullyCreateFork(ctx context.Context, client *github.Client, upstreamOwner, repo string) (*github.Repository, error) {
	var fork *github.Repository

	if err := Retry(func() error {
		log.Printf("Creating fork of %v/%v...\n", upstreamOwner, repo)
		var err error
		fork, _, err = client.Repositories.CreateFork(ctx, upstreamOwner, repo, &github.RepositoryCreateForkOptions{
			DefaultBranchOnly: true,
		})
		var acceptedError *github.AcceptedError
		if errors.As(err, &acceptedError) {
			return nil
		}
		return err
	}); err != nil {
		return nil, err
	}

	// Make sure the fork is done being created by getting the full info.
	fork, err := FetchRepository(ctx, client, *fork.Owner.Login, *fork.Name)
	if err != nil {
		return nil, err
	}

	return fork, nil
}

// PATScopes gets the scopes of the client's PAT by reading a simple API call's response headers.
func PATScopes(ctx context.Context, client *github.Client) ([]string, error) {
	var scopes []string

	if err := Retry(func() error {
		log.Printf("Fetching PAT scopes...\n")
		_, resp, err := client.Meta.Get(ctx)
		if err != nil {
			return err
		}
		for _, s := range strings.Split(resp.Header.Get("X-OAuth-Scopes"), ",") {
			scopes = append(scopes, strings.TrimSpace(s))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return scopes, nil
}

// FetchRepository fetches a repository from GitHub or returns an error. If the GitHub API error
// matches one of the errors defined in this package, it is wrapped. Retries if necessary.
func FetchRepository(ctx context.Context, client *github.Client, owner, repo string) (*github.Repository, error) {
	var repository *github.Repository

	if err := Retry(func() error {
		log.Printf("Fetching repository %v/%v...\n", owner, repo)
		r, _, err := client.Repositories.Get(ctx, owner, repo)
		if err != nil {
			return err
		}
		repository = r
		return nil
	}); err != nil {
		var errResponse *github.ErrorResponse
		if errors.As(err, &errResponse) && errResponse.Response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %v/%v", ErrRepositoryNotExists, owner, repo)
		}
		return nil, err
	}

	return repository, nil
}

// Git tree entry mode constants defined by https://docs.github.com/en/rest/git/trees?apiVersion=2022-11-28#create-a-tree--parameters
const (
	TreeModeFile       = "100644"
	TreeModeExecutable = "100755"
	TreeModeDir        = "040000"
	TreeModeSubmodule  = "160000"
	TreeModeGitlink    = "120000"
)
