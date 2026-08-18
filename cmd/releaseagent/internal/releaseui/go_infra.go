// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
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
	goInfraActionTimeout        = 2 * time.Minute
	goInfraWorkflowURL          = "https://github.com/microsoft/go-infra/actions/workflows/create-go-infra-patch-release.yml"
)

// GoInfraPullRequest is the immutable pull request metadata reviewed before applying a release label.
type GoInfraPullRequest struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	BaseRef string   `json:"baseRef"`
	HeadRef string   `json:"headRef"`
	HeadSHA string   `json:"headSHA"`
	Fork    bool     `json:"fork"`
	Labels  []string `json:"labels,omitempty"`
}

// GoInfraGitHubIntegration is the fixed GitHub read and mutation boundary for go-infra releases.
type GoInfraGitHubIntegration struct {
	Preflight              func(context.Context) (string, error)
	GetPullRequest         func(context.Context, int) (GoInfraPullRequest, error)
	AddReleaseOnMergeLabel func(context.Context, int, string) (GoInfraPullRequest, error)
	DispatchPatchRelease   func(context.Context, bool) error
}

type goInfraPlanInput struct {
	Action       string `json:"action"`
	PullRequest  string `json:"pullRequest,omitempty"`
	DispatchMode string `json:"dispatchMode,omitempty"`
}

type goInfraPlan struct {
	Input       goInfraPlanInput    `json:"input"`
	PullRequest *GoInfraPullRequest `json:"pullRequest,omitempty"`
	Digest      string              `json:"digest"`
	Started     bool                `json:"started"`
	Complete    bool                `json:"complete"`
	Result      string              `json:"result,omitempty"`
}

type goInfraPlanResponse struct {
	Input     goInfraPlanInput  `json:"input"`
	Steps     []planStep        `json:"steps"`
	SessionID string            `json:"sessionId"`
	Execution executionResponse `json:"execution"`
	View      ProcessPlanView   `json:"view"`
}

func (s *Server) handleGoInfraPreflight(response http.ResponseWriter, request *http.Request) {
	report := PreflightReport{
		Checks: []PreflightCheck{{
			ID: "loopback-server", Name: "Loopback-only HTTP server", Status: CheckStatusPassed,
			Details: "The release UI accepts requests only through a local loopback address.",
		}},
	}
	if _, err := s.lookPath("gh"); err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: "github-cli", Name: "GitHub CLI (gh)", Status: CheckStatusUnavailable,
			Details: "Executable not found in PATH. No GitHub request can be made.",
		})
		writeJSON(response, http.StatusOK, report)
		return
	}
	s.mu.Lock()
	integration := s.goInfra
	s.mu.Unlock()
	if integration == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: "go-infra-github", Name: "Go-infra GitHub actions", Status: CheckStatusUnavailable,
			Details: "Disabled. Restart with -enable-go-infra-github-execution to allow confirmed GitHub actions.",
		})
		writeJSON(response, http.StatusOK, report)
		return
	}
	details, err := integration.Preflight(request.Context())
	if err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID: "go-infra-github", Name: "Go-infra GitHub actions", Status: CheckStatusWarning,
			Details: err.Error(),
		})
		writeJSON(response, http.StatusOK, report)
		return
	}
	report.PlanningEnabled = true
	report.ExternalExecutionEnabled = true
	report.Checks = append(report.Checks, PreflightCheck{
		ID: "github-cli", Name: "GitHub CLI (gh)", Status: CheckStatusPassed,
		Details: details,
	})
	writeJSON(response, http.StatusOK, report)
}

