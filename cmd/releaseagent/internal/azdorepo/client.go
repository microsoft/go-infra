// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdorepo adapts the Azure DevOps Git SDK to the narrow read-only repository surface
// needed by release UI workflows. It does not log tokens or authorization headers.
package azdorepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	azdogit "github.com/microsoft/azure-devops-go-api/azuredevops/git"
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

const sdkRequestTimeout = 3 * time.Minute

// BranchTip is the exact commit currently referenced by an Azure Repos branch.
type BranchTip struct {
	Name     string
	ObjectID string
}

// TokenProvider acquires an Azure DevOps bearer token when a request is made.
type TokenProvider interface {
	Token(context.Context) (string, error)
}

// Client accesses one repository in an Azure DevOps project.
type Client struct {
	baseURL      string
	project      string
	repository   string
	tokens       TokenProvider
	newGitClient func(context.Context) (gitClient, string, error)
}

type gitClient interface {
	GetCommit(context.Context, azdogit.GetCommitArgs) (*azdogit.GitCommit, error)
	GetItem(context.Context, azdogit.GetItemArgs) (*azdogit.GitItem, error)
	GetRefs(context.Context, azdogit.GetRefsArgs) (*azdogit.GetRefsResponseValue, error)
}

// NewClient validates and creates a read-only repository client.
func NewClient(baseURL, project, repository string, tokens TokenProvider) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Azure DevOps base URL %q", baseURL)
	}
	if project == "" || repository == "" {
		return nil, errors.New("azure DevOps project and repository must not be empty")
	}
	if tokens == nil {
		return nil, errors.New("azure DevOps token provider must not be nil")
	}
	client := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		project:    project,
		repository: repository,
		tokens:     tokens,
	}
	client.newGitClient = client.createGitClient
	return client, nil
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
	client, token, err := c.newGitClient(ctx)
	if err != nil {
		return fmt.Errorf("create Azure Repos Git client: %w", redactError(err, token))
	}
	response, err := client.GetCommit(ctx, azdogit.GetCommitArgs{
		CommitId: &commit, RepositoryId: &c.repository, Project: &c.project,
	})
	if err != nil {
		return fmt.Errorf("get Azure Repos commit: %w", redactError(err, token))
	}
	actual := ""
	if response != nil && response.CommitId != nil {
		actual = strings.ToLower(*response.CommitId)
	}
	if actual != commit {
		return fmt.Errorf("azure Repos returned commit %q, expected %q", actual, commit)
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
	filter := "heads/" + shortBranch
	client, token, err := c.newGitClient(ctx)
	if err != nil {
		return BranchTip{}, fmt.Errorf("create Azure Repos Git client: %w", redactError(err, token))
	}
	response, err := client.GetRefs(ctx, azdogit.GetRefsArgs{
		RepositoryId: &c.repository, Project: &c.project, Filter: &filter,
	})
	if err != nil {
		return BranchTip{}, fmt.Errorf("get Azure Repos branch: %w", redactError(err, token))
	}
	if response == nil || len(response.Value) != 1 || response.Value[0].Name == nil || *response.Value[0].Name != expectedName {
		count := 0
		if response != nil {
			count = len(response.Value)
		}
		return BranchTip{}, fmt.Errorf(
			"azure Repos returned %d refs for branch %q, expected exactly %q",
			count,
			branch,
			expectedName,
		)
	}
	objectID := ""
	if response.Value[0].ObjectId != nil {
		objectID = strings.ToLower(*response.Value[0].ObjectId)
	}
	if !commitPattern.MatchString(objectID) {
		return BranchTip{}, fmt.Errorf("azure Repos branch %q has invalid object ID %q", expectedName, objectID)
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
	client, token, err := c.newGitClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure Repos Git client: %w", redactError(err, token))
	}
	versionKind := azdogit.GitVersionType(versionType)
	versionOptions := azdogit.GitVersionOptionsValues.None
	includeContent := true
	item, err := client.GetItem(ctx, azdogit.GetItemArgs{
		RepositoryId: &c.repository,
		Project:      &c.project,
		Path:         &path,
		VersionDescriptor: &azdogit.GitVersionDescriptor{
			Version: &version, VersionType: &versionKind, VersionOptions: &versionOptions,
		},
		IncludeContent: &includeContent,
	})
	if err != nil {
		return nil, fmt.Errorf("get Azure Repos file: %w", redactError(err, token))
	}
	if item == nil || item.Content == nil || *item.Content == "" {
		return nil, fmt.Errorf("azure Repos returned empty file content for %s at %s %s", path, versionType, version)
	}
	return []byte(*item.Content), nil
}

func (c *Client) createGitClient(ctx context.Context) (gitClient, string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("acquire Azure DevOps token: %w", err)
	}
	if token == "" {
		return nil, "", errors.New("azure DevOps token provider returned an empty token")
	}
	connection := azuredevops.NewAnonymousConnection(c.baseURL)
	connection.AuthorizationString = "Bearer " + token
	timeout := sdkRequestTimeout
	connection.Timeout = &timeout
	client, err := azdogit.NewClient(ctx, connection)
	return client, token, err
}

func redactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
}
