// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/githubclient"
)

const (
	goInfraProcessID            = "go-infra"
	goInfraActionReleaseOnMerge = "release-on-merge"
	goInfraActionManualDispatch = "manual-dispatch"
	goInfraDispatchModeDryRun   = "dry-run"
	goInfraDispatchModePublish  = "publish"
	goInfraReleaseLabel         = "release-on-merge"
	goInfraWorkflowFile         = "create-go-infra-patch-release.yml"
	goInfraRepository           = "microsoft/go-infra"
	goInfraDefaultRef           = "main"
	goInfraLabelTimeout         = 2 * time.Minute
	goInfraWorkflowTimeout      = 30 * time.Minute
	goInfraWorkflowURL          = "https://github.com/microsoft/go-infra/actions/workflows/create-go-infra-patch-release.yml"
)

// GoInfraPullRequest is the immutable pull request metadata reviewed before applying a release label.
type GoInfraPullRequest = githubclient.PullRequest

// GoInfraWorkflowRun is the validated GitHub Actions run created by manual dispatch.
type GoInfraWorkflowRun = githubclient.WorkflowRun

// GoInfraGitHubIntegration is the fixed GitHub read and mutation boundary for go-infra releases.
type GoInfraGitHubIntegration struct {
	Preflight              func(context.Context) (string, error)
	GetPullRequest         func(context.Context, int) (GoInfraPullRequest, error)
	AddReleaseOnMergeLabel func(context.Context, int, string) (GoInfraPullRequest, error)
	DispatchPatchRelease   func(context.Context, bool) (GoInfraWorkflowRun, error)
	PollWorkflowRun        func(context.Context, int64, func(GoInfraWorkflowRun) error) (GoInfraWorkflowRun, error)
}

type goInfraPlanInput struct {
	Action       string `json:"action"`
	PullRequest  string `json:"pullRequest,omitempty"`
	DispatchMode string `json:"dispatchMode,omitempty"`
}

// WithGoInfraGitHubIntegration enables the fixed go-infra policy through the shared process executor.
func WithGoInfraGitHubIntegration(integration GoInfraGitHubIntegration) Option {
	return WithProcessExecutor(goInfraProcessID, newGoInfraProcessExecutor(integration))
}

func normalizeGoInfraPlanInput(input goInfraPlanInput) (goInfraPlanInput, int, error) {
	input.Action = strings.TrimSpace(input.Action)
	input.PullRequest = strings.TrimSpace(input.PullRequest)
	input.DispatchMode = strings.TrimSpace(input.DispatchMode)
	switch input.Action {
	case goInfraActionReleaseOnMerge:
		if input.DispatchMode != "" {
			return goInfraPlanInput{}, 0, errors.New("release-on-merge does not accept a dispatch mode")
		}
		number, err := strconv.Atoi(input.PullRequest)
		if err != nil || number <= 0 {
			return goInfraPlanInput{}, 0, errors.New("pull request number must be a positive integer")
		}
		input.PullRequest = strconv.Itoa(number)
		return input, number, nil
	case goInfraActionManualDispatch:
		if input.PullRequest != "" {
			return goInfraPlanInput{}, 0, errors.New("manual dispatch does not accept a pull request number")
		}
		if input.DispatchMode != goInfraDispatchModeDryRun && input.DispatchMode != goInfraDispatchModePublish {
			return goInfraPlanInput{}, 0, fmt.Errorf("unsupported dispatch mode %q", input.DispatchMode)
		}
		return input, 0, nil
	default:
		return goInfraPlanInput{}, 0, fmt.Errorf("unsupported go-infra action %q", input.Action)
	}
}

func validateGoInfraPullRequest(pullRequest GoInfraPullRequest, expectedNumber int) error {
	if pullRequest.Number != expectedNumber || pullRequest.Number <= 0 {
		return errors.New("GitHub returned a different pull request")
	}
	if pullRequest.BaseRef != goInfraDefaultRef {
		return fmt.Errorf("go-infra pull request targets %q, expected %q", pullRequest.BaseRef, goInfraDefaultRef)
	}
	if pullRequest.Fork {
		return errors.New("fork pull requests cannot use release-on-merge")
	}
	if len(pullRequest.HeadSHA) != 40 {
		return fmt.Errorf("go-infra pull request has invalid head SHA %q", pullRequest.HeadSHA)
	}
	if _, err := hex.DecodeString(pullRequest.HeadSHA); err != nil {
		return fmt.Errorf("go-infra pull request has invalid head SHA %q", pullRequest.HeadSHA)
	}
	if pullRequest.URL != fmt.Sprintf("https://github.com/microsoft/go-infra/pull/%d", expectedNumber) {
		return fmt.Errorf("go-infra pull request has unexpected URL %q", pullRequest.URL)
	}
	return nil
}

func validateGoInfraWorkflowRun(run *GoInfraWorkflowRun) error {
	if run == nil || run.ID <= 0 {
		return errors.New("go-infra workflow run is invalid")
	}
	wantURL := fmt.Sprintf("https://github.com/microsoft/go-infra/actions/runs/%d", run.ID)
	if run.URL != wantURL || len(run.HeadSHA) != 40 || run.CreatedAt.IsZero() {
		return fmt.Errorf("go-infra workflow run does not match the allowlist: %#v", run)
	}
	if _, err := hex.DecodeString(run.HeadSHA); err != nil {
		return fmt.Errorf("go-infra workflow run has invalid head SHA %q", run.HeadSHA)
	}
	switch run.Status {
	case "queued", "in_progress", "waiting", "pending", "requested":
		if run.Conclusion != "" {
			return fmt.Errorf("incomplete go-infra workflow run has conclusion %q", run.Conclusion)
		}
	case "completed":
		if run.Conclusion == "" {
			return errors.New("completed go-infra workflow run has no conclusion")
		}
	default:
		return fmt.Errorf("go-infra workflow run has unsupported status %q", run.Status)
	}
	return nil
}
