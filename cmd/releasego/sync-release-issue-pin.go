// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

const maxReleaseIssuePins = 3

const releaseIssuePinCapacityWarningMarker = "<!-- releasego:issue-pin-capacity-warning -->"

const releaseIssuePinCapacityWarning = releaseIssuePinCapacityWarningMarker + "\n\n> [!WARNING]\n" +
	"> This release tracker could not be pinned because all three issue pin slots are in use. " +
	"The release process will continue without pinning it. Unpin another issue and pin this tracker " +
	"manually, or rerun `releasego sync-release-issue-pin` for this issue."

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "sync-release-issue-pin",
		Summary: "Pin an open release tracker or unpin a closed one",
		Description: `

Reads the issue's current state rather than relying on an event payload. Issues not created by
create-release-day-issue are skipped.
`,
		Handle: handleSyncReleaseIssuePin,
	})
}

func handleSyncReleaseIssuePin(p subcmd.ParseFunc) error {
	repo := githubutil.BindRepoFlag()
	gitHubAuthFlags := githubutil.BindGitHubAuthFlags("")
	issueNumber := flag.Int("i", 0, "[Required] The release tracking issue number.")
	if err := p(); err != nil {
		return err
	}
	if *issueNumber <= 0 {
		return errors.New("a positive issue number is required")
	}
	owner, name, err := githubutil.ParseRepoFlag(repo)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, err := gitHubAuthFlags.NewClient(ctx)
	if err != nil {
		return err
	}
	var issue *github.Issue
	if err := githubutil.Retry(func() error {
		var err error
		issue, _, err = client.Issues.Get(ctx, owner, name, *issueNumber)
		return err
	}); err != nil {
		return err
	}
	if !strings.HasPrefix(issue.GetBody(), releaseDayIssueBodyPrefix) {
		log.Printf("Skipping %s/%s#%d: not a release tracking issue.", owner, name, *issueNumber)
		return nil
	}
	if err := syncReleaseIssuePin(ctx, client, owner, name, issue); err != nil {
		return fmt.Errorf("sync release tracking issue pin for %s/%s#%d: %w", owner, name, *issueNumber, err)
	}
	return nil
}

type releaseIssuePinState struct {
	ID         string
	Number     int
	Body       string
	State      string
	IsPinned   *bool
	Repository *releaseIssuePinRepository
}

type releaseIssuePinRepository struct {
	PinnedIssues *struct {
		TotalCount *int
		Nodes      []struct {
			Issue *releaseIssuePinState
		}
	}
}

type releaseIssuePinResponse struct {
	Data struct {
		Node           *releaseIssuePinState
		Repository     *releaseIssuePinRepository
		UpdateIssuePin *struct {
			Issue *releaseIssuePinState
		}
	}
	Errors []struct {
		Message string
	}
}

