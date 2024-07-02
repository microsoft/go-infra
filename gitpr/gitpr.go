// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package gitpr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

var client = http.Client{
	Timeout: time.Second * 30,
}

// PRRefSet contains information about an automatic PR branch and calculates the set of refs that
// would correspond to that PR.
type PRRefSet struct {
	// Name of the base branch to update. Do not include "refs/heads/".
	Name string
	// Purpose of the PR. This is used to generate the PR branch name, "dev/{Purpose}/{Name}".
	Purpose string
}

// PRBranch is the name of the "head" branch name for this PR, under "dev/{Purpose}/{Name}"
// convention, without the "refs/heads/" prefix.
func (b PRRefSet) PRBranch() string {
	return "dev/" + b.Purpose + "/" + b.Name
}

// BaseBranchFetchRefspec is the refspec with src: PR base branch src, dst: PR head branch dst. This
// can be used with "fetch" to create a fresh dev branch.
func (b PRRefSet) BaseBranchFetchRefspec() string {
	return createRefspec(b.Name, b.PRBranch())
}

// PRBranchRefspec is the refspec that syncs the dev branch between two repos.
func (b PRRefSet) PRBranchRefspec() string {
	return createRefspec(b.PRBranch(), b.PRBranch())
}

// CreateGitHubPR creates the data model that can be sent to GitHub to create a PR for this branch.
func (b PRRefSet) CreateGitHubPR(headOwner, title, body string) *GitHubRequest {
	return &GitHubRequest{
		Head: headOwner + ":" + b.PRBranch(),
		Base: b.Name,

		Title: title,
		Body:  body,

		MaintainerCanModify: true,
		Draft:               false,
	}
}

// SyncPRRefSet calculates the set of refs that correspond to a PR branch that is performing a Git
// sync from an upstream repository.
type SyncPRRefSet struct {
	// UpstreamName is the name of the upstream branch being synced from.
	UpstreamName string
	// Commit is either the specific commit hash to sync to, or empty string to sync from the latest
	// commit in the branch. Commit is expected to already be contained in the upstream branch.
	Commit string
	PRRefSet
}

// UpstreamLocalBranch is the name of the upstream ref after it has been fetched locally.
func (b SyncPRRefSet) UpstreamLocalBranch() string {
	return "fetched-upstream/" + b.UpstreamName
}

// UpstreamLocalSyncTarget is the commit (or branch) that should be synced to. Normally, it is the
// tip of the upstream ref, but it may be a specific commit if the config struct specified one.
func (b SyncPRRefSet) UpstreamLocalSyncTarget() string {
	if b.Commit == "" {
		return b.UpstreamLocalBranch()
	}
	return b.Commit
}

// UpstreamMirrorLocalBranch is the name of the upstream ref after it has been fetched locally from
// the mirror of the upstream.
func (b SyncPRRefSet) UpstreamMirrorLocalBranch() string {
	return "fetched-upstream-mirror/" + b.UpstreamName
}

// UpstreamFetchRefspec fetches the current upstream ref into the local branch.
func (b SyncPRRefSet) UpstreamFetchRefspec() string {
	return createRefspec(b.UpstreamName, b.UpstreamLocalBranch())
}

// UpstreamMirrorFetchRefspec fetches the current upstream ref as it is in an upstream mirror into a
// local branch.
func (b SyncPRRefSet) UpstreamMirrorFetchRefspec() string {
	return createRefspec(b.UpstreamName, b.UpstreamMirrorLocalBranch())
}

// UpstreamMirrorRefspec is the refspec that mirrors the original branch name to the same name in another
// repo. This can be used with "push" for a mirror operation.
func (b SyncPRRefSet) UpstreamMirrorRefspec() string {
	return createRefspec(b.UpstreamLocalBranch(), b.UpstreamName)
}

// ForkFromMainRefspec fetches the specified main branch on the target repo into the local branch.
func (b SyncPRRefSet) ForkFromMainRefspec(mainBranch string) string {
	return createRefspec(mainBranch, b.Name)
}

// MirrorRefSet calculates the set of refs that correspond to a pure mirroring
// operation, where the set of mirrored branches is specified by a pattern.
type MirrorRefSet struct {
	UpstreamPattern string
}

