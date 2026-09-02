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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

func (s *Server) goImagesExecutionResponseLocked() executionResponse {
	result := executionResponse{Enabled: s.execution != nil}
	if s.goImages.document == nil || s.goImages.workflowInput == nil || len(s.steps) == 0 {
		return result
	}
	state := s.goImages.document.State
	result.Run = pipelineRun{
		BuildID: state.BuildID, Complete: state.Complete,
	}
	if result.Run.BuildID != "" {
		result.Run.URL = "https://dev.azure.com/dnceng/internal/_build/results?buildId=" + result.Run.BuildID
		result.Run.LinkLabel = "Open Azure DevOps run " + result.Run.BuildID
	}
	if !result.Enabled {
		result.UnavailableReason = "Real pipeline execution is disabled. The workflow can still be simulated."
		return result
	}
	plan, err := goimagessession.NewPlan(s.steps)
	if err != nil {
		result.UnavailableReason = "The release graph is invalid."
		return result
	}
	parameters, err := goimagesworkflow.PipelineParameters(
		s.goImages.workflowInput.Mode,
		s.goImages.workflowInput.SourceBuildID,
	)
	if err != nil {
		result.UnavailableReason = err.Error()
		return result
	}
	payload := struct {
		SessionID        string
		ExecutionDigest  string
		Mode             goimagesworkflow.Mode
		Versions         []string
		SourceVersion    string
		SourceBuildID    string
		Parameters       map[string]string
		WorkflowRevision int
		WorkflowDigest   string
	}{
		SessionID: s.goImages.document.ID, ExecutionDigest: s.goImages.document.ExecutionDigest,
		Mode: s.goImages.workflowInput.Mode, Versions: append([]string(nil), s.goImages.workflowInput.Versions...),
		SourceVersion: s.goImages.workflowInput.SourceVersion,
		SourceBuildID: s.goImages.workflowInput.SourceBuildID,
		Parameters:    parameters, WorkflowRevision: plan.WorkflowRevision, WorkflowDigest: plan.Digest,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		result.UnavailableReason = "Unable to fingerprint the release intent."
		return result
	}
	digest := sha256.Sum256(data)
	result.PlanDigest = fmt.Sprintf("%x", digest)
	result.Eligible = result.PlanDigest != ""
	return result
}

func (s *Server) restoreSession() error {
	if s.sessionStore == nil {
		return nil
	}
	document, err := s.sessionStore.Load(s.ctx)
	if errors.Is(err, goimagessession.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load release session: %w", err)
	}
	input := document.Input
	state := document.State
	steps, restoredState, err := goimagesworkflow.NewGraphWithCheckpoint(
		&input, &state, disabledGoImagesService{}, s.checkpointReleaseState,
	)
	if err != nil {
		return fmt.Errorf("reconstruct release session: %w", err)
	}
	plan, err := goimagessession.NewPlan(steps)
	if err != nil {
		return fmt.Errorf("fingerprint reconstructed release session: %w", err)
	}
	if err := document.MatchesPlan(plan); err != nil {
		return fmt.Errorf("restore release session: %w", err)
	}
	s.steps = steps
	s.goImages.planInput = PlanInput{Mode: input.Mode, SourceBuildID: input.SourceBuildID}
	s.goImages.source = GoImagesSource{
		Branch: goimagesworkflow.SourceBranch, Commit: input.SourceVersion,
		Versions: append([]string(nil), input.Versions...),
	}
	if input.Mode == goimagesworkflow.ModeRollback {
		buildID, _ := strconv.Atoi(input.SourceBuildID)
		s.goImages.rollbackSource = &GoImagesRollbackSource{BuildID: buildID, Versions: append([]string(nil), input.Versions...)}
	}
	s.goImages.workflowInput = &input
	s.goImages.workflowState = restoredState
	s.goImages.document = document
	s.runner = &coordinator.StepRunner{}
	s.goImages.restored = true
	s.activeProcessID = goImagesProcessID
	return nil
}

