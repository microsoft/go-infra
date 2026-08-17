// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdopipeline adapts the Azure DevOps Build SDK and provides the few additional REST
// operations needed by release UI workflows. It does not log tokens or authorization headers.
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

	"github.com/microsoft/azure-devops-go-api/azuredevops"
	azdobuild "github.com/microsoft/azure-devops-go-api/azuredevops/build"
)

const (
	maxResponseSize   = 4 << 20
	sdkRequestTimeout = 3 * time.Minute
)

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
	baseURL             string
	project             string
	http                HTTPDoer
	tokens              TokenProvider
	newDefinitionClient func(context.Context) (definitionClient, string, error)
	newTimelineClient   func(context.Context) (timelineClient, string, error)
}

type definitionClient interface {
	GetDefinition(context.Context, azdobuild.GetDefinitionArgs) (*azdobuild.BuildDefinition, error)
}

type timelineClient interface {
	GetBuildTimeline(context.Context, azdobuild.GetBuildTimelineArgs) (*azdobuild.Timeline, error)
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
	client := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		project: project,
		http:    httpClient,
		tokens:  tokens,
	}
	client.newDefinitionClient = client.createDefinitionClient
	client.newTimelineClient = client.createTimelineClient
	return client, nil
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
	client, token, err := c.newTimelineClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps Build client: %w", redactError(err, token))
	}
	response, err := client.GetBuildTimeline(ctx, azdobuild.GetBuildTimelineArgs{
		Project: &c.project, BuildId: &buildID,
	})
	if err != nil {
		return nil, fmt.Errorf("get Azure DevOps build timeline: %w", redactError(err, token))
	}
	timeline := &Timeline{}
	if response == nil || response.Records == nil {
		return timeline, nil
	}
	timeline.Records = make([]TimelineRecord, 0, len(*response.Records))
	for _, record := range *response.Records {
		mapped := TimelineRecord{
			Type: stringValue(record.Type), Name: stringValue(record.Name),
			State: enumValue(record.State), Result: enumValue(record.Result),
		}
		if record.Id != nil {
			mapped.ID = record.Id.String()
		}
		if record.ParentId != nil {
			mapped.ParentID = record.ParentId.String()
		}
		if record.Order != nil {
			mapped.Order = *record.Order
		}
		timeline.Records = append(timeline.Records, mapped)
	}
	return timeline, nil
}

// GetDefinition returns read-only metadata used to verify an allowlisted pipeline target.
func (c *Client) GetDefinition(ctx context.Context, definitionID int) (*Definition, error) {
	if definitionID <= 0 {
		return nil, errors.New("pipeline definition ID must be positive")
	}
	client, token, err := c.newDefinitionClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create Azure DevOps Build client: %w", redactError(err, token))
	}
	response, err := client.GetDefinition(ctx, azdobuild.GetDefinitionArgs{
		Project: &c.project, DefinitionId: &definitionID,
	})
	if err != nil {
		return nil, fmt.Errorf("get Azure DevOps pipeline definition: %w", redactError(err, token))
	}
	if response == nil || response.Id == nil || *response.Id != definitionID {
		return nil, fmt.Errorf("azure DevOps returned an unexpected definition, expected %d", definitionID)
	}
	return &Definition{
		ID:            *response.Id,
		Name:          stringValue(response.Name),
		QueueStatus:   enumValue(response.QueueStatus),
		DefaultBranch: repositoryValue(response.Repository, func(repository *azdobuild.BuildRepository) *string { return repository.DefaultBranch }),
		Repository:    repositoryValue(response.Repository, func(repository *azdobuild.BuildRepository) *string { return repository.Name }),
		YAMLPath:      processString(response.Process, "yamlFilename"),
	}, nil
}

func (c *Client) createDefinitionClient(ctx context.Context) (definitionClient, string, error) {
	client, token, err := c.createBuildClient(ctx)
	return client, token, err
}

func (c *Client) createTimelineClient(ctx context.Context) (timelineClient, string, error) {
	client, token, err := c.createBuildClient(ctx)
	return client, token, err
}

func (c *Client) createBuildClient(ctx context.Context) (azdobuild.Client, string, error) {
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
	if httpClient, ok := c.http.(*http.Client); ok && httpClient.Timeout > 0 {
		timeout = httpClient.Timeout
	}
	connection.Timeout = &timeout
	client, err := azdobuild.NewClient(ctx, connection)
	return client, token, err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func enumValue[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func repositoryValue(repository *azdobuild.BuildRepository, field func(*azdobuild.BuildRepository) *string) string {
	if repository == nil {
		return ""
	}
	return stringValue(field(repository))
}

func processString(process any, name string) string {
	fields, ok := process.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := fields[name].(string)
	return value
}

func redactError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[REDACTED]"))
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