// UpstreamMirrorLocalBranchPattern is the name of the local ref (or pattern
// matching multiple local refs) after it has been fetched from the upstream.
func (b MirrorRefSet) UpstreamMirrorLocalBranchPattern() string {
	return "fetched-upstream-mirror-pattern/" + b.UpstreamPattern
}

// UpstreamMirrorFetchRefspec fetches the remote refs that match the pattern to
// local branches.
func (b MirrorRefSet) UpstreamMirrorFetchRefspec() string {
	return createRefspec(b.UpstreamPattern, b.UpstreamMirrorLocalBranchPattern())
}

// UpstreamMirrorRefspec pushes the local mirroring branches to back to
// branches with the same name as the branches they were fetched from.
func (b MirrorRefSet) UpstreamMirrorRefspec() string {
	return createRefspec(b.UpstreamMirrorLocalBranchPattern(), b.UpstreamPattern)
}

// Remote is a parsed version of a Git Remote. It helps determine how to send a GitHub PR.
type Remote struct {
	url      string
	urlParts []string
}

// ParseRemoteURL takes the URL ("https://github.com/microsoft/go", "git@github.com:microsoft/go")
// and grabs the owner ("microsoft") and repository name ("go"). This assumes the URL follows one of
// these two patterns, or something that's compatible. Returns an initialized 'Remote'.
func ParseRemoteURL(url string) (*Remote, error) {
	r := &Remote{
		url,
		strings.FieldsFunc(url, func(r rune) bool { return r == '/' || r == ':' }),
	}
	if len(r.urlParts) < 3 {
		return r, fmt.Errorf(
			"failed to find 3 parts of Remote url '%v'. Found '%v'. Expected a string separated with '/' or ':', like https://github.com/microsoft/go or git@github.com:microsoft/go",
			r.url,
			r.urlParts,
		)
	}
	fmt.Printf("From repo URL %v, detected %v for the PR target.\n", url, r.urlParts)
	return r, nil
}

func (r Remote) GetOwnerRepo() []string {
	return r.urlParts[len(r.urlParts)-2:]
}

func (r Remote) GetOwner() string {
	return r.GetOwnerRepo()[0]
}

func (r Remote) GetOwnerSlashRepo() string {
	return strings.Join(r.GetOwnerRepo(), "/")
}

// GetUsername queries GitHub for the username associated with a PAT.
func GetUsername(pat string) string {
	request, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		log.Panic(err)
	}
	request.SetBasicAuth("", pat)

	response := &struct {
		Login string `json:"login"`
	}{}

	if err := sendJSONRequestSuccessful(request, response); err != nil {
		log.Panic(err)
	}

	return response.Login
}

// sendJSONRequest sends a request for JSON information. The JSON response is unmarshalled (parsed)
// into the 'response' parameter, based on the structure of 'response'.
func sendJSONRequest(request *http.Request, response interface{}) (status int, err error) {
	request.Header.Add("Accept", "application/vnd.github.v3+json")
	fmt.Printf("Sending request: %v %v\n", request.Method, request.URL)

	httpResponse, err := client.Do(request)
	if err != nil {
		return
	}
	defer httpResponse.Body.Close()
	status = httpResponse.StatusCode

	for key, value := range httpResponse.Header {
		if strings.HasPrefix(key, "X-Ratelimit-") {
			fmt.Printf("%v : %v\n", key, value)
		}
	}

	jsonBytes, err := ioutil.ReadAll(httpResponse.Body)
	if err != nil {
		return
	}

	fmt.Printf("---- Full response:\n%v\n", string(jsonBytes))
	fmt.Printf("----\n")

	err = json.Unmarshal(jsonBytes, response)
	return
}

// sendJSONRequestSuccessful sends a request for JSON information via sendJSONRequest and verifies
// the status code is success.
func sendJSONRequestSuccessful(request *http.Request, response interface{}) error {
	status, err := sendJSONRequest(request, response)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("request unsuccessful, http status %v, %v", status, http.StatusText(status))
	}
	return nil
}

// GitHubRequest is the payload for a GitHub PR creation API call, marshallable as JSON.
type GitHubRequest struct {
	Head                string `json:"head"`
	Base                string `json:"base"`
	Title               string `json:"title"`
	Body                string `json:"body"`
	MaintainerCanModify bool   `json:"maintainer_can_modify"`
	Draft               bool   `json:"draft"`
}

