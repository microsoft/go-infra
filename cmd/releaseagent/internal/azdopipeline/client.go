// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdopipeline provides the narrow Azure DevOps Build API surface needed by release UI
// workflows. It does not log tokens or request authorization headers.
package azdopipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseSize = 4 << 20

// TokenProvider acquires an Azure DevOps bearer token when a request is made.
type TokenProvider interface {
	Token(context.Context) (string, error)
}

// HTTPDoer is implemented by *http.Client and hermetic test clients.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client accesses one Azure DevOps project.
type Client struct {
	baseURL string
	project string
	http    HTTPDoer
	tokens  TokenProvider
}

// QueueRequest describes one Azure Pipelines run request.
type QueueRequest struct {
	DefinitionID  int
	SourceBranch  string
	SourceVersion string
	Parameters    map[string]string
	Variables     map[string]string
}

// Build is the release UI's stable view of an Azure Pipelines run.
type Build struct {
	ID                 int
	DefinitionID       int
	Status             string
	Result             string
	WebURL             string
	Parameters         map[string]string
	TemplateParameters map[string]any
	QueueTime          time.Time
}

// Definition is the allowlist-relevant metadata of an Azure Pipeline definition.
type Definition struct {
	ID            int
	Name          string
	QueueStatus   string
	DefaultBranch string
	YAMLPath      string
}

// RunState is the normalized lifecycle of an Azure Pipelines run.
type RunState string

const (
	RunStateWaiting   RunState = "waiting"
	RunStateRunning   RunState = "running"
	RunStateSucceeded RunState = "succeeded"
	RunStateFailed    RunState = "failed"
	RunStateCanceled  RunState = "canceled"
)

// HTTPError reports a non-success response without exposing authorization data.
type HTTPError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Azure DevOps request %s %s returned %d: %s", e.Method, e.URL, e.StatusCode, e.Body)
}

// NewClient validates and creates a client.
func NewClient(baseURL, project string, httpClient HTTPDoer, tokens TokenProvider) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Azure DevOps base URL %q", baseURL)
	}
	if project == "" {
		return nil, errors.New("azure DevOps project must not be empty")
	}
	if httpClient == nil {
		return nil, errors.New("azure DevOps HTTP client must not be nil")
	}
	if tokens == nil {
		return nil, errors.New("azure DevOps token provider must not be nil")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		project: project,
		http:    httpClient,
		tokens:  tokens,
	}, nil
}

// Queue starts a pipeline run and returns its assigned build ID.
func (c *Client) Queue(ctx context.Context, request QueueRequest) (*Build, error) {
	if request.DefinitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	variables, err := json.Marshal(request.Variables)
	if err != nil {
		return nil, fmt.Errorf("marshal pipeline variables: %w", err)
	}
	body := map[string]any{
		"definition":         map[string]int{"id": request.DefinitionID},
		"templateParameters": request.Parameters,
		// The Build API's legacy "parameters" field carries pipeline variables as a JSON string.
		"parameters": string(variables),
	}
	if request.SourceBranch != "" {
		body["sourceBranch"] = request.SourceBranch
	}
	if request.SourceVersion != "" {
		body["sourceVersion"] = request.SourceVersion
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal pipeline request: %w", err)
	}
	query := url.Values{
		"definitionId": {strconv.Itoa(request.DefinitionID)},
		"api-version":  {"7.1-preview.7"},
	}
	var response apiBuild
	if err := c.doJSON(ctx, http.MethodPost, c.buildsURL(query), bytes.NewReader(encoded), &response); err != nil {
		return nil, err
	}
	return response.build()
}

// Get returns one pipeline run.
func (c *Client) Get(ctx context.Context, buildID int) (*Build, error) {
	if buildID <= 0 {
		return nil, errors.New("build ID must be positive")
	}
	endpoint := c.buildsURL(nil) + "/" + strconv.Itoa(buildID) + "?api-version=7.1"
	var response apiBuild
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	return response.build()
}

// GetDefinition returns read-only metadata used to verify an allowlisted pipeline target.
func (c *Client) GetDefinition(ctx context.Context, definitionID int) (*Definition, error) {
	if definitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/build/definitions/" +
		strconv.Itoa(definitionID) + "?api-version=7.1"
	var response struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		QueueStatus string `json:"queueStatus"`
		Process     struct {
			YAMLPath string `json:"yamlFilename"`
		} `json:"process"`
		Repository struct {
			DefaultBranch string `json:"defaultBranch"`
		} `json:"repository"`
	}
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.ID != definitionID {
		return nil, fmt.Errorf("azure DevOps returned definition %d, expected %d", response.ID, definitionID)
	}
	return &Definition{
		ID:            response.ID,
		Name:          response.Name,
		QueueStatus:   response.QueueStatus,
		DefaultBranch: response.Repository.DefaultBranch,
		YAMLPath:      response.Process.YAMLPath,
	}, nil
}