func (s *Server) resumeRestoredMonitoring() error {
	if s.execution == nil || s.goImages.document == nil || s.goImages.workflowInput == nil || s.goImages.workflowState == nil {
		return nil
	}
	buildIDText := s.goImages.workflowState.BuildID
	if buildIDText == "" || s.goImages.workflowState.Complete {
		return nil
	}
	buildID, err := strconv.Atoi(buildIDText)
	if err != nil || buildID <= 0 {
		return fmt.Errorf("restore go-images monitoring with invalid build ID %q", buildIDText)
	}
	intent := s.goImagesExecutionResponseLocked()
	if !intent.Eligible || intent.PlanDigest == "" {
		return errors.New("restore go-images monitoring without an eligible execution plan")
	}
	service, err := s.execution.NewService(GoImagesExecutionRequest{
		Mode: s.goImages.workflowInput.Mode, SessionID: s.goImages.document.ID, ExecutionDigest: intent.PlanDigest,
		Versions: append([]string(nil), s.goImages.workflowInput.Versions...), SourceBuildID: s.goImages.workflowInput.SourceBuildID,
		SourceVersion:        s.goImages.workflowInput.SourceVersion,
		PreviousQueueAttempt: s.goImages.workflowState.QueueAttempted,
	})
	if err != nil {
		return fmt.Errorf("restore go-images monitoring service: %w", err)
	}
	monitor := restoredRunMonitor{
		buildID: buildID,
		monitor: func(ctx context.Context, id int) error {
			return service.PollPipeline(ctx, strconv.Itoa(id))
		},
	}
	input := *s.goImages.workflowInput
	steps, state, err := goimagesworkflow.NewGraphWithCheckpoint(
		&input, s.goImages.workflowState, monitor, s.checkpointReleaseState,
	)
	if err != nil {
		return fmt.Errorf("restore go-images monitoring graph: %w", err)
	}
	actualPlan, err := goimagessession.NewPlan(steps)
	if err != nil || actualPlan.Digest != s.goImages.document.Plan.Digest ||
		actualPlan.WorkflowRevision != s.goImages.document.Plan.WorkflowRevision {

		return errors.New("restored go-images monitoring graph no longer matches the persisted plan")
	}
	s.steps = steps
	s.goImages.workflowState = state
	s.runner = &coordinator.StepRunner{}
	s.releaseRunning = true
	runner := s.runner
	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.finishRelease()
	}()
	return nil
}

func (s *Server) checkpointReleaseState(ctx context.Context, state *goimagesworkflow.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionStore == nil {
		return nil
	}
	if s.goImages.document == nil {
		return errors.New("cannot checkpoint release state before creating a session document")
	}
	document, err := s.goImages.document.WithState(state, time.Now())
	if err != nil {
		return fmt.Errorf("update release session document: %w", err)
	}
	if err := s.sessionStore.Save(ctx, document); err != nil {
		return fmt.Errorf("persist release state checkpoint: %w", err)
	}
	s.goImages.document = document
	return nil
}

func (s *Server) handlePreflight(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusOK, s.preflightReport(request.Context()))
}

type releaseStartRequest struct {
	PlanDigest string `json:"planDigest"`
	Confirmed  bool   `json:"confirmed"`
}