// GitHubResponse is a PR creation response from GitHub. It may represent success or failure.
type GitHubResponse struct {
	// GitHub success response:
	HTMLURL string `json:"html_url"`
	NodeID  string `json:"node_id"`
	Number  int    `json:"number"`

	// GitHub failure response:
	Message string               `json:"message"`
	Errors  []GitHubRequestError `json:"errors"`

	// AlreadyExists is set to true if the error message says the PR exists. Otherwise, false. For
	// our purposes, a GitHub failure response that indicates a PR already exists is not an error.
	AlreadyExists bool
}

type GitHubRequestError struct {
	Message string `json:"message"`
}

func PostGitHub(ownerRepo string, request *GitHubRequest, pat string) (response *GitHubResponse, err error) {
	prSubmitContent, err := json.MarshalIndent(request, "", "")
	if err != nil {
		return
	}
	fmt.Printf("Submitting payload: %s\n", prSubmitContent)

	httpRequest, err := http.NewRequest("POST", "https://api.github.com/repos/"+ownerRepo+"/pulls", bytes.NewReader(prSubmitContent))
	if err != nil {
		return
	}
	httpRequest.SetBasicAuth("", pat)

	response = &GitHubResponse{}
	statusCode, err := sendJSONRequest(httpRequest, response)
	if err != nil {
		return
	}

	switch statusCode {
	case http.StatusCreated:
		// 201 Created is the expected code if the PR is created. Do nothing.

	case http.StatusUnprocessableEntity:
		// 422 Unprocessable Entity may indicate the PR already exists. GitHub also gives us a response
		// that looks like this:
		/*
			{
				"message": "Validation Failed",
				"errors": [
					{
						"resource": "GitHubRequest",
						"code": "custom",
						"message": "A pull request already exists for microsoft-golang-bot:auto-merge/microsoft/main."
					}
				],
				"documentation_url": "https://docs.github.com/rest/reference/pulls#create-a-pull-request"
			}
		*/
		for _, e := range response.Errors {
			if strings.HasPrefix(e.Message, "A pull request already exists for ") {
				response.AlreadyExists = true
			}
		}
		if !response.AlreadyExists {
			err = fmt.Errorf(
				"response code %v may indicate PR already exists, but the error message is not recognized: %v",
				statusCode,
				response.Errors,
			)
		}

	default:
		err = fmt.Errorf("unexpected http status code: %v", statusCode)
	}
	return
}

func QueryGraphQL(pat string, query string, variables map[string]interface{}, result interface{}) error {
	queryBytes, err := json.Marshal(&struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables,omitempty"`
	}{
		query,
		variables,
	})
	if err != nil {
		return err
	}

	httpRequest, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(queryBytes))
	if err != nil {
		return err
	}
	httpRequest.SetBasicAuth("", pat)

	return sendJSONRequestSuccessful(httpRequest, result)
}

func MutateGraphQL(pat string, query string, variables map[string]interface{}) error {
	// Queries and mutations use the same API. But with a mutation, the results aren't useful to us.
	return QueryGraphQL(pat, query, variables, &struct{}{})
}

type ExistingPR struct {
	Title  string
	ID     string
	Number int
}

