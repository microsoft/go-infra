// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
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

func (s *Server) handleGetPlan(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	result := s.goImagesPlanResponseLocked(s.goImages.restored)
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handlePlan(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var input PlanInput
	if err := decodeJSON(response, request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	normalized, err := normalizePlanInput(input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if s.readOnly == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-images source resolution is not enabled")
		return
	}
	if s.simulationRunning || s.releaseRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.processRun != nil && s.processRun.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "an external action has uncertain status; inspect the target service before preparing another process")
		return
	}
	preflight := s.readOnly.Preflight
	resolveSource := s.readOnly.ResolveCurrentSource
	validateRollback := s.readOnly.ValidateRollback
	s.mu.Unlock()

	if _, err := preflight(request.Context()); err != nil {
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("Azure preflight failed: %v", err))
		return
	}
	source, err := resolveSource(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("resolve current microsoft/main: %v", err))
		return
	}
	if err := validateCurrentSource(source); err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}

	versions := append([]string(nil), source.Versions...)
	var rollbackSource *GoImagesRollbackSource
	if normalized.Mode == goimagesworkflow.ModeRollback {
		buildID, _ := strconv.Atoi(normalized.SourceBuildID)
		validated, err := validateRollback(request.Context(), buildID)
		if err != nil {
			writeError(response, http.StatusConflict, fmt.Sprintf("validate rollback source: %v", err))
			return
		}
		if validated.BuildID != buildID {
			writeError(response, http.StatusConflict, "rollback validation returned a different build")
			return
		}
		rollbackSource = &validated
		versions = append([]string(nil), validated.Versions...)
	}
	versions, err = normalizeResolvedVersions(versions)
	if err != nil {
		writeError(response, http.StatusConflict, err.Error())
		return
	}

	releaseInput := &goimagesworkflow.Input{
		Versions:      versions,
		Mode:          normalized.Mode,
		SourceVersion: source.Commit,
		SourceBuildID: normalized.SourceBuildID,
	}
	steps, releaseState, err := goimagesworkflow.NewGraphWithCheckpoint(
		releaseInput, nil, disabledGoImagesService{}, s.checkpointReleaseState,
	)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images plan: %v", err))
		return
	}
	document, err := goimagessession.NewDocument(releaseInput, releaseState, steps, time.Now())
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create durable release session: %v", err))
		return
	}

	s.mu.Lock()
	if s.simulationRunning || s.releaseRunning || s.processRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.processRun != nil && s.processRun.Result == "uncertain" {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "an external action has uncertain status; inspect the target service before preparing another process")
		return
	}
	if err := s.sessionStore.Save(request.Context(), document); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist release session: %v", err))
		return
	}
	s.steps = steps
	s.goImages.planInput = normalized
	s.goImages.source = source
	s.goImages.rollbackSource = rollbackSource
	s.goImages.workflowInput = releaseInput
	s.goImages.workflowState = releaseState
	s.goImages.document = document
	s.runner = &coordinator.StepRunner{}
	s.goImages.restored = false
	result := s.goImagesPlanResponseLocked(false)
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) goImagesPlanResponseLocked(restored bool) planResponse {
	parameters := map[string]string{}
	if s.goImages.workflowInput != nil {
		if derived, err := goimagesworkflow.PipelineParameters(
			s.goImages.workflowInput.Mode,
			s.goImages.workflowInput.SourceBuildID,
		); err == nil {
			parameters = derived
		}
	}
	steps := describeSteps(s.steps)
	if s.goImages.document != nil {
		state := s.goImages.document.State
		if state.Complete {
			for i := range steps {
				steps[i].Status = "succeeded"
			}
		} else if state.BuildID != "" {
			setPlanStepStatus(steps, "Verify go-images commit is mirrored internally", "succeeded")
			setPlanStepStatus(steps, "🚀 Queue go-images release", "succeeded")
			setPlanStepStatus(steps, "⌚ Wait for go-images release", "running")
		}
	}
	result := planResponse{Input: s.goImages.planInput, Steps: steps}
	if s.goImages.document != nil {
		result.SessionID = s.goImages.document.ID
	}
	result.Execution = s.goImagesExecutionResponseLocked()
	result.View = goImagesPlanView(
		s.goImages.planInput,
		s.goImages.source,
		s.goImages.rollbackSource,
		parameters,
		len(steps),
		restored,
	)
	return result
}

func setPlanStepStatus(steps []planStep, name, status string) {
	for i := range steps {
		if steps[i].Name == name {
			steps[i].Status = status
			return
		}
	}
}

func goImagesPlanView(
	input PlanInput,
	source GoImagesSource,
	rollbackSource *GoImagesRollbackSource,
	parameters map[string]string,
	stepCount int,
	restored bool,
) ProcessPlanView {
	modeName := string(input.Mode)
	if modeName != "" {
		modeName = strings.ToUpper(modeName[:1]) + modeName[1:]
	}
	view := ProcessPlanView{
		Subtitle:    fmt.Sprintf("%s release · pipeline %d · %d steps", modeName, goimagesworkflow.DefinitionID, stepCount),
		IntentBadge: parameters["publishRepoPrefix"],
		Facts: []ProcessPlanFact{{
			Label: "Pipeline source", Value: source.Branch, Detail: source.Commit,
		}},
		Request: &ProcessRequestPreview{
			Eyebrow: "Azure DevOps request preview · not sent",
			Title:   fmt.Sprintf("Pipeline %d · %s", goimagesworkflow.DefinitionID, goImagesPipelineName),
			Target:  goImagesPipelineOrg + "/" + goImagesPipelineProject,
		},
	}
	if restored {
		view.Subtitle += " · restored from disk"
	}
	for _, name := range sortedMapKeys(parameters) {
		view.Request.Fields = append(view.Request.Fields, ProcessRequestField{Name: name, Value: parameters[name]})
	}
	switch input.Mode {
	case goimagesworkflow.ModeNormal:
		view.IntentTitle = "Build current main and publish production images"
		view.ExecutionTitle = "Run production release"
		view.ExecutionWarning = "This builds current main, performs production signing, and publishes production images under public/."
		view.ExecutionConfirmation = "Confirm run to build, sign, and publish current main to public/."
		view.ExecutionButtonLabel = "Run production release"
	case goimagesworkflow.ModeRollback:
		view.IntentTitle = "Republish artifacts from build " + input.SourceBuildID
		view.ExecutionTitle = "Run rollback / republish"
		view.ExecutionWarning = "This republishes artifacts from build " + input.SourceBuildID + " under public/. It does not rebuild those images."
		view.ExecutionConfirmation = "Confirm run to republish artifacts from build " + input.SourceBuildID + " to public/."
		view.ExecutionButtonLabel = "Run rollback"
		if rollbackSource != nil {
			view.Facts = append(view.Facts, ProcessPlanFact{
				Label: "Artifact source", Value: fmt.Sprintf("Pipeline %d build %d", goimagesworkflow.DefinitionID, rollbackSource.BuildID),
				Href: rollbackSource.URL,
			})
		}
	case goimagesworkflow.ModeTest:
		view.IntentTitle = "Build current main and publish a dev/ test release"
		view.ExecutionTitle = "Run test release"
		view.ExecutionWarning = "This queues a real build and may use production signing resources, but publication is fixed to dev/ rather than public/."
		view.ExecutionConfirmation = "Confirm run to queue pipeline 1023 with publication locked to dev/."
		view.ExecutionButtonLabel = "Run test release"
	}
	return view
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
