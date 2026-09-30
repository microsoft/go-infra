// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type pipelineQueuer interface {
	Queue(context.Context, azdopipeline.QueueRequest) (int, error)
}

type azureQueueClient struct {
	client pipelineQueuer
}

// QueueRelease queues the fixed definition and branch with one mode-derived parameter set.
func (c *azureQueueClient) QueueRelease(ctx context.Context, request QueueRequest) (int, error) {
	if !sourceCommitPattern.MatchString(request.SourceVersion) {
		return 0, fmt.Errorf("invalid go-images release source commit %q", request.SourceVersion)
	}
	if request.SessionID == "" || !digestPattern.MatchString(request.ExecutionDigest) {
		return 0, errors.New("go-images release session ID and 64-character execution digest are required")
	}
	parameters, err := PipelineParameters(request.Mode, request.SourceBuildID)
	if err != nil {
		return 0, err
	}
	var versions []string
	if err := json.Unmarshal([]byte(request.VersionSet), &versions); err != nil || len(versions) == 0 {
		return 0, errors.New("go-images release canonical version set is invalid")
	}
	variables := map[string]string{
		correlationVariable:     request.SessionID,
		executionDigestVariable: request.ExecutionDigest,
		modeVariable:            string(request.Mode),
		versionsVariable:        request.VersionSet,
		sourceBuildVariable:     request.SourceBuildID,
	}
	templateParameters := make(map[string]any, len(parameters))
	for name, value := range parameters {
		templateParameters[name] = value
	}
	return c.client.Queue(ctx, azdopipeline.QueueRequest{
		DefinitionID:       DefinitionID,
		SourceBranch:       SourceBranch,
		SourceVersion:      request.SourceVersion,
		TemplateParameters: templateParameters,
		Variables:          variables,
	})
}

var _ QueueClient = (*azureQueueClient)(nil)