// FindExistingPR looks for a PR submitted to a target branch with a set of filters. Returns the
// result's graphql identity if one match is found, empty string if no matches are found, and an
// error if more than one match was found.
func FindExistingPR(r *GitHubRequest, head, target *Remote, headBranch, submitterUser, githubPAT string) (*ExistingPR, error) {
	prQuery := `query ($githubUser: String!, $headRefName: String!, $baseRefName: String!) {
		user(login: $githubUser) {
			pullRequests(states: OPEN, headRefName: $headRefName, baseRefName: $baseRefName, first: 5) {
				nodes {
					title
					id
					number
					headRepositoryOwner {
						login
					}
					baseRepository {
						owner {
							login
						}
						nameWithOwner
					}
				}
			}
		}
	}`
	variables := map[string]interface{}{
		"githubUser":  submitterUser,
		"headRefName": headBranch,
		"baseRefName": r.Base,
	}
	// Output structure from the query. We pull out some data to make sure our search result is what
	// we expect and avoid relying solely on the search engine query. This may be expanded in the
	// future to search for a specific PR among the search results, if necessary. (Needed if we want
	// to submit multiple, similar PRs from this bot.)
	//
	// Declared adjacent to the query because the query determines the structure.
	type PRNode struct {
		ExistingPR
		HeadRepositoryOwner struct {
			Login string
		}
		BaseRepository struct {
			Owner struct {
				Login string
			}
			NameWithOwner string
		}
	}
	result := &struct {
		// Note: Go encoding/json only detects exported properties (capitalized), but it does handle
		// matching it to the lowercase JSON for us.
		Data struct {
			User struct {
				PullRequests struct {
					Nodes    []PRNode
					PageInfo struct {
						HasNextPage bool
					}
				}
			}
		}
	}{}

	if err := QueryGraphQL(githubPAT, prQuery, variables, result); err != nil {
		return nil, err
	}
	fmt.Printf("%+v\n", result)

	// The user.pullRequests GitHub API isn't able to filter by repo name, so do it ourselves.
	result.Data.User.PullRequests.Nodes = selectFunc(
		result.Data.User.PullRequests.Nodes,
		func(n PRNode) bool {
			return n.BaseRepository.NameWithOwner == target.GetOwnerSlashRepo()
		})

	// Basic search result validation. We could be more flexible in some cases, but the goal here is
	// to detect an unknown state early so we don't end up doing something strange.

	if prNodes := len(result.Data.User.PullRequests.Nodes); prNodes > 1 {
		return nil, fmt.Errorf("expected 0/1 PR search result, found %v", prNodes)
	}
	if result.Data.User.PullRequests.PageInfo.HasNextPage {
		return nil, fmt.Errorf("expected 0/1 PR search result, but the results say there's another page")
	}

	if len(result.Data.User.PullRequests.Nodes) == 0 {
		return nil, nil
	}

	n := result.Data.User.PullRequests.Nodes[0]
	if foundHeadOwner := n.HeadRepositoryOwner.Login; foundHeadOwner != head.GetOwner() {
		return nil, fmt.Errorf("pull request head owner is %v, expected %v", foundHeadOwner, head.GetOwner())
	}
	if foundBaseOwner := n.BaseRepository.Owner.Login; foundBaseOwner != target.GetOwner() {
		return nil, fmt.Errorf("pull request base owner is %v, expected %v", foundBaseOwner, target.GetOwner())
	}
	return &n.ExistingPR, nil
}

// ApprovePR adds an approving review on the target GraphQL PR node ID. The review author is the user
// associated with the PAT.
func ApprovePR(nodeID string, pat string) error {
	return MutateGraphQL(
		pat,
		`mutation ($nodeID: ID!) {
				addPullRequestReview(input: {pullRequestId: $nodeID, event: APPROVE, body: "Thanks! Auto-approving."}) {
					clientMutationId
				}
			}`,
		map[string]interface{}{"nodeID": nodeID})
}

// EnablePRAutoMerge enables PR automerge on the target GraphQL PR node ID.
func EnablePRAutoMerge(nodeID string, pat string) error {
	return MutateGraphQL(
		pat,
		`mutation ($nodeID: ID!) {
			enablePullRequestAutoMerge(input: {pullRequestId: $nodeID, mergeMethod: MERGE}) {
				clientMutationId
			}
		}`,
		map[string]interface{}{"nodeID": nodeID})
}

// createRefspec makes a refspec that will fetch or push a branch "source" to "dest". The args must
// not already have a "refs/heads/" prefix.
func createRefspec(source, dest string) string {
	return fmt.Sprintf("refs/heads/%v:refs/heads/%v", source, dest)
}

// selectFunc returns a new slice where each element from s for which keep
// returns true has been copied.
//
// Capable of similar things as slices.DeleteFunc, but slices is not available
// in the Go version in the go.mod as of writing. selectFunc is simpler: it does
// not modify the existing slice.
func selectFunc[S ~[]E, E any](s S, keep func(E) bool) S {
	var r S
	for _, v := range s {
		if keep(v) {
			r = append(r, v)
		}
	}
	return r
}