func (s *Server) handleGetGoInfraPlan(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if s.goInfraPlan == nil {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	result := s.goInfraPlanResponseLocked()
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleGoInfraPlan(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var input goInfraPlanInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	normalized, pullRequestNumber, err := normalizeGoInfraPlanInput(input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if s.goInfra == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-infra GitHub execution is not enabled")
		return
	}
	if s.simulationRunning || s.releaseRunning || s.goInfraRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.goInfraPlan != nil && s.goInfraPlan.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a previous go-infra action has uncertain status; inspect GitHub and remove the action journal before retrying")
		return
	}
	integration := *s.goInfra
	s.mu.Unlock()

	if _, err := integration.Preflight(request.Context()); err != nil {
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("GitHub preflight failed: %v", err))
		return
	}
	var pullRequest *GoInfraPullRequest
	if normalized.Action == goInfraActionReleaseOnMerge {
		resolved, err := integration.GetPullRequest(request.Context(), pullRequestNumber)
		if err != nil {
			writeError(response, http.StatusConflict, fmt.Sprintf("validate go-infra pull request: %v", err))
			return
		}
		if err := validateGoInfraPullRequest(resolved, pullRequestNumber); err != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
		pullRequest = &resolved
	}
	digest, err := goInfraPlanDigest(normalized, pullRequest)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("fingerprint go-infra action: %v", err))
		return
	}
	plan := &goInfraPlan{Input: normalized, PullRequest: pullRequest, Digest: digest}
	step := goInfraStep(plan, func(context.Context) error { return nil })

	s.mu.Lock()
	if s.simulationRunning || s.releaseRunning || s.goInfraRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.goInfraPlan != nil && s.goInfraPlan.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a previous go-infra action has uncertain status; inspect GitHub and remove the action journal before retrying")
		return
	}
	if err := s.goInfraStore.Save(request.Context(), plan); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist reviewed go-infra action: %v", err))
		return
	}
	s.goInfraPlan = plan
	s.steps = []*coordinator.Step{step}
	s.runner = &coordinator.StepRunner{}
	result := s.goInfraPlanResponseLocked()
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleGoInfraStart(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var start releaseStartRequest
	if err := decodeJSON(response, request, &start); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if s.goInfra == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-infra GitHub execution is not enabled")
		return
	}
	if s.goInfraPlan == nil {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "review a go-infra action first")
		return
	}
	if !secureEqual(start.PlanDigest, s.goInfraPlan.Digest) {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "run request does not match the reviewed go-infra action")
		return
	}
	if !start.Confirmed {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirm the GitHub action before starting it")
		return
	}
	if s.goInfraPlan.Started || s.goInfraRunning || s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the reviewed go-infra action has already started")
		return
	}
	integration := *s.goInfra
	plan := *s.goInfraPlan
	if s.goInfraPlan.PullRequest != nil {
		copyPullRequest := *s.goInfraPlan.PullRequest
		copyPullRequest.Labels = append([]string(nil), copyPullRequest.Labels...)
		plan.PullRequest = &copyPullRequest
	}
	step := goInfraStep(&plan, func(ctx context.Context) error {
		if _, err := integration.Preflight(ctx); err != nil {
			return fmt.Errorf("GitHub preflight failed before mutation: %w", err)
		}
		switch plan.Input.Action {
		case goInfraActionReleaseOnMerge:
			_, err := integration.AddReleaseOnMergeLabel(ctx, plan.PullRequest.Number, plan.PullRequest.HeadSHA)
			return err
		case goInfraActionManualDispatch:
			return integration.DispatchPatchRelease(ctx, plan.Input.DispatchMode == goInfraDispatchModeDryRun)
		default:
			return fmt.Errorf("unsupported go-infra action %q", plan.Input.Action)
		}
	})
	s.steps = []*coordinator.Step{step}
	s.runner = &coordinator.StepRunner{}
	runner := s.runner
	s.goInfraPlan.Started = true
	if err := s.goInfraStore.Save(request.Context(), s.goInfraPlan); err != nil {
		s.goInfraPlan.Started = false
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("checkpoint go-infra action before mutation: %v", err))
		return
	}
	s.goInfraRunning = true
	s.mu.Unlock()

	go func() {
		err := runner.Execute(s.ctx, []*coordinator.Step{step})
		s.mu.Lock()
		if s.goInfraPlan != nil && s.goInfraPlan.Digest == plan.Digest {
			s.goInfraPlan.Complete = true
			if err != nil {
				s.goInfraPlan.Result = "failed"
			} else {
				s.goInfraPlan.Result = "succeeded"
			}
			if saveErr := s.goInfraStore.Save(context.Background(), s.goInfraPlan); saveErr != nil {
				s.goInfraPlan.Result = "uncertain"
			}
		}
		s.goInfraRunning = false
		s.mu.Unlock()
	}()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "go-infra GitHub action started"})
}

