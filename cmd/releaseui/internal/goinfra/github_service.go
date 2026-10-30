// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goinfra provides the fixed GitHub mutation boundary for go-infra releases.
package goinfra

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/microsoft/go-infra/cmd/releaseui/internal/githubclient"
	"go.yaml.in/yaml/v4"
)

const (
	Owner            = "microsoft"
	Repository       = "go-infra"
	DefaultRef       = "main"
	ReleaseLabel     = "release-on-merge"
	WorkflowFile     = "create-go-infra-patch-release.yml"
	WorkflowPath     = ".github/workflows/" + WorkflowFile
	CorrelationInput = "release-ui-correlation-id"
	workflowRunName  = "${{ inputs.release-ui-correlation-id || 'Create go-infra patch release' }}"
)

// Service performs allowlisted GitHub reads and mutations for go-infra.
type Service struct {
	client *githubclient.Client
}

// NewGitHubService creates the fixed go-infra policy adapter over a generic GitHub client.
func NewGitHubService(client *githubclient.Client) (*Service, error) {
	if client == nil {
		return nil, errors.New("GitHub client is nil")
	}
	return &Service{client: client}, nil
}

// WorkflowRun is one allowlisted go-infra patch-release workflow run.
type WorkflowRun = githubclient.WorkflowRun

// PullRequest contains the immutable metadata used to confirm a release-on-merge plan.
type PullRequest = githubclient.PullRequest

func repositoryTarget() githubclient.Repository {
	return githubclient.Repository{Owner: Owner, Name: Repository}
}

func workflowTarget() githubclient.Workflow {
	return githubclient.Workflow{
		Repository: repositoryTarget(), File: WorkflowFile, Ref: DefaultRef,
		CorrelationInput: CorrelationInput,
	}
}

// Preflight verifies readiness for manual patch releases on the fixed workflow target.
func (s *Service) Preflight(ctx context.Context) (string, error) {
	if err := s.verifyTargets(ctx); err != nil {
		return "", err
	}
	workflowYAML, err := s.client.GetFile(ctx, repositoryTarget(), WorkflowPath, DefaultRef)
	if err != nil {
		return "", fmt.Errorf("read go-infra release workflow contract: %w", err)
	}
	if err := validateWorkflowContract(workflowYAML); err != nil {
		return "", err
	}
	return "Authenticated to GitHub and verified microsoft/go-infra main and its active patch-release workflow.", nil
}

// PreflightReleaseOnMerge verifies the targets needed to apply the release-on-merge label.
// It does not require the correlation inputs used only by manual workflow dispatch.
func (s *Service) PreflightReleaseOnMerge(ctx context.Context) (string, error) {
	if err := s.verifyTargets(ctx); err != nil {
		return "", err
	}
	return "Authenticated to GitHub and verified the go-infra release-on-merge target.", nil
}

func (s *Service) verifyTargets(ctx context.Context) error {
	if err := s.client.AuthStatus(ctx); err != nil {
		return err
	}
	repository, err := s.client.GetRepository(ctx, repositoryTarget())
	if err != nil {
		return fmt.Errorf("verify go-infra repository: %w", err)
	}
	if repository.FullName != Owner+"/"+Repository || repository.DefaultBranch != DefaultRef {
		return fmt.Errorf("go-infra repository does not match the release allowlist: %#v", repository)
	}
	workflow, err := s.client.GetWorkflow(ctx, workflowTarget())
	if err != nil {
		return fmt.Errorf("verify go-infra release workflow: %w", err)
	}
	if workflow.Path != WorkflowPath || workflow.State != "active" {
		return fmt.Errorf("go-infra release workflow does not match the allowlist: %#v", workflow)
	}
	return nil
}

// GetPullRequest validates an open, non-fork pull request targeting main.
func (s *Service) GetPullRequest(ctx context.Context, number int) (PullRequest, error) {
	response, err := s.client.GetPullRequest(ctx, repositoryTarget(), number)
	if err != nil {
		return PullRequest{}, fmt.Errorf("read go-infra pull request %d: %w", number, err)
	}
	if response.Number != number || response.State != "open" || response.Merged {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d is not open", number)
	}
	if response.BaseRef != DefaultRef {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d targets %q, expected %q", number, response.BaseRef, DefaultRef)
	}
	if response.Fork {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d comes from a fork; release-on-merge cannot publish from fork-triggered runs", number)
	}
	if len(response.HeadSHA) != 40 {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d has invalid head SHA %q", number, response.HeadSHA)
	}
	if _, err := hex.DecodeString(response.HeadSHA); err != nil {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d has invalid head SHA %q", number, response.HeadSHA)
	}
	return response, nil
}

// AddReleaseOnMergeLabel revalidates a pull request and applies the fixed release label.
func (s *Service) AddReleaseOnMergeLabel(ctx context.Context, number int, expectedHeadSHA string) (PullRequest, error) {
	pullRequest, err := s.GetPullRequest(ctx, number)
	if err != nil {
		return PullRequest{}, err
	}
	if pullRequest.HeadSHA != expectedHeadSHA {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d changed after the plan was prepared", number)
	}
	for _, label := range pullRequest.Labels {
		if label == ReleaseLabel {
			return pullRequest, nil
		}
	}
	if err := s.client.AddLabels(ctx, repositoryTarget(), number, []string{ReleaseLabel}); err != nil {
		return PullRequest{}, fmt.Errorf("add %s label to go-infra pull request %d: %w", ReleaseLabel, number, err)
	}
	return pullRequest, nil
}

// DispatchPatchRelease dispatches the fixed workflow and discovers the resulting run.
func (s *Service) DispatchPatchRelease(ctx context.Context, dryRun bool) (WorkflowRun, error) {
	run, err := s.client.DispatchWorkflow(ctx, workflowTarget(), map[string]string{
		"dry-run": strconv.FormatBool(dryRun),
	})
	if err != nil {
		return WorkflowRun{}, fmt.Errorf("dispatch go-infra patch-release workflow: %w", err)
	}
	return run, nil
}

// GetWorkflowRun reads one existing run on the fixed release target.
func (s *Service) GetWorkflowRun(ctx context.Context, id int64) (WorkflowRun, error) {
	return s.client.GetWorkflowRun(ctx, workflowTarget(), id)
}

func validateWorkflowContract(data []byte) error {
	var workflow struct {
		RunName string `yaml:"run-name"`
		On      struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Required *bool  `yaml:"required"`
					Type     string `yaml:"type"`
					Default  any    `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return fmt.Errorf("parse go-infra release workflow: %w", err)
	}
	inputs := workflow.On.WorkflowDispatch.Inputs
	dryRun, dryRunOK := inputs["dry-run"]
	correlation, correlationOK := inputs[CorrelationInput]
	if !correlationOK || workflow.RunName != workflowRunName {
		return fmt.Errorf(
			"manual patch releases are unavailable: %s on %s must include %s and the matching run-name; merge the workflow update first",
			WorkflowFile, DefaultRef, CorrelationInput,
		)
	}
	if len(inputs) != 2 || !dryRunOK ||
		dryRun.Required == nil || !*dryRun.Required || dryRun.Type != "boolean" || dryRun.Default != false ||
		correlation.Required == nil || *correlation.Required || correlation.Type != "string" || correlation.Default != "" {

		return fmt.Errorf("go-infra release workflow_dispatch inputs do not match the allowlist")
	}
	return nil
}
