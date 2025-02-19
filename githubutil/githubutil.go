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

// BindPATFlag returns a flag to specify the personal access token.
func BindPATFlag() *string {
	return flag.String("pat", "", "[Required] The GitHub PAT to use.")
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

func ListDirFiles(ctx context.Context, client *github.Client, owner, repo, ref, dir string) ([]*github.RepositoryContent, error) {
	_, directoryContent, err := getContents(ctx, client, owner, repo, ref, dir)
	if err != nil {
		return nil, err
	}
	if directoryContent == nil {
		return nil, fmt.Errorf("%q is unexpectedly a file; %w", dir, ErrFileNotExists)
	}

	return directoryContent, nil
}

// DownloadFile downloads a file from a given repository.
func DownloadFile(ctx context.Context, client *github.Client, owner, repo, ref, path string) ([]byte, error) {
	fileContent, _, err := getContents(ctx, client, owner, repo, ref, path)
	if err != nil {
		return nil, err
	}
	if fileContent == nil {
		return nil, fmt.Errorf("%q is unexpectedly a directory; %w", path, ErrFileNotExists)
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return nil, err
	}

	return []byte(content), nil
}

// getContents either gets the file content or directory contents at the given path. Depending on
// the type of content at path, the other returned value will be nil.
func getContents(
	ctx context.Context,
	client *github.Client,
	owner, repo, ref, path string,
) (
	fileContent *github.RepositoryContent,
	directoryContent []*github.RepositoryContent,
	err error,
) {
	var resp *github.Response

	if err = Retry(func() error {
		fileContent, directoryContent, resp, err = client.Repositories.GetContents(
			ctx,
			owner, repo, path,
			&github.RepositoryContentGetOptions{
				Ref: ref,
			})
		return err
	}); err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, nil, ErrFileNotExists
		}
		return nil, nil, err
	}

	return fileContent, directoryContent, nil
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