func (s *Server) restoreGoInfraAction() error {
	if s.goInfraStore == nil {
		return nil
	}
	plan, err := s.goInfraStore.Load(s.ctx)
	if errors.Is(err, ErrGoInfraActionNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load go-infra action journal: %w", err)
	}
	if plan.Started && !plan.Complete {
		plan.Complete = true
		plan.Result = "uncertain"
		if err := s.goInfraStore.Save(s.ctx, plan); err != nil {
			return fmt.Errorf("mark interrupted go-infra action uncertain: %w", err)
		}
	}
	s.goInfraPlan = plan
	if s.document != nil {
		if plan.Result == "uncertain" {
			return errors.New("both a go-images session and an uncertain go-infra action exist; inspect the go-infra action journal before restarting")
		}
		return nil
	}
	s.steps = []*coordinator.Step{goInfraStep(plan, func(context.Context) error { return nil })}
	s.runner = &coordinator.StepRunner{}
	s.activeProcessID = goInfraProcessID
	return nil
}

func (s *Server) goInfraPlanResponseLocked() goInfraPlanResponse {
	plan := s.goInfraPlan
	steps := describeSteps(s.steps)
	if plan.Complete {
		status := "succeeded"
		if plan.Result != "succeeded" {
			status = "failed"
		}
		for i := range steps {
			steps[i].Status = status
		}
	}
	execution := executionResponse{
		Enabled: s.goInfra != nil, Eligible: plan.Digest != "", PlanDigest: plan.Digest,
	}
	if plan.Started {
		execution.Run = pipelineRun{
			BuildID: goInfraRunID(plan), URL: goInfraRunURL(plan), LinkLabel: goInfraRunLinkLabel(plan),
			Complete: plan.Complete, Result: plan.Result,
		}
	}
	return goInfraPlanResponse{
		Input: plan.Input, Steps: steps, SessionID: plan.Digest, Execution: execution,
		View: goInfraPlanView(plan),
	}
}

