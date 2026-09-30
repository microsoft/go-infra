// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/microsoft/go-infra/buildmodel/dockerversions"
	azdopipeline "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/pipeline"
	azdorepo "github.com/microsoft/go-infra/cmd/releaseui/internal/azdo/repo"
)

const (
	azureBaseURL    = "https://dev.azure.com/dnceng"
	azureProject    = "internal"
	azureRepository = "microsoft-go-images"
	pipelineYAML    = "/eng/pipeline/go-docker-rolling-internal-pipeline.yml"
	versionsJSON    = "/src/microsoft/versions.json"
)

// AzureService implements the fixed Azure operations required by the Go-images process.
type AzureService struct {
	pipelines  *azdopipeline.Client
	repository *azdorepo.Client
	queue      QueueClient
}

// NewAzureService creates the fixed Go-images Azure service.
func NewAzureService(httpClient azdopipeline.HTTPDoer, tokens azdopipeline.TokenProvider) (*AzureService, error) {
	pipelines, err := azdopipeline.NewClient(azureBaseURL, azureProject, httpClient, tokens)
	if err != nil {
		return nil, err
	}
	repository, err := azdorepo.NewClient(azureBaseURL, azureProject, azureRepository, tokens)
	if err != nil {
		return nil, err
	}
	queue := &azureQueueClient{client: pipelines}
	return &AzureService{pipelines: pipelines, repository: repository, queue: queue}, nil
}

// Preflight verifies the fixed pipeline definition used by the Go-images process.
func (s *AzureService) Preflight(ctx context.Context) (string, error) {
	definition, err := s.pipelines.GetDefinition(ctx, DefinitionID)
	if err != nil {
		return "", err
	}
	if definition.Name != goImagesPipelineName || definition.QueueStatus != "enabled" ||
		definition.DefaultBranch != SourceBranch || definition.Repository != azureRepository ||
		definition.YAMLPath != pipelineYAML[1:] {

		return "", fmt.Errorf("pipeline 1023 does not match the read-only allowlist: %#v", definition)
	}
	return "Authenticated and verified direct go-images pipeline 1023. Planning is read-only; confirmed execution can queue only this target.", nil
}

// ResolveCurrentSource resolves and validates the exact current microsoft/main source.
func (s *AzureService) ResolveCurrentSource(ctx context.Context) (Source, error) {
	tip, err := s.repository.GetBranchTip(ctx, SourceBranch)
	if err != nil {
		return Source{}, err
	}
	pipeline, err := s.repository.GetFileAtCommit(ctx, pipelineYAML, tip.ObjectID)
	if err != nil {
		return Source{}, fmt.Errorf("read pipeline 1023 YAML at %s: %w", tip.ObjectID, err)
	}
	if err := ValidatePipelineParameterContract(pipeline); err != nil {
		return Source{}, fmt.Errorf("verify pipeline 1023 parameters at %s: %w", tip.ObjectID, err)
	}
	versions, err := s.VersionsAtCommit(ctx, tip.ObjectID)
	if err != nil {
		return Source{}, fmt.Errorf("read versions at %s: %w", tip.ObjectID, err)
	}
	return Source{Branch: tip.Name, Commit: tip.ObjectID, Versions: versions}, nil
}

// VersionsAtCommit returns the Microsoft Build of Go versions declared at commit.
func (s *AzureService) VersionsAtCommit(ctx context.Context, commit string) ([]string, error) {
	var model dockerversions.Versions
	if err := s.repository.GetJSONFileAtCommit(ctx, versionsJSON, commit, &model); err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(model))
	for _, version := range model {
		versions = append(versions, version.GoVersion().Full())
	}
	sort.Strings(versions)
	return versions, nil
}

// ValidateRollback validates one successful pipeline 1023 build as a rollback source.
func (s *AzureService) ValidateRollback(ctx context.Context, buildID int) (RollbackSource, error) {
	return ValidateRollbackSource(ctx, s.pipelines, s, buildID)
}

// NewRunService creates the queue-and-monitor service for one confirmed run.
func (s *AzureService) NewRunService(request RunRequest) (RunService, error) {
	return NewPipelineRunService(s.pipelines, s.queue, PipelineRunConfig{
		Mode:                 request.Mode,
		SessionID:            request.SessionID,
		ExecutionDigest:      request.ExecutionDigest,
		Versions:             request.Versions,
		SourceBuildID:        request.SourceBuildID,
		SourceVersion:        request.SourceVersion,
		VerifyMirrorCommit:   s.repository.VerifyCommit,
		MirrorPollInterval:   5 * time.Second,
		PollInterval:         5 * time.Second,
		PreviousQueueAttempt: request.PreviousQueueAttempt,
		ReconcileAttempts:    6,
		ReconcileInterval:    5 * time.Second,
	}, nil)
}

var _ ProcessService = (*AzureService)(nil)