func unpinClosedReleaseIssues(ctx context.Context, client *github.Client, owner, repo string) error {
	var closed []*releaseIssuePinState
	if err := githubutil.Retry(func() error {
		response, err := releaseIssuePinGraphQL(ctx, client, `
			query ($owner: String!, $repo: String!) {
				repository(owner: $owner, name: $repo) {
					pinnedIssues(first: 100) {
						totalCount
						nodes { issue { id number body state } }
					}
				}
			}`, map[string]string{"owner": owner, "repo": repo})
		if err != nil {
			return err
		}
		repository := response.Data.Repository
		if repository == nil || repository.PinnedIssues == nil || repository.PinnedIssues.TotalCount == nil {
			return errors.New("GitHub did not return the repository's pinned issues")
		}
		pins := repository.PinnedIssues
		if len(pins.Nodes) != *pins.TotalCount {
			return errors.New("GitHub did not return all pinned issues")
		}
		var candidates []*releaseIssuePinState
		for _, pin := range pins.Nodes {
			issue := pin.Issue
			if issue == nil || issue.ID == "" || issue.Number <= 0 || (issue.State != "OPEN" && issue.State != "CLOSED") {
				return errors.New("GitHub returned an invalid pinned issue")
			}
			if issue.State == "CLOSED" && strings.HasPrefix(issue.Body, releaseDayIssueBodyPrefix) {
				candidates = append(candidates, issue)
			}
		}
		closed = candidates
		return nil
	}); err != nil {
		return err
	}

	for _, issue := range closed {
		if err := githubutil.Retry(func() error {
			current, err := getReleaseIssuePinState(ctx, client, issue.ID)
			if err != nil {
				return err
			}
			if current.State != "CLOSED" || !*current.IsPinned || !strings.HasPrefix(current.Body, releaseDayIssueBodyPrefix) {
				log.Printf("Skipping %s/%s#%d: no longer a closed, pinned release tracker.", owner, repo, issue.Number)
				return nil
			}
			if err := updateReleaseIssuePin(ctx, client, issue.ID, false); err != nil {
				log.Printf("Unpinning closed release tracker %s/%s#%d failed; checking its current state: %v", owner, repo, issue.Number, err)
				current, checkErr := getReleaseIssuePinState(ctx, client, issue.ID)
				if checkErr != nil {
					return errors.Join(err, fmt.Errorf("recheck closed release tracker pin: %w", checkErr))
				}
				if *current.IsPinned {
					return err
				}
			}
			log.Printf("Unpinned closed release tracker %s/%s#%d.", owner, repo, issue.Number)
			return nil
		}); err != nil {
			return fmt.Errorf("unpin closed release tracker %s/%s#%d: %w", owner, repo, issue.Number, err)
		}
	}
	return nil
}

func syncReleaseIssuePin(ctx context.Context, client *github.Client, owner, repo string, releaseIssue *github.Issue) error {
	nodeID := releaseIssue.GetNodeID()
	if nodeID == "" {
		return errors.New("release tracking issue has no GitHub node ID")
	}
	if releaseIssue.GetNumber() <= 0 {
		return errors.New("release tracking issue has no positive issue number")
	}
	pinsFull := false
	if err := githubutil.Retry(func() error {
		issue, err := getReleaseIssuePinState(ctx, client, nodeID)
		if err != nil {
			return err
		}
		pinned := issue.State == "OPEN"
		if *issue.IsPinned == pinned {
			log.Printf("Release tracking issue %s already has pinned=%t.", nodeID, pinned)
			return nil
		}

		if pinned {
			if *issue.Repository.PinnedIssues.TotalCount >= maxReleaseIssuePins {
				pinsFull = true
				return nil
			}
		}
		if err := updateReleaseIssuePin(ctx, client, nodeID, pinned); err != nil {
			if !pinned {
				return err
			}
			// Recheck before returning the error, even on the final retry.
			log.Printf("Pinning release tracking issue %s failed; checking its current state: %v", nodeID, err)
			current, checkErr := getReleaseIssuePinState(ctx, client, nodeID)
			if checkErr != nil {
				return errors.Join(err, fmt.Errorf("recheck release tracking issue pin: %w", checkErr))
			}
			if *current.IsPinned == (current.State == "OPEN") {
				log.Printf("Release tracking issue %s already has the required pin state.", nodeID)
				return nil
			}
			if current.State == "OPEN" && *current.Repository.PinnedIssues.TotalCount >= maxReleaseIssuePins {
				pinsFull = true
				return nil
			}
			return err
		}
		log.Printf("Release tracking issue %s now has pinned=%t.", nodeID, pinned)
		return nil
	}); err != nil {
		return err
	}
	if !pinsFull {
		return nil
	}
	log.Printf("Warning: cannot pin %s/%s#%d: all issue pin slots are in use. Continuing release.", owner, repo, releaseIssue.GetNumber())
	return githubutil.Retry(func() error {
		exists, err := releaseIssuePinWarningExists(ctx, client, owner, repo, releaseIssue.GetNumber())
		if err != nil {
			return err
		}
		if exists {
			log.Printf("Pin-capacity warning already exists on %s/%s#%d.", owner, repo, releaseIssue.GetNumber())
			return nil
		}
		comment, _, err := client.Issues.CreateComment(ctx, owner, repo, releaseIssue.GetNumber(), github.IssueCommentRequest{
			Body: releaseIssuePinCapacityWarning,
		})
		if err != nil {
			exists, checkErr := releaseIssuePinWarningExists(ctx, client, owner, repo, releaseIssue.GetNumber())
			if checkErr != nil || !exists {
				return errors.Join(fmt.Errorf("post pin-capacity warning: %w", err), checkErr)
			}
			log.Printf("Pin-capacity warning confirmed after a failed response: %v", err)
			return nil
		}
		log.Printf("Pin-capacity warning comment: %s", comment.GetHTMLURL())
		return nil
	})
}

