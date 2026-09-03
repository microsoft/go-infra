// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	complianceAPIVersion = "3.0-preview.1"
	maxResponseSize      = 64 << 20
)

type complianceClient struct {
	baseURL    *url.URL
	httpClient *http.Client
	token      string
}

func newComplianceClient(baseURL, token string, httpClient *http.Client) (*complianceClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse compliance API URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("compliance API URL must use HTTP or HTTPS: %q", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("compliance API URL has no host: %q", baseURL)
	}
	if token == "" {
		return nil, errors.New("access token for Azure DevOps is empty")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &complianceClient{baseURL: parsed, httpClient: httpClient, token: token}, nil
}

func (c *complianceClient) getScope(ctx context.Context, scopeID string) (*scope, error) {
	query := url.Values{
		"$expand": {"assessmentGroups($expand=modifiedByUser,assessments($expand=modifiedByUser))"},
	}
	var result scope
	if err := c.doJSON(ctx, http.MethodGet, "scopes('"+encodeODataSegment(scopeID)+"')", query, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type assessmentWork struct {
	ID        string            `json:"id"`
	Answers   []json.RawMessage `json:"answers"`
	Work      []json.RawMessage `json:"work"`
	Questions []question        `json:"questions"`
}

type question struct {
	NodeID string          `json:"nodeId"`
	Raw    json.RawMessage `json:"-"`
}

func (q *question) UnmarshalJSON(data []byte) error {
	type questionFields question

	var fields questionFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*q = question(fields)
	q.Raw = append(q.Raw[:0], data...)
	return nil
}

type assessmentWorkRequest struct {
	Answers           []json.RawMessage `json:"answers"`
	UseLatestGraph    bool              `json:"useLatestGraph"`
	ScopeID           string            `json:"scopeId"`
	PolicyNodeID      string            `json:"policyNodeId"`
	AssessmentGroupID string            `json:"assessmentGroupId"`
}

func (c *complianceClient) getAssessmentWork(ctx context.Context, target *assessment, answers []json.RawMessage) (*assessmentWork, error) {
	request := assessmentWorkRequest{
		Answers:           answers,
		UseLatestGraph:    true,
		ScopeID:           target.ScopeID,
		PolicyNodeID:      target.PolicyNodeID,
		AssessmentGroupID: target.AssessmentGroupID,
	}
	query := url.Values{"$expand": {"work,questions"}}
	var result assessmentWork
	resource := "assessments('" + encodeODataSegment(target.ID) + "')/work.retrieve"
	if err := c.doJSON(ctx, http.MethodPost, resource, query, request, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *complianceClient) getPolicyQuestions(ctx context.Context, policyID string, graphVersion int64) ([]question, error) {
	query := url.Values{
		"includePaqQuestions": {"false"},
		"graphVersion":        {strconv.FormatInt(graphVersion, 10)},
	}
	resource := "questions/questions.getPolicyQuestions(policyId=('" + encodeODataSegment(policyID) + "'))"
	var result collection[question]
	if err := c.doJSON(ctx, http.MethodGet, resource, query, nil, &result); err != nil {
		return nil, err
	}
	return result.Value, nil
}

func (c *complianceClient) writeAssessment(ctx context.Context, target *assessment, answers []json.RawMessage) (*assessment, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(target.Raw, &payload); err != nil {
		return nil, fmt.Errorf("decode assessment %q for update: %w", target.Name, err)
	}
	encodedAnswers, err := json.Marshal(answers)
	if err != nil {
		return nil, fmt.Errorf("encode answers for assessment %q: %w", target.Name, err)
	}
	payload["answers"] = encodedAnswers
	delete(payload, "modifiedByUser")
	delete(payload, "createdByUser")
	for key := range payload {
		if strings.HasPrefix(key, "@odata.") {
			delete(payload, key)
		}
	}

	var result assessment
	if err := c.doJSON(ctx, http.MethodPut, "assessments", nil, payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

type session struct {
	ID             string            `json:"id"`
	AssessmentID   string            `json:"assessmentId"`
	State          string            `json:"state"`
	FailureMessage string            `json:"failureMessage"`
	IsInterrupted  bool              `json:"isInterrupted"`
	ProjectID      string            `json:"projectId"`
	WITConfigID    string            `json:"witConfigId"`
	AreaPath       string            `json:"areaPath"`
	IterationPath  string            `json:"iterationPath"`
	ServerURL      string            `json:"serverUrl"`
	WorkItems      []sessionWorkItem `json:"workItems"`
}

type sessionWorkItem struct {
	ItemType      string        `json:"itemType"`
	NodeID        string        `json:"nodeId"`
	WorkItemID    string        `json:"workItemId"`
	WorkItemState workItemState `json:"workItemState"`
}

type workItemState struct {
	State               string `json:"state"`
	IsCompletedCategory bool   `json:"isCompletedCategory"`
}

type collection[T any] struct {
	Value []T `json:"value"`
}

func (c *complianceClient) getSession(ctx context.Context, sessionID string) (*session, error) {
	query := url.Values{
		"$filter":              {"id eq '" + escapeODataString(sessionID) + "'"},
		"refreshWorkItemState": {"true"},
	}
	var result collection[session]
	if err := c.doJSON(ctx, http.MethodGet, "sessions", query, nil, &result); err != nil {
		return nil, err
	}
	if len(result.Value) != 1 {
		return nil, fmt.Errorf("session %q: got %d results, want 1", sessionID, len(result.Value))
	}
	return &result.Value[0], nil
}

func (c *complianceClient) generateSession(ctx context.Context, assessmentID string, payload map[string]json.RawMessage) error {
	resource := "assessments('" + encodeODataSegment(assessmentID) + "')/sessions.generate"
	return c.doJSON(ctx, http.MethodPost, resource, nil, payload, nil)
}

func (c *complianceClient) getLatestSession(ctx context.Context, assessmentID string) (*session, error) {
	query := url.Values{
		"$filter":              {"assessmentId eq '" + escapeODataString(assessmentID) + "'"},
		"$orderby":             {"createdDateTime desc"},
		"$top":                 {"1"},
		"refreshWorkItemState": {"true"},
	}
	var result collection[session]
	if err := c.doJSON(ctx, http.MethodGet, "sessions", query, nil, &result); err != nil {
		return nil, err
	}
	if len(result.Value) == 0 {
		return nil, nil
	}
	return &result.Value[0], nil
}

func (c *complianceClient) setWorkItemState(ctx context.Context, serverURL, workItemID, state string) error {
	parsedID, err := strconv.ParseInt(workItemID, 10, 64)
	if err != nil || parsedID <= 0 {
		return fmt.Errorf("invalid work item ID %q", workItemID)
	}
	if state == "" {
		return errors.New("work item state is empty")
	}

	endpoint, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("parse work item server URL: %w", err)
	}
	trustedTestHost := c.baseURL.Scheme == "http" && endpoint.Scheme == "http" && endpoint.Host == c.baseURL.Host
	hostname := strings.ToLower(endpoint.Hostname())
	trustedAzureDevOpsHost := endpoint.Scheme == "https" && (hostname == "dev.azure.com" || strings.HasSuffix(hostname, ".visualstudio.com"))
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Host == "" || !trustedTestHost && !trustedAzureDevOpsHost {
		return fmt.Errorf("untrusted work item server URL %q", serverURL)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/_apis/wit/workitems/" + strconv.FormatInt(parsedID, 10)
	endpoint.RawQuery = url.Values{"api-version": {"7.1"}}.Encode()

	patch := []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}{{Op: "replace", Path: "/fields/System.State", Value: state}}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("encode work item state update: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create work item state update: %w", err)
	}
	c.setAuthorization(request)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json-patch+json")
	request.Header.Set("X-TFS-FedAuthRedirect", "Suppress")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("update work item %s state: %w", workItemID, err)
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read work item %s update response: %w", workItemID, err)
	}
	if len(responseData) > maxResponseSize {
		return fmt.Errorf("read work item %s update response: body exceeds %d bytes", workItemID, maxResponseSize)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := jsonErrorMessage(response.Header.Get("Content-Type"), responseData)
		if message != "" {
			return fmt.Errorf("update work item %s state: HTTP %d: %s", workItemID, response.StatusCode, message)
		}
		return fmt.Errorf("update work item %s state: HTTP %d", workItemID, response.StatusCode)
	}
	return nil
}

func (c *complianceClient) doJSON(ctx context.Context, method, resource string, query url.Values, requestBody, responseBody any) error {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v2/_odata/" + resource
	endpoint.RawQuery = query.Encode()

	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode %s request: %w", resource, err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create %s request: %w", resource, err)
	}
	c.setAuthorization(request)
	request.Header.Set("Accept", "application/json;api-version="+complianceAPIVersion+";excludeUrls=true")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-TFS-FedAuthRedirect", "Suppress")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send %s request: %w", resource, err)
	}
	defer response.Body.Close()

	responseData, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return fmt.Errorf("read %s response: %w", resource, err)
	}
	if len(responseData) > maxResponseSize {
		return fmt.Errorf("read %s response: body exceeds %d bytes", resource, maxResponseSize)
	}

	mediaType, _, mediaTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := jsonErrorMessage(response.Header.Get("Content-Type"), responseData)
		if message != "" {
			return fmt.Errorf("%s %s: HTTP %d: %s", method, resource, response.StatusCode, message)
		}
		return fmt.Errorf("%s %s: HTTP %d", method, resource, response.StatusCode)
	}
	if responseBody == nil || len(responseData) == 0 {
		return nil
	}
	if mediaTypeErr != nil || mediaType != "application/json" {
		return fmt.Errorf("decode %s response: unexpected content type %q", resource, response.Header.Get("Content-Type"))
	}
	if err := json.Unmarshal(responseData, responseBody); err != nil {
		return fmt.Errorf("decode %s response: %w", resource, err)
	}
	return nil
}

func (c *complianceClient) setAuthorization(request *http.Request) {
	if isJWT(c.token) {
		request.Header.Set("Authorization", "Bearer "+c.token)
		return
	}
	request.SetBasicAuth("", c.token)
}

// isJWT distinguishes Azure CLI access tokens from opaque Azure DevOps PATs.
func isJWT(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

func jsonErrorMessage(contentType string, data []byte) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return ""
	}
	var apiError struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &apiError) != nil {
		return ""
	}
	if apiError.Message != "" {
		return apiError.Message
	}
	return apiError.Error.Message
}

func encodeODataSegment(value string) string {
	value = strings.ReplaceAll(value, "/", "∕")
	value = strings.ReplaceAll(value, "+", "➕")
	return escapeODataString(value)
}

func escapeODataString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
