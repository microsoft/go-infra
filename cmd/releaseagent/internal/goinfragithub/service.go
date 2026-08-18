// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package goinfragithub provides the fixed GitHub mutation boundary for go-infra releases.
package goinfragithub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

const (
	Owner        = "microsoft"
	Repository   = "go-infra"
	DefaultRef   = "main"
	ReleaseLabel = "release-on-merge"
	WorkflowFile = "create-go-infra-patch-release.yml"
	WorkflowPath = ".github/workflows/" + WorkflowFile
)

// CommandRunner executes gh without exposing its authentication token to callers.
type CommandRunner interface {
	Run(context.Context, []byte, ...string) ([]byte, error)
}

// ExecCommandRunner runs the locally authenticated gh executable.
type ExecCommandRunner struct{}

// Run executes gh with optional JSON stdin.
func (ExecCommandRunner) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		return nil, fmt.Errorf("gh %s: %w", firstArg(args), err)
	}
	return nil, fmt.Errorf("gh %s: %w: %s", firstArg(args), err, message)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return "command"
	}
	return args[0]
}

// Service performs allowlisted GitHub reads and mutations for go-infra.
type Service struct {
	runner CommandRunner
}

// New creates a go-infra GitHub service.
func New(runner CommandRunner) (*Service, error) {
	if runner == nil {
		return nil, errors.New("GitHub command runner is nil")
	}
	return &Service{runner: runner}, nil
}

// PullRequest contains the immutable metadata used to confirm a release-on-merge plan.
type PullRequest struct {
	Number  int
	Title   string
	URL     string
	BaseRef string
	HeadRef string
	HeadSHA string
	Fork    bool
	Labels  []string
}

// Preflight verifies local authentication and the fixed repository and workflow targets.
func (s *Service) Preflight(ctx context.Context) (string, error) {
	if _, err := s.runner.Run(ctx, nil, "auth", "status", "--hostname", "github.com"); err != nil {
		return "", fmt.Errorf("verify GitHub CLI authentication: %w", err)
	}
	var repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.getJSON(ctx, "repos/"+Owner+"/"+Repository, &repository); err != nil {
		return "", fmt.Errorf("verify go-infra repository: %w", err)
	}
	if repository.FullName != Owner+"/"+Repository || repository.DefaultBranch != DefaultRef {
		return "", fmt.Errorf("go-infra repository does not match the release allowlist: %#v", repository)
	}
	var workflow struct {
		Path  string `json:"path"`
		State string `json:"state"`
	}
	if err := s.getJSON(ctx, workflowEndpoint(), &workflow); err != nil {
		return "", fmt.Errorf("verify go-infra release workflow: %w", err)
	}
	if workflow.Path != WorkflowPath || workflow.State != "active" {
		return "", fmt.Errorf("go-infra release workflow does not match the allowlist: %#v", workflow)
	}
	var content struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := s.getJSON(ctx, "repos/"+Owner+"/"+Repository+"/contents/"+WorkflowPath+"?ref="+DefaultRef, &content); err != nil {
		return "", fmt.Errorf("read go-infra release workflow contract: %w", err)
	}
	if content.Encoding != "base64" {
		return "", fmt.Errorf("go-infra release workflow has unsupported content encoding %q", content.Encoding)
	}
	workflowYAML, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode go-infra release workflow: %w", err)
	}
	if err := validateWorkflowContract(workflowYAML); err != nil {
		return "", err
	}
	return "Authenticated to GitHub and verified microsoft/go-infra main and its active patch-release workflow.", nil
}

// GetPullRequest validates an open, non-fork pull request targeting main.
func (s *Service) GetPullRequest(ctx context.Context, number int) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, errors.New("pull request number must be positive")
	}
	var response struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo *struct {
				Fork bool `json:"fork"`
			} `json:"repo"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("repos/%s/%s/pulls/%d", Owner, Repository, number), &response); err != nil {
		return PullRequest{}, fmt.Errorf("read go-infra pull request %d: %w", number, err)
	}
	if response.Number != number || response.State != "open" || response.Merged {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d is not open", number)
	}
	if response.Base.Ref != DefaultRef {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d targets %q, expected %q", number, response.Base.Ref, DefaultRef)
	}
	if response.Head.Repo == nil {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d has no head repository", number)
	}
	if response.Head.Repo.Fork {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d comes from a fork; release-on-merge cannot publish from fork-triggered runs", number)
	}
	if len(response.Head.SHA) != 40 {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d has invalid head SHA %q", number, response.Head.SHA)
	}
	if _, err := hex.DecodeString(response.Head.SHA); err != nil {
		return PullRequest{}, fmt.Errorf("go-infra pull request %d has invalid head SHA %q", number, response.Head.SHA)
	}
	result := PullRequest{
		Number: number, Title: response.Title,
		URL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", Owner, Repository, number), BaseRef: response.Base.Ref,
		HeadRef: response.Head.Ref, HeadSHA: response.Head.SHA, Fork: response.Head.Repo.Fork,
	}
	for _, label := range response.Labels {
		result.Labels = append(result.Labels, label.Name)
	}
	return result, nil
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
	body, err := json.Marshal(struct {
		Labels []string `json:"labels"`
	}{Labels: []string{ReleaseLabel}})
	if err != nil {
		return PullRequest{}, fmt.Errorf("encode release label request: %w", err)
	}
	if _, err := s.runner.Run(
		ctx, body, "api", "--hostname", "github.com", "--method", "POST",
		fmt.Sprintf("repos/%s/%s/issues/%d/labels", Owner, Repository, number), "--input", "-",
	); err != nil {
		return PullRequest{}, fmt.Errorf("add %s label to go-infra pull request %d: %w", ReleaseLabel, number, err)
	}
	return pullRequest, nil
}

// DispatchPatchRelease dispatches the fixed workflow on main with only its dry-run input.
func (s *Service) DispatchPatchRelease(ctx context.Context, dryRun bool) error {
	body, err := json.Marshal(struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}{
		Ref: DefaultRef,
		Inputs: map[string]string{
			"dry-run": strconv.FormatBool(dryRun),
		},
	})
	if err != nil {
		return fmt.Errorf("encode workflow dispatch request: %w", err)
	}
	if _, err := s.runner.Run(ctx, body, "api", "--hostname", "github.com", "--method", "POST", workflowEndpoint()+"/dispatches", "--input", "-"); err != nil {
		return fmt.Errorf("dispatch go-infra patch-release workflow: %w", err)
	}
	return nil
}

func (s *Service) getJSON(ctx context.Context, endpoint string, target any) error {
	output, err := s.runner.Run(ctx, nil, "api", "--hostname", "github.com", endpoint)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(output, target); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func workflowEndpoint() string {
	return "repos/" + Owner + "/" + Repository + "/actions/workflows/" + WorkflowFile
}

func validateWorkflowContract(data []byte) error {
	var workflow struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Required *bool  `yaml:"required"`
					Type     string `yaml:"type"`
					Default  *bool  `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return fmt.Errorf("parse go-infra release workflow: %w", err)
	}
	inputs := workflow.On.WorkflowDispatch.Inputs
	input, ok := inputs["dry-run"]
	if len(inputs) != 1 || !ok || input.Required == nil || !*input.Required || input.Type != "boolean" ||
		input.Default == nil || *input.Default {

		return fmt.Errorf("go-infra release workflow_dispatch inputs do not match the allowlist")
	}
	return nil
}
