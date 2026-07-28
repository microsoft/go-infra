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

// GetJSONFileAtCommit decodes a JSON file from an exact repository commit into target.
func (c *Client) GetJSONFileAtCommit(ctx context.Context, path, commit string, target any) error {
	if path == "" || path[0] != '/' {
		return errors.New("repository file path must be absolute")
	}
	if !commitPattern.MatchString(commit) {
		return fmt.Errorf("invalid repository commit %q", commit)
	}
	if target == nil {
		return errors.New("repository file target must not be nil")
	}
	query := url.Values{
		"path":                             {path},
		"versionDescriptor.version":        {commit},
		"versionDescriptor.versionType":    {"commit"},
		"versionDescriptor.versionOptions": {"none"},
		"includeContent":                   {"true"},
		"api-version":                      {"7.1"},
	}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/git/repositories/" +
		url.PathEscape(c.repository) + "/items?" + query.Encode()
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
	var item struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &item); err != nil {
		return fmt.Errorf("decode Azure Repos item metadata for %s at %s: %w", path, commit, err)
	}
	if item.Content == "" {
		return fmt.Errorf("azure Repos returned empty file content for %s at %s", path, commit)
	}
	if err := json.Unmarshal([]byte(item.Content), target); err != nil {
		return fmt.Errorf("decode Azure Repos file %s at %s: %w", path, commit, err)
	}
	return nil
}