func (s *Server) handleReleaseStart(response http.ResponseWriter, request *http.Request) {
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
	if s.execution == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "real go-images execution is not enabled")
		return
	}
	if s.goImages.document == nil || s.goImages.workflowInput == nil || s.goImages.workflowState == nil {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "prepare a go-images release first")
		return
	}
	intent := s.goImagesExecutionResponseLocked()
	if !intent.Eligible || intent.PlanDigest == "" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the current plan is not eligible for execution")
		return
	}
	if !secureEqual(start.PlanDigest, intent.PlanDigest) {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "run request does not match the current release plan")
		return
	}
	if !start.Confirmed {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirm the release before starting it")
		return
	}
	if s.goImages.workflowState.Complete {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the go-images release is already complete")
		return
	}
	if s.simulationRunning || s.releaseRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a workflow is already running")
		return
	}
	preflight := s.readOnly.Preflight
	resolveSource := s.readOnly.ResolveCurrentSource
	validateRollback := s.readOnly.ValidateRollback
	newService := s.execution.NewService
	input := *s.goImages.workflowInput
	state := s.goImages.workflowState
	previousQueueAttempt := state.QueueAttempted
	sessionID := s.goImages.document.ID
	expectedPlan := s.goImages.document.Plan
	alreadyQueued := state.BuildID != ""
	s.releaseRunning = true
	s.mu.Unlock()

	if !alreadyQueued {
		current, err := resolveSource(request.Context())
		if err != nil {
			s.finishRelease()
			writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("re-resolve current microsoft/main: %v", err))
			return
		}
		if current.Branch != goimagesworkflow.SourceBranch || current.Commit != input.SourceVersion {
			s.finishRelease()
			writeError(response, http.StatusConflict, "microsoft/main changed after this plan was prepared; refresh the plan before queueing")
			return
		}
		if input.Mode == goimagesworkflow.ModeRollback {
			buildID, _ := strconv.Atoi(input.SourceBuildID)
			if _, err := validateRollback(request.Context(), buildID); err != nil {
				s.finishRelease()
				writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("revalidate rollback source: %v", err))
				return
			}
		}
	}
	if _, err := preflight(request.Context()); err != nil {
		s.finishRelease()
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("go-images execution preflight failed: %v", err))
		return
	}
	service, err := newService(GoImagesExecutionRequest{
		Mode: input.Mode, SessionID: sessionID, ExecutionDigest: intent.PlanDigest,
		Versions: append([]string(nil), input.Versions...), SourceBuildID: input.SourceBuildID,
		SourceVersion: input.SourceVersion, PreviousQueueAttempt: previousQueueAttempt,
	})
	if err != nil {
		s.finishRelease()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images execution service: %v", err))
		return
	}
	steps, state, err := goimagesworkflow.NewGraphWithCheckpoint(
		&input, state, service, s.checkpointReleaseState,
	)
	if err != nil {
		s.finishRelease()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images execution graph: %v", err))
		return
	}
	actualPlan, err := goimagessession.NewPlan(steps)
	if err != nil || actualPlan.Digest != expectedPlan.Digest || actualPlan.WorkflowRevision != expectedPlan.WorkflowRevision {
		s.finishRelease()
		writeError(response, http.StatusConflict, "go-images execution graph no longer matches the confirmed plan")
		return
	}

	s.mu.Lock()
	s.steps = steps
	s.goImages.workflowState = state
	s.runner = &coordinator.StepRunner{}
	runner := s.runner
	s.mu.Unlock()
	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.finishRelease()
	}()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "go-images release started"})
}

func (s *Server) finishRelease() {
	s.mu.Lock()
	s.releaseRunning = false
	s.mu.Unlock()
}

func normalizePlanInput(input PlanInput) (PlanInput, error) {
	input.SourceBuildID = strings.TrimSpace(input.SourceBuildID)
	switch input.Mode {
	case goimagesworkflow.ModeNormal, goimagesworkflow.ModeTest:
		if input.SourceBuildID != "" {
			return PlanInput{}, fmt.Errorf("%s release does not accept a source build ID", input.Mode)
		}
	case goimagesworkflow.ModeRollback:
		buildID, err := strconv.Atoi(input.SourceBuildID)
		if err != nil || buildID <= 0 {
			return PlanInput{}, errors.New("rollback source build ID must be a positive integer")
		}
		input.SourceBuildID = strconv.Itoa(buildID)
	default:
		return PlanInput{}, fmt.Errorf("unsupported go-images release mode %q", input.Mode)
	}
	return input, nil
}

func validateCurrentSource(source GoImagesSource) error {
	if source.Branch != goimagesworkflow.SourceBranch {
		return fmt.Errorf("resolved go-images branch %q is not allowlisted", source.Branch)
	}
	if len(source.Commit) != 40 {
		return fmt.Errorf("resolved go-images commit %q is invalid", source.Commit)
	}
	for _, character := range source.Commit {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return fmt.Errorf("resolved go-images commit %q is invalid", source.Commit)
		}
	}
	return nil
}

func normalizeResolvedVersions(versions []string) ([]string, error) {
	if len(versions) == 0 {
		return nil, errors.New("resolved go-images source contains no Microsoft Build of Go versions")
	}
	seen := make(map[string]struct{}, len(versions))
	normalized := make([]string, 0, len(versions))
	for _, version := range versions {
		version = strings.TrimSpace(version)
		if version == "" {
			return nil, errors.New("resolved go-images source contains an empty version")
		}
		if _, exists := seen[version]; exists {
			continue
		}
		seen[version] = struct{}{}
		normalized = append(normalized, version)
	}
	sort.Strings(normalized)
	return normalized, nil
}