func goInfraPlanView(plan *goInfraPlan) ProcessPlanView {
	view := ProcessPlanView{
		Subtitle: "GitHub action · 1 step",
		Request: &ProcessRequestPreview{
			Eyebrow: "GitHub request preview · not sent",
			Target:  goInfraRepository,
		},
	}
	switch plan.Input.Action {
	case goInfraActionReleaseOnMerge:
		pullRequest := plan.PullRequest
		view.IntentTitle = fmt.Sprintf("Apply %s to PR #%d", goInfraReleaseLabel, pullRequest.Number)
		view.IntentBadge = goInfraReleaseLabel
		view.Facts = []ProcessPlanFact{
			{Label: "Pull request", Value: fmt.Sprintf("#%d · %s", pullRequest.Number, pullRequest.Title), Href: pullRequest.URL},
			{Label: "Target", Value: pullRequest.BaseRef, Detail: pullRequest.HeadRef + " @ " + pullRequest.HeadSHA},
		}
		view.Request.Title = "Add pull request label"
		view.Request.Fields = []ProcessRequestField{
			{Name: "pullRequest", Value: strconv.Itoa(pullRequest.Number)},
			{Name: "expectedHeadSHA", Value: pullRequest.HeadSHA},
			{Name: "label", Value: goInfraReleaseLabel},
		}
		view.ExecutionTitle = "Apply release-on-merge label"
		view.ExecutionWarning = "This adds release-on-merge to the reviewed non-fork PR. It does not merge the PR. Merging it later causes the existing GitHub workflow to create the patch release."
		view.ExecutionConfirmation = fmt.Sprintf("Confirm adding %s to go-infra PR #%d at %s.", goInfraReleaseLabel, pullRequest.Number, pullRequest.HeadSHA)
		view.ExecutionButtonLabel = "Apply release-on-merge label"
	case goInfraActionManualDispatch:
		dryRun := plan.Input.DispatchMode == goInfraDispatchModeDryRun
		mode := "Publish"
		if dryRun {
			mode = "Dry run"
		}
		view.IntentTitle = mode + " the next go-infra patch release"
		view.IntentBadge = plan.Input.DispatchMode
		view.Facts = []ProcessPlanFact{{Label: "Workflow", Value: goInfraWorkflowFile, Href: goInfraWorkflowURL}, {Label: "Ref", Value: goInfraDefaultRef}}
		view.Request.Title = "Dispatch GitHub Actions workflow"
		view.Request.Fields = []ProcessRequestField{{Name: "workflow", Value: goInfraWorkflowFile}, {Name: "ref", Value: goInfraDefaultRef}, {Name: "dry-run", Value: strconv.FormatBool(dryRun)}}
		if dryRun {
			view.ExecutionTitle = "Run release dry run"
			view.ExecutionWarning = "This dispatches the existing workflow with dry-run=true. It calculates the next version but does not create a release."
			view.ExecutionConfirmation = "Confirm dispatching the go-infra patch-release workflow on main in dry-run mode."
			view.ExecutionButtonLabel = "Run dry run"
		} else {
			view.ExecutionTitle = "Publish patch release"
			view.ExecutionWarning = "This immediately dispatches the existing workflow on main with dry-run=false. The workflow can create the next go-infra patch release."
			view.ExecutionConfirmation = "Confirm dispatching the go-infra patch-release workflow on main to publish a release."
			view.ExecutionButtonLabel = "Publish patch release"
		}
	}
	return view
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
	if pullRequest.URL != fmt.Sprintf("https://github.com/microsoft/go-infra/pull/%d", expectedNumber) {
		return fmt.Errorf("go-infra pull request has unexpected URL %q", pullRequest.URL)
	}
	return nil
}

func goInfraPlanDigest(input goInfraPlanInput, pullRequest *GoInfraPullRequest) (string, error) {
	payload := struct {
		Repository   string
		Ref          string
		Workflow     string
		ReleaseLabel string
		Input        goInfraPlanInput
		PullRequest  *GoInfraPullRequest
	}{
		Repository: goInfraRepository, Ref: goInfraDefaultRef, Workflow: goInfraWorkflowFile,
		ReleaseLabel: goInfraReleaseLabel, Input: input, PullRequest: pullRequest,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), nil
}

func goInfraStep(plan *goInfraPlan, action coordinator.StepFunc) *coordinator.Step {
	name := "Apply release-on-merge label"
	if plan.Input.Action == goInfraActionManualDispatch {
		if plan.Input.DispatchMode == goInfraDispatchModeDryRun {
			name = "Dispatch patch-release dry run"
		} else {
			name = "Dispatch patch release"
		}
	}
	return coordinator.NewRootStep(name, goInfraActionTimeout, action)
}

func goInfraRunID(plan *goInfraPlan) string {
	if plan.Input.Action == goInfraActionReleaseOnMerge {
		return "pr-" + plan.Input.PullRequest
	}
	return plan.Input.DispatchMode
}

func goInfraRunURL(plan *goInfraPlan) string {
	if plan.Input.Action == goInfraActionReleaseOnMerge {
		return plan.PullRequest.URL
	}
	return goInfraWorkflowURL
}

func goInfraRunLinkLabel(plan *goInfraPlan) string {
	if plan.Input.Action == goInfraActionReleaseOnMerge {
		return "Open go-infra PR #" + plan.Input.PullRequest
	}
	return "Open go-infra workflow runs"
}
