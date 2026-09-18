// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdopipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// QueueRequest contains the Azure Pipeline target and values for one queue operation. Callers own
// the allowlist that determines which definitions, refs, and parameters they may supply.
type QueueRequest struct {
	DefinitionID       int
	SourceBranch       string
	SourceVersion      string
	TemplateParameters map[string]any
	Variables          map[string]string
}

// Queue starts one Azure Pipeline run and returns its build ID.
func (c *Client) Queue(ctx context.Context, request QueueRequest) (int, error) {
	if request.DefinitionID <= 0 {
		return 0, errors.New("pipeline definition ID must be positive")
	}
	variables := request.Variables
	if variables == nil {
		variables = map[string]string{}
	}
	encodedVariables, err := json.Marshal(variables)
	if err != nil {
		return 0, fmt.Errorf("marshal Azure Pipeline variables: %w", err)
	}
	body, err := json.Marshal(struct {
		Definition struct {
			ID int `json:"id"`
		} `json:"definition"`
		SourceBranch       string         `json:"sourceBranch,omitempty"`
		SourceVersion      string         `json:"sourceVersion,omitempty"`
		TemplateParameters map[string]any `json:"templateParameters,omitempty"`
		Parameters         string         `json:"parameters"`
	}{
		Definition: struct {
			ID int `json:"id"`
		}{ID: request.DefinitionID},
		SourceBranch:       request.SourceBranch,
		SourceVersion:      request.SourceVersion,
		TemplateParameters: request.TemplateParameters,
		Parameters:         string(encodedVariables),
	})
	if err != nil {
		return 0, fmt.Errorf("marshal Azure Pipeline queue request: %w", err)
	}
	endpoint := c.buildsURL(url.Values{
		"definitionId": {strconv.Itoa(request.DefinitionID)},
		"api-version":  {"7.1-preview.7"},
	})
	var response struct {
		ID int `json:"id"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, endpoint, body, &response); err != nil {
		return 0, err
	}
	if response.ID <= 0 {
		return 0, errors.New("pipeline queue response has no build ID")
	}
	return response.ID, nil
}
