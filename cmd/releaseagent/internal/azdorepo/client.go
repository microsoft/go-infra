// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdorepo provides the narrow read-only Azure Repos API surface needed by release UI
// workflows. It does not log tokens or request authorization headers.
package azdorepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxResponseSize = 4 << 20

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// BranchTip is the exact commit currently referenced by an Azure Repos branch.
type BranchTip struct {
	Name     string
	ObjectID string
}

// TokenProvider acquires an Azure DevOps bearer token when a request is made.
type TokenProvider interface {
	Token(context.Context) (string, error)
}

// HTTPDoer is implemented by *http.Client and hermetic test clients.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client accesses one repository in an Azure DevOps project.
type Client struct {
	baseURL    string
	project    string
	repository string
	http       HTTPDoer
	tokens     TokenProvider
}

// NewClient validates and creates a read-only repository client.
func NewClient(baseURL, project, repository string, httpClient HTTPDoer, tokens TokenProvider) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Azure DevOps base URL %q", baseURL)
	}
	if project == "" || repository == "" {
		return nil, errors.New("azure DevOps project and repository must not be empty")
	}
	if httpClient == nil || tokens == nil {
		return nil, errors.New("azure DevOps HTTP client and token provider must not be nil")
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		project:    project,
		repository: repository,
		http:       httpClient,
		tokens:     tokens,
	}, nil
}

// GetFileAtCommit returns a file from an exact repository commit.
func (c *Client) GetFileAtCommit(ctx context.Context, path, commit string) ([]byte, error) {
	if !commitPattern.MatchString(commit) {
		return nil, fmt.Errorf("invalid repository commit %q", commit)
	}
	return c.getFile(ctx, path, commit, "commit")
}

// VerifyCommit confirms that an exact commit is available in the repository.
func (c *Client) VerifyCommit(ctx context.Context, commit string) error {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("invalid repository commit %q", commit)
	}
	query := url.Values{"api-version": {"7.1"}}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/git/repositories/" +
		url.PathEscape(c.repository) + "/commits/" + url.PathEscape(commit) + "?" + query.Encode()
	var response struct {
		CommitID string `json:"commitId"`
	}
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return err
	}
	if actual := strings.ToLower(response.CommitID); actual != commit {
		return fmt.Errorf("azure Repos returned commit %q, expected %q", response.CommitID, commit)
	}
	return nil
}

// GetFileAtBranch returns a file from the current tip of a repository branch.
func (c *Client) GetFileAtBranch(ctx context.Context, path, branch string) ([]byte, error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" || strings.ContainsAny(branch, "\x00\r\n") {
		return nil, fmt.Errorf("invalid repository branch %q", branch)
	}
	return c.getFile(ctx, path, branch, "branch")
}

// GetBranchTip resolves a branch to one exact commit. The full refs/heads/ name is returned so
// callers can bind an execution plan to both the ref and object ID.
func (c *Client) GetBranchTip(ctx context.Context, branch string) (BranchTip, error) {
	branch = strings.TrimSpace(branch)
	shortBranch := strings.TrimPrefix(branch, "refs/heads/")
	if shortBranch == "" || strings.ContainsAny(shortBranch, "\x00\r\n") {
		return BranchTip{}, fmt.Errorf("invalid repository branch %q", branch)
	}
	expectedName := "refs/heads/" + shortBranch
	query := url.Values{
		"filter":      {"heads/" + shortBranch},
		"api-version": {"7.1"},
	}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/git/repositories/" +
		url.PathEscape(c.repository) + "/refs?" + query.Encode()
	var response struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
		} `json:"value"`
	}
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return BranchTip{}, err
	}
	if len(response.Value) != 1 || response.Value[0].Name != expectedName {
		return BranchTip{}, fmt.Errorf(
			"azure Repos returned %d refs for branch %q, expected exactly %q",
			len(response.Value),
			branch,
			expectedName,
		)
	}
	objectID := strings.ToLower(response.Value[0].ObjectID)
	if !commitPattern.MatchString(objectID) {
		return BranchTip{}, fmt.Errorf("azure Repos branch %q has invalid object ID %q", expectedName, response.Value[0].ObjectID)
	}
	return BranchTip{Name: expectedName, ObjectID: objectID}, nil
}

// GetJSONFileAtCommit decodes a JSON file from an exact repository commit into target.
func (c *Client) GetJSONFileAtCommit(ctx context.Context, path, commit string, target any) error {
	data, err := c.GetFileAtCommit(ctx, path, commit)
	if err != nil {
		return err
	}
	if target == nil {
		return errors.New("repository file target must not be nil")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode Azure Repos file %s at %s: %w", path, commit, err)
	}
	return nil
}

func (c *Client) getFile(ctx context.Context, path, version, versionType string) ([]byte, error) {
	if path == "" || path[0] != '/' {
		return nil, errors.New("repository file path must be absolute")
	}
	query := url.Values{
		"path":                             {path},
		"versionDescriptor.version":        {version},
		"versionDescriptor.versionType":    {versionType},
		"versionDescriptor.versionOptions": {"none"},
		"includeContent":                   {"true"},
		"api-version":                      {"7.1"},
	}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/git/repositories/" +
		url.PathEscape(c.repository) + "/items?" + query.Encode()
	var item struct {
		Content string `json:"content"`
	}
	if err := c.getJSON(ctx, endpoint, &item); err != nil {
		return nil, err
	}
	if item.Content == "" {
		return nil, fmt.Errorf("azure Repos returned empty file content for %s at %s %s", path, versionType, version)
	}
	return []byte(item.Content), nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("acquire Azure DevOps token: %w", err)
	}
	if token == "" {
		return errors.New("azure DevOps token provider returned an empty token")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Azure Repos request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("send Azure Repos request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read Azure Repos response: %w", err)
	}
	if len(data) > maxResponseSize {
		return fmt.Errorf("azure Repos response exceeds %d bytes", maxResponseSize)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body := strings.ReplaceAll(strings.TrimSpace(string(data)), token, "[REDACTED]")
		return fmt.Errorf("azure Repos request returned %d: %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode Azure Repos response: %w", err)
	}
	return nil
}