// FindLatestByVariable returns the newest recent run of definitionID carrying name=value.
func (c *Client) FindLatestByVariable(ctx context.Context, definitionID int, name, value string) (*Build, error) {
	if definitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	if name == "" || value == "" {
		return nil, errors.New("correlation variable name and value are required")
	}
	builds, err := c.ListRecentByVariables(ctx, definitionID, map[string]string{name: value})
	if err != nil {
		return nil, err
	}
	if len(builds) == 0 {
		return nil, nil
	}
	return builds[0], nil
}

// ListRecentByVariables returns recent runs whose pipeline variables contain every required pair.
func (c *Client) ListRecentByVariables(
	ctx context.Context,
	definitionID int,
	required map[string]string,
) ([]*Build, error) {
	if definitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	if len(required) == 0 {
		return nil, errors.New("at least one required pipeline variable is needed")
	}
	for name, value := range required {
		if name == "" || value == "" {
			return nil, errors.New("required pipeline variable names and values must not be empty")
		}
	}
	builds, err := c.ListRecent(ctx, definitionID)
	if err != nil {
		return nil, err
	}
	var matches []*Build
	for _, build := range builds {
		match := true
		for name, value := range required {
			if build.Parameters[name] != value {
				match = false
				break
			}
		}
		if match {
			matches = append(matches, build)
		}
	}
	return matches, nil
}

// ListRecent returns up to 50 recent runs of a pipeline, newest first.
func (c *Client) ListRecent(ctx context.Context, definitionID int) ([]*Build, error) {
	if definitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	query := url.Values{
		"definitions": {strconv.Itoa(definitionID)},
		"queryOrder":  {"queueTimeDescending"},
		"$top":        {"50"},
		"api-version": {"7.1"},
	}
	var response struct {
		Value []apiBuild `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.buildsURL(query), nil, &response); err != nil {
		return nil, err
	}
	builds := make([]*Build, 0, len(response.Value))
	for _, candidate := range response.Value {
		build, err := candidate.build()
		if err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, nil
}

// State normalizes Azure status and result values for UI workflows.
func (b *Build) State() (RunState, error) {
	switch b.Status {
	case "notStarted", "postponed", "none", "":
		return RunStateWaiting, nil
	case "inProgress", "cancelling":
		return RunStateRunning, nil
	case "completed":
		switch b.Result {
		case "succeeded", "partiallySucceeded":
			return RunStateSucceeded, nil
		case "canceled":
			return RunStateCanceled, nil
		case "failed", "none", "":
			return RunStateFailed, nil
		default:
			return "", fmt.Errorf("unknown completed build result %q", b.Result)
		}
	default:
		return "", fmt.Errorf("unknown build status %q", b.Status)
	}
}

func (c *Client) buildsURL(query url.Values) string {
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/build/builds"
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, target any) error {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("acquire Azure DevOps token: %w", err)
	}
	if token == "" {
		return errors.New("azure DevOps token provider returned an empty token")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create Azure DevOps request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("send Azure DevOps request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read Azure DevOps response: %w", err)
	}
	if len(data) > maxResponseSize {
		return fmt.Errorf("azure DevOps response exceeds %d bytes", maxResponseSize)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody := strings.TrimSpace(string(data))
		responseBody = strings.ReplaceAll(responseBody, token, "[REDACTED]")
		return &HTTPError{
			StatusCode: response.StatusCode,
			Method:     method,
			URL:        endpoint,
			Body:       responseBody,
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode Azure DevOps response: %w", err)
	}
	return nil
}

type apiBuild struct {
	ID                 int            `json:"id"`
	Status             string         `json:"status"`
	Result             string         `json:"result"`
	Parameters         string         `json:"parameters"`
	TemplateParameters map[string]any `json:"templateParameters"`
	QueueTime          time.Time      `json:"queueTime"`
	Definition         struct {
		ID int `json:"id"`
	} `json:"definition"`
	Links json.RawMessage `json:"_links"`
}

func (b apiBuild) build() (*Build, error) {
	if b.ID <= 0 {
		return nil, errors.New("azure DevOps response has no build ID")
	}
	parameters, err := decodeParameters(b.Parameters)
	if err != nil {
		return nil, fmt.Errorf("decode build %d parameters: %w", b.ID, err)
	}
	return &Build{
		ID:                 b.ID,
		DefinitionID:       b.Definition.ID,
		Status:             b.Status,
		Result:             b.Result,
		WebURL:             decodeWebURL(b.Links),
		Parameters:         parameters,
		TemplateParameters: b.TemplateParameters,
		QueueTime:          b.QueueTime,
	}, nil
}

func decodeParameters(parameters string) (map[string]string, error) {
	result := make(map[string]string)
	if parameters == "" {
		return result, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parameters), &values); err != nil {
		return nil, err
	}
	for name, raw := range values {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			result[name] = value
			continue
		}
		var wrapped struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Value == "" {
			continue
		}
		result[name] = wrapped.Value
	}
	return result, nil
}

func decodeWebURL(links json.RawMessage) string {
	var value struct {
		Web struct {
			Href string `json:"href"`
		} `json:"web"`
	}
	if err := json.Unmarshal(links, &value); err != nil {
		return ""
	}
	return value.Web.Href
}