func getReleaseIssuePinState(ctx context.Context, client *github.Client, nodeID string) (*releaseIssuePinState, error) {
	response, err := releaseIssuePinGraphQL(ctx, client, `
		query ($issueID: ID!) {
			node(id: $issueID) {
				... on Issue {
					id state isPinned body
					repository { pinnedIssues(first: 1) { totalCount } }
				}
			}
		}`, map[string]string{"issueID": nodeID})
	if err != nil {
		return nil, err
	}
	issue := response.Data.Node
	if issue == nil || issue.ID != nodeID || issue.IsPinned == nil {
		return nil, errors.New("GitHub did not return the release tracking issue's pin state")
	}
	switch issue.State {
	case "OPEN":
		if !*issue.IsPinned && (issue.Repository == nil || issue.Repository.PinnedIssues == nil ||
			issue.Repository.PinnedIssues.TotalCount == nil || *issue.Repository.PinnedIssues.TotalCount < 0) {

			return nil, errors.New("GitHub did not return the repository's pinned issue count")
		}
	case "CLOSED":
	default:
		return nil, fmt.Errorf("unexpected release tracking issue state %q", issue.State)
	}
	return issue, nil
}

func updateReleaseIssuePin(ctx context.Context, client *github.Client, nodeID string, pinned bool) error {
	mutation := "unpinIssue"
	if pinned {
		mutation = "pinIssue"
	}
	response, err := releaseIssuePinGraphQL(ctx, client, fmt.Sprintf(`
		mutation ($issueID: ID!) {
			updateIssuePin: %s(input: {issueId: $issueID}) {
				issue { id isPinned }
			}
		}`, mutation), map[string]string{"issueID": nodeID})
	if err != nil {
		return err
	}
	result := response.Data.UpdateIssuePin
	if result == nil || result.Issue == nil || result.Issue.ID != nodeID ||
		result.Issue.IsPinned == nil || *result.Issue.IsPinned != pinned {

		return fmt.Errorf("GitHub did not confirm release tracking issue %s has pinned=%t", nodeID, pinned)
	}
	return nil
}

func releaseIssuePinWarningExists(ctx context.Context, client *github.Client, owner, repo string, number int) (bool, error) {
	options := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, response, err := client.Issues.ListComments(ctx, owner, repo, number, options)
		if err != nil {
			return false, fmt.Errorf("find pin-capacity warning: %w", err)
		}
		for _, comment := range comments {
			if strings.Contains(comment.GetBody(), releaseIssuePinCapacityWarningMarker) {
				return true, nil
			}
		}
		if response.NextPage == 0 {
			return false, nil
		}
		options.Page = response.NextPage
	}
}

func releaseIssuePinGraphQL(ctx context.Context, client *github.Client, query string, variables map[string]string) (*releaseIssuePinResponse, error) {
	req, err := client.NewRequest(ctx, http.MethodPost, "graphql", struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}{
		Query:     query,
		Variables: variables,
	})
	if err != nil {
		return nil, err
	}
	var response releaseIssuePinResponse
	if _, err := client.Do(req, &response); err != nil {
		return nil, err
	}
	// GraphQL can report operation failures in an HTTP 200 response.
	if len(response.Errors) != 0 {
		messages := make([]string, len(response.Errors))
		for i, e := range response.Errors {
			messages[i] = e.Message
		}
		return nil, fmt.Errorf("GitHub GraphQL errors: %s", strings.Join(messages, "; "))
	}
	return &response, nil
}
