// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdopipeline provides the narrow Azure DevOps Build API surface needed by release UI
// workflows. It does not log tokens or request authorization headers.
package azdopipeline

import (
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

// Build is the release UI's stable view of an Azure Pipelines run.
type Build struct {
	ID                 int
	DefinitionID       int
	Status             string
	Result             string
	WebURL             string
	SourceBranch       string
	SourceVersion      string
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
	Repository    string
	YAMLPath      string
}

// Timeline is the execution hierarchy of an Azure Pipelines build.
type Timeline struct {
	Records []TimelineRecord
}

// TimelineRecord is one stage, phase, job, task, or other timeline node. ParentID links records
// into the hierarchy returned by Azure DevOps.
type TimelineRecord struct {
	ID       string
	ParentID string
	Type     string
	Name     string
	State    string
	Result   string
	Order    int
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

// Get returns one pipeline run.
func (c *Client) Get(ctx context.Context, buildID int) (*Build, error) {
	if buildID <= 0 {
		return nil, errors.New("build ID must be positive")
	}
	endpoint := c.buildsURL(nil) + "/" + strconv.Itoa(buildID) + "?api-version=7.1"
	var response apiBuild
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, err
	}
	return response.build()
}

// GetTimeline returns the current stage, job, and task hierarchy for one pipeline run.
func (c *Client) GetTimeline(ctx context.Context, buildID int) (*Timeline, error) {
	if buildID <= 0 {
		return nil, errors.New("build ID must be positive")
	}
	endpoint := c.buildsURL(nil) + "/" + strconv.Itoa(buildID) + "/timeline?api-version=7.1"
	var response struct {
		Records []struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
			Type     string `json:"type"`
			Name     string `json:"name"`
			State    string `json:"state"`
			Result   string `json:"result"`
			Order    int    `json:"order"`
		} `json:"records"`
	}
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, err
	}
	timeline := &Timeline{Records: make([]TimelineRecord, 0, len(response.Records))}
	for _, record := range response.Records {
		timeline.Records = append(timeline.Records, TimelineRecord{
			ID: record.ID, ParentID: record.ParentID, Type: record.Type, Name: record.Name,
			State: record.State, Result: record.Result, Order: record.Order,
		})
	}
	return timeline, nil
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
			Name          string `json:"name"`
		} `json:"repository"`
	}
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
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
		Repository:    response.Repository.Name,
		YAMLPath:      response.Process.YAMLPath,
	}, nil
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
	if err := c.getJSON(ctx, c.buildsURL(query), &response); err != nil {
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
		return fmt.Errorf("create Azure DevOps request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
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
			Method:     http.MethodGet,
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
	SourceBranch       string         `json:"sourceBranch"`
	SourceVersion      string         `json:"sourceVersion"`
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
		SourceBranch:       b.SourceBranch,
		SourceVersion:      b.SourceVersion,
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
