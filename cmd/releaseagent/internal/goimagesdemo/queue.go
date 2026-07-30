// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimagesdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdopipeline"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const maxResponseSize = 4 << 20

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HTTPQueueClient can issue exactly one kind of mutation: queue definition 1492 with the fixed
// unofficial dev/ payload. It cannot accept a browser-controlled definition, branch, or prefix.
type HTTPQueueClient struct {
	baseURL string
	project string
	http    azdopipeline.HTTPDoer
	tokens  azdopipeline.TokenProvider
}

// NewHTTPQueueClient validates and creates the dedicated unofficial-demo queue client.
func NewHTTPQueueClient(
	baseURL,
	project string,
	httpClient azdopipeline.HTTPDoer,
	tokens azdopipeline.TokenProvider,
) (*HTTPQueueClient, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Azure DevOps base URL %q", baseURL)
	}
	if project == "" || httpClient == nil || tokens == nil {
		return nil, errors.New("azure DevOps project, HTTP client, and token provider are required")
	}
	return &HTTPQueueClient{
		baseURL: strings.TrimRight(baseURL, "/"), project: project, http: httpClient, tokens: tokens,
	}, nil
}

// QueueUnofficialDemo queues the fixed definition, branch, parameters, and correlation variables.
func (c *HTTPQueueClient) QueueUnofficialDemo(ctx context.Context, request QueueRequest) (int, error) {
	if !commitPattern.MatchString(request.SourceVersion) {
		return 0, fmt.Errorf("invalid unofficial demo source commit %q", request.SourceVersion)
	}
	if request.SessionID == "" || !digestPattern.MatchString(request.ExecutionDigest) {
		return 0, errors.New("unofficial demo session ID and 64-character execution digest are required")
	}
	if id, err := strconv.Atoi(request.SourceBuildID); err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid unofficial demo source build ID %q", request.SourceBuildID)
	}
	var versions []string
	if err := json.Unmarshal([]byte(request.VersionSet), &versions); err != nil || len(versions) == 0 {
		return 0, errors.New("unofficial demo canonical version set is invalid")
	}
	variables, err := json.Marshal(map[string]string{
		correlationVariable:     request.SessionID,
		executionDigestVariable: request.ExecutionDigest,
		versionsVariable:        request.VersionSet,
		sourceBuildVariable:     request.SourceBuildID,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal unofficial demo variables: %w", err)
	}
	body, err := json.Marshal(map[string]any{
		"definition":         map[string]int{"id": DefinitionID},
		"sourceBranch":       SourceBranch,
		"sourceVersion":      request.SourceVersion,
		"templateParameters": releasesteps.GoImagesUnofficialDemoPipelineParameters(),
		// The Build API's legacy parameters field carries pipeline variables as a JSON string.
		"parameters": string(variables),
	})
	if err != nil {
		return 0, fmt.Errorf("marshal unofficial demo request: %w", err)
	}
	endpoint := c.baseURL + "/" + url.PathEscape(c.project) + "/_apis/build/builds?" + url.Values{
		"definitionId": {strconv.Itoa(DefinitionID)},
		"api-version":  {"7.1-preview.7"},
	}.Encode()
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire Azure DevOps token: %w", err)
	}
	if token == "" {
		return 0, errors.New("azure DevOps token provider returned an empty token")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create unofficial demo queue request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return 0, fmt.Errorf("send unofficial demo queue request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return 0, fmt.Errorf("read unofficial demo queue response: %w", err)
	}
	if len(data) > maxResponseSize {
		return 0, fmt.Errorf("unofficial demo queue response exceeds %d bytes", maxResponseSize)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody := strings.ReplaceAll(strings.TrimSpace(string(data)), token, "[REDACTED]")
		return 0, &azdopipeline.HTTPError{
			StatusCode: response.StatusCode,
			Method:     http.MethodPost,
			URL:        endpoint,
			Body:       responseBody,
		}
	}
	var build struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(data, &build); err != nil {
		return 0, fmt.Errorf("decode unofficial demo queue response: %w", err)
	}
	if build.ID <= 0 {
		return 0, errors.New("unofficial demo queue response has no build ID")
	}
	return build.ID, nil
}

var _ QueueClient = (*HTTPQueueClient)(nil)
