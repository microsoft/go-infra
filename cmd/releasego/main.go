// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/microsoft/go-infra/subcmd"
	"golang.org/x/oauth2"
)

const description = `
releasego runs various parts of a release of microsoft/go. The subcommands implement the steps.
`

// subcommands is the list of subcommand options, populated by each file's init function.
var subcommands []subcmd.Option

func main() {
	if err := subcmd.Run("releasego", description, subcommands); err != nil {
		log.Fatal(err)
	}
}

func githubClient(ctx context.Context, pat string) (*github.Client, error) {
	if pat == "" {
		return nil, errors.New("no GitHub PAT specified")
	}
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pat})
	tokenClient := oauth2.NewClient(ctx, tokenSource)
	return github.NewClient(tokenClient), nil
}

func tagFlag() *string {
	return flag.String("tag", "", "[Required] The tag name.")
}

func githubPATFlag() *string {
	return flag.String("pat", "", "[Required] The GitHub PAT to use.")
}

func azdoPATFlag() *string {
	return flag.String("azdopat", "", "[Required] The Azure DevOps PAT to use.")
}

func repoFlag() *string {
	return flag.String("repo", "", "[Required] The repo to tag, in '{owner}/{repo}' form.")
}

func parseRepoFlag(repo string) (owner, name string, err error) {
	if repo == "" {
		return "", "", errors.New("repo not specified")
	}
	var found bool
	owner, name, found = strings.Cut(repo, "/")
	if !found {
		return "", "", fmt.Errorf("unable to split repo into owner and name: %v", repo)
	}
	return
}

// retryAttempts is the number of times 'retry' will attempt the call.
const retryAttempts = 5
const maxRateLimitResetWait = time.Minute * 15
const rateLimitResetWaitSlack = time.Second * 5

// retry runs f up to 'attempts' times, printing the error if one is encountered. Handles GitHub
// rate limit exceeded errors by waiting, if the reset will happen reasonably soon.
func retry(f func() error) error {
	var i = 0
	for ; i < retryAttempts; i++ {
		log.Printf("   attempt %v/%v...\n", i+1, retryAttempts)
		err := f()
		if err != nil {
			log.Printf("...attempt %v/%v failed with error: %v\n", i+1, retryAttempts, err)
			if i+1 < retryAttempts {
				var rateErr *github.RateLimitError
				if errors.As(err, &rateErr) {
					resetDuration := rateErr.Rate.Reset.Sub(time.Now())

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

// appendPathAndVerificationFilePaths appends to p the path and the verification file (hash,
// signature) paths that should be available along with the file at path. This can be used to
// calculate what URLs should be available for a given build artifact URL.
func appendPathAndVerificationFilePaths(p []string, path string) []string {
	p = append(p, path, path+".sha256")
	if strings.HasSuffix(path, ".tar.gz") {
		p = append(p, path+".sig")
	}
	return p
}
