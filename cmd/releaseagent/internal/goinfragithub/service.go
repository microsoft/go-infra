// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goinfragithub provides the fixed GitHub mutation boundary for go-infra releases.
package goinfragithub

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/githubclient"
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

// CommandRunner executes gh without exposing its authentication token to callers.
type CommandRunner = githubclient.CommandRunner

// ExecCommandRunner runs the locally authenticated gh executable.
type ExecCommandRunner = githubclient.ExecCommandRunner

// Service performs allowlisted GitHub reads and mutations for go-infra.
type Service struct {
	client *githubclient.Client
}

// New creates a go-infra GitHub service.
func New(runner CommandRunner) (*Service, error) {
	client, err := githubclient.New("github.com", runner)
	if err != nil {
		return nil, err
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

// Preflight verifies local authentication and the fixed repository and workflow targets.
func (s *Service) Preflight(ctx context.Context) (string, error) {
	if err := s.client.AuthStatus(ctx); err != nil {
		return "", err
	}
	repository, err := s.client.GetRepository(ctx, repositoryTarget())
	if err != nil {
		return "", fmt.Errorf("verify go-infra repository: %w", err)
	}
	if repository.FullName != Owner+"/"+Repository || repository.DefaultBranch != DefaultRef {
		return "", fmt.Errorf("go-infra repository does not match the release allowlist: %#v", repository)
	}
	workflow, err := s.client.GetWorkflow(ctx, workflowTarget())
	if err != nil {
		return "", fmt.Errorf("verify go-infra release workflow: %w", err)
	}
	if workflow.Path != WorkflowPath || workflow.State != "active" {
		return "", fmt.Errorf("go-infra release workflow does not match the allowlist: %#v", workflow)
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

// PollWorkflowRun waits until the allowlisted run completes and reports each observed state.
func (s *Service) PollWorkflowRun(ctx context.Context, id int64, report func(WorkflowRun) error) (WorkflowRun, error) {
	run, err := s.client.PollWorkflowRun(ctx, workflowTarget(), id, report)
	if err != nil {
		return run, fmt.Errorf("monitor go-infra patch-release workflow: %w", err)
	}
	return run, nil
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
	if workflow.RunName != workflowRunName || len(inputs) != 2 || !dryRunOK || !correlationOK ||
		dryRun.Required == nil || !*dryRun.Required || dryRun.Type != "boolean" || dryRun.Default != false ||
		correlation.Required == nil || *correlation.Required || correlation.Type != "string" || correlation.Default != "" {

		return fmt.Errorf("go-infra release workflow_dispatch inputs do not match the allowlist")
	}
	return nil
}
