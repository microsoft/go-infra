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

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
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

// DownloadFile is a function that will download a file from a given repository.
func DownloadFile(ctx context.Context, client *github.Client, owner, repo, branch, path string) (file []byte, exists bool, err error) {
	var fileContent *github.RepositoryContent
	var resp *github.Response

	if err = Retry(func() error {
		fileContent, _, resp, err = client.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{
			Ref: branch,
		})

		return err
	}); err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return nil, false, err
	}

	return []byte(content), true, nil
}
