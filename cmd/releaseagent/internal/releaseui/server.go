// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package releaseui implements the local browser UI for planning and observing releases.
package releaseui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
)

const (
	sessionCookieName       = "releaseui_session"
	goImagesPipelineID      = 1023
	goImagesSourceBranch    = "refs/heads/microsoft/main"
	goImagesProcessID       = "go-images"
	goImagesPipelineName    = "microsoft-go-images (official)"
	goImagesPipelineOrg     = "dnceng"
	goImagesPipelineProject = "internal"

	legacyGoImagesWorkflowRevision5Digest = "7182e0900b9b575ad50962c2e3a7586ff4fb713ae749e4e8ca22e3de53358c40"
)

//go:embed web/*
var webFiles embed.FS

// Server hosts a single local release session. Dashboard responses use slices so the storage model
// can grow to multiple concurrent sessions without changing the browser contract.
type Server struct {
	ctx       context.Context
	token     string
	demoDelay time.Duration
	lookPath  executableLookup

	sessionStore session.Store
	readOnly     *GoImagesReadOnlyIntegration
	execution    *GoImagesExecutionIntegration

	mu                sync.Mutex
	steps             []*coordinator.Step
	input             PlanInput
	source            GoImagesSource
	rollbackSource    *GoImagesRollbackSource
	releaseInput      *releasesteps.Input
	releaseState      *releasesteps.State
	document          *session.Document
	runner            *coordinator.StepRunner
	simulationRunning bool
	releaseRunning    bool
	restoredFromDisk  bool
}

// GoImagesSource is the exact current microsoft/main source selected entirely by the server.
type GoImagesSource struct {
	Branch   string   `json:"branch"`
	Commit   string   `json:"commit"`
	Versions []string `json:"versions,omitempty"`
}

// GoImagesRollbackSource describes a validated successful build whose artifacts may be republished.
type GoImagesRollbackSource struct {
	BuildID       int      `json:"buildId"`
	URL           string   `json:"url,omitempty"`
	SourceBranch  string   `json:"sourceBranch"`
	SourceVersion string   `json:"sourceVersion"`
	Versions      []string `json:"versions"`
}

// GoImagesOngoingRun is a read-only view of one currently waiting or running pipeline 1023 build.
type GoImagesOngoingRun struct {
	BuildID int
	Mode    releasesteps.GoImagesReleaseMode
	Status  string
	URL     string
	Queued  time.Time
}

// GoImagesReadOnlyIntegration is the explicitly enabled Azure read boundary. It resolves current
// main and validates rollback builds but cannot queue, cancel, approve, or otherwise mutate a run.
type GoImagesReadOnlyIntegration struct {
	DefinitionID         int
	Preflight            func(context.Context) (string, error)
	ResolveCurrentSource func(context.Context) (GoImagesSource, error)
	ValidateRollback     func(context.Context, int) (GoImagesRollbackSource, error)
	ListOngoing          func(context.Context) ([]GoImagesOngoingRun, error)
}

// GoImagesExecutionIntegration is the only real execution boundary. Its implementation must
// hardcode definition 1023 and microsoft/main, and derive parameters from the selected mode.
type GoImagesExecutionIntegration struct {
	DefinitionID int
	Preflight    func(context.Context) (string, error)
	NewService   func(GoImagesExecutionRequest) (releasesteps.GoImagesReleaseService, error)
}

// GoImagesExecutionRequest binds one real run to a confirmed durable plan.
type GoImagesExecutionRequest struct {
	Mode                 releasesteps.GoImagesReleaseMode
	SessionID            string
	ExecutionDigest      string
	Versions             []string
	SourceBuildID        string
	SourceVersion        string
	PreviousQueueAttempt bool
}

// Option customizes a Server.
type Option func(*Server)

// WithDemoDelay changes the simulated duration of each step. It is primarily useful in tests.
func WithDemoDelay(delay time.Duration) Option {
	return func(server *Server) {
		server.demoDelay = delay
	}
}

// WithSessionStore enables durable, non-secret release plan persistence and restoration.
func WithSessionStore(store session.Store) Option {
	return func(server *Server) {
		server.sessionStore = store
	}
}

// WithExecutableLookup replaces local executable discovery. It is intended for hermetic tests.
func WithExecutableLookup(lookup func(string) (string, error)) Option {
	return func(server *Server) {
		server.lookPath = lookup
	}
}

// WithGoImagesReadOnlyIntegration enables current-main resolution and rollback validation.
func WithGoImagesReadOnlyIntegration(integration GoImagesReadOnlyIntegration) Option {
	return func(server *Server) {
		server.readOnly = &integration
	}
}

// WithGoImagesExecutionIntegration enables two-step-confirmed real pipeline execution.
func WithGoImagesExecutionIntegration(integration GoImagesExecutionIntegration) Option {
	return func(server *Server) {
		server.execution = &integration
	}
}

// New creates a local release UI server. External execution exists only when the explicit
// execution option is supplied and all server-side safety boundaries validate.
func New(ctx context.Context, options ...Option) (*Server, error) {
	if ctx == nil {
		return nil, errors.New("server context is nil")
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}
	server := &Server{
		ctx:       ctx,
		token:     base64.RawURLEncoding.EncodeToString(tokenBytes),
		demoDelay: 250 * time.Millisecond,
		lookPath:  defaultExecutableLookup,
		runner:    &coordinator.StepRunner{},
	}
	for _, option := range options {
		option(server)
	}
	if server.demoDelay < 0 {
		return nil, errors.New("demo delay cannot be negative")
	}
	if server.lookPath == nil {
		return nil, errors.New("executable lookup is nil")
	}
	if server.readOnly != nil {
		if server.sessionStore == nil {
			return nil, errors.New("go-images source resolution requires a durable session store")
		}
		if server.readOnly.DefinitionID != goImagesPipelineID {
			return nil, fmt.Errorf("go-images definition %d is not allowlisted", server.readOnly.DefinitionID)
		}
		if server.readOnly.Preflight == nil || server.readOnly.ResolveCurrentSource == nil ||
			server.readOnly.ValidateRollback == nil || server.readOnly.ListOngoing == nil {

			return nil, errors.New("go-images read-only integration is incomplete")
		}
	}
	if server.execution != nil {
		if server.sessionStore == nil || server.readOnly == nil {
			return nil, errors.New("go-images execution requires durable storage and read-only validation")
		}
		if server.execution.DefinitionID != goImagesPipelineID {
			return nil, fmt.Errorf("go-images execution definition %d is not allowlisted", server.execution.DefinitionID)
		}
		if server.execution.Preflight == nil || server.execution.NewService == nil {
			return nil, errors.New("go-images execution integration is incomplete")
		}
	}
	if err := server.restoreSession(); err != nil {
		return nil, err
	}
	if err := server.resumeRestoredMonitoring(); err != nil {
		return nil, err
	}
	return server, nil
}

// LaunchURL adds the one-time session token to baseURL. Visiting this URL establishes an HTTP-only
// session cookie and immediately redirects to a clean URL without the token.
func (s *Server) LaunchURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" {
		return "", fmt.Errorf("base URL must be an HTTP origin, got %q", baseURL)
	}
	query := parsed.Query()
	query.Set("token", s.token)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// Handler returns the HTTP handler for the local UI.
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(fmt.Sprintf("create embedded web filesystem: %v", err))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /go-images", s.handlePage)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/releases/ongoing", s.handleOngoingReleases)
	mux.HandleFunc("GET /api/plan", s.handleGetPlan)
	mux.HandleFunc("POST /api/plan", s.handlePlan)
	mux.HandleFunc("GET /api/preflight", s.handlePreflight)
	mux.HandleFunc("POST /api/demo/start", s.handleDemoStart)
	mux.HandleFunc("POST /api/go-images/release/start", s.handleReleaseStart)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return s.withSecurityHeaders(s.authenticate(mux))
}

func (s *Server) handlePage(response http.ResponseWriter, request *http.Request) {
	name := "index.html"
	switch request.URL.Path {
	case "/":
	case "/go-images":
		name = "go-images.html"
	default:
		http.NotFound(response, request)
		return
	}
	content, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.Error(response, "embedded UI is unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = response.Write(content)
}

// PlanInput is the complete browser-controlled input surface for a go-images release. The server
// resolves branch, commit, versions, definition, and prefix. Only rollback accepts a build ID.
type PlanInput struct {
	Mode          releasesteps.GoImagesReleaseMode `json:"mode"`
	SourceBuildID string                           `json:"sourceBuildId,omitempty"`
}

type planStep struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
}

type planResponse struct {
	Input          PlanInput               `json:"input"`
	Source         GoImagesSource          `json:"source"`
	RollbackSource *GoImagesRollbackSource `json:"rollbackSource,omitempty"`
	Steps          []planStep              `json:"steps"`
	Pipeline       pipelinePreview         `json:"pipeline"`
	SessionID      string                  `json:"sessionId,omitempty"`
	Restored       bool                    `json:"restored"`
	Execution      executionResponse       `json:"execution"`
}

type pipelinePreview struct {
	DefinitionID int               `json:"definitionId"`
	Organization string            `json:"organization"`
	Project      string            `json:"project"`
	Name         string            `json:"name"`
	Parameters   map[string]string `json:"parameters"`
	Locked       bool              `json:"locked"`
}

type pipelineRun struct {
	BuildID  string `json:"buildId,omitempty"`
	URL      string `json:"url,omitempty"`
	Complete bool   `json:"complete"`
	Result   string `json:"result,omitempty"`
}

type executionResponse struct {
	Enabled           bool        `json:"enabled"`
	Eligible          bool        `json:"eligible"`
	PlanDigest        string      `json:"planDigest,omitempty"`
	UnavailableReason string      `json:"unavailableReason,omitempty"`
	Run               pipelineRun `json:"run"`
}

type dashboardResponse struct {
	Ongoing   []releaseSummary `json:"ongoing"`
	Recent    []releaseSummary `json:"recent"`
	Processes []processSummary `json:"processes"`
}

type releaseSummary struct {
	ID        string                           `json:"id"`
	ProcessID string                           `json:"processId"`
	Name      string                           `json:"name"`
	Mode      releasesteps.GoImagesReleaseMode `json:"mode"`
	Status    string                           `json:"status"`
	BuildID   string                           `json:"buildId,omitempty"`
	UpdatedAt time.Time                        `json:"updatedAt"`
	Href      string                           `json:"href"`
}

type processSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Href        string `json:"href,omitempty"`
	Available   bool   `json:"available"`
	Status      string `json:"status"`
}

func (s *Server) handleDashboard(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	result := dashboardResponse{
		Ongoing: make([]releaseSummary, 0),
		Recent:  make([]releaseSummary, 0),
		Processes: []processSummary{
			{
				ID: goImagesProcessID, Name: "Go images", Available: true, Href: "/go-images", Status: "Available",
				Description: "Build, sign, publish, test, or republish the Microsoft Build of Go container images.",
			},
			{
				ID: "go-infra", Name: "Go infrastructure", Status: "Planned",
				Description: "Infrastructure release automation will be added in a future iteration.",
			},
			{
				ID: "microsoft-go", Name: "Microsoft Build of Go", Status: "Future",
				Description: "The complete Microsoft Build of Go release process is intentionally out of scope for now.",
			},
		},
	}
	if s.document != nil {
		summary := s.releaseSummaryLocked()
		if s.document.State.Day.GoImagesReleaseComplete {
			result.Recent = append(result.Recent, summary)
		} else {
			result.Ongoing = append(result.Ongoing, summary)
		}
	}
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleOngoingReleases(response http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	if s.readOnly == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "live release tracking is not enabled")
		return
	}
	preflight := s.readOnly.Preflight
	listOngoing := s.readOnly.ListOngoing
	s.mu.Unlock()
	if _, err := preflight(request.Context()); err != nil {
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("Azure preflight failed: %v", err))
		return
	}
	runs, err := listOngoing(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("list ongoing go-images releases: %v", err))
		return
	}
	releases := make([]releaseSummary, 0, len(runs))
	for _, run := range runs {
		if run.BuildID <= 0 || run.Status == "" {
			writeError(response, http.StatusBadGateway, "ongoing release discovery returned invalid run metadata")
			return
		}
		mode := run.Mode
		if mode == "" {
			mode = releasesteps.GoImagesReleaseModeNormal
		}
		releases = append(releases, releaseSummary{
			ID: "azdo-" + strconv.Itoa(run.BuildID), ProcessID: goImagesProcessID, Name: "Go images",
			Mode: mode, Status: run.Status, BuildID: strconv.Itoa(run.BuildID), UpdatedAt: run.Queued, Href: run.URL,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"releases": releases})
}

func (s *Server) releaseSummaryLocked() releaseSummary {
	state := s.document.State.Day
	status := "ready"
	switch {
	case state.GoImagesReleaseComplete:
		status = state.GoImagesReleaseResult
		if status == "" {
			status = "succeeded"
		}
	case state.GoImagesReleaseBuildID != "":
		status = "running"
	case state.GoImagesReleaseQueueAttempted:
		status = "reconciling"
	}
	return releaseSummary{
		ID: s.document.ID, ProcessID: goImagesProcessID, Name: "Go images", Mode: state.GoImagesReleaseMode,
		Status: status, BuildID: state.GoImagesReleaseBuildID, UpdatedAt: s.document.UpdatedAt, Href: "/go-images",
	}
}

func (s *Server) handleGetPlan(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	result := s.planResponseLocked(s.restoredFromDisk)
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
	if s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
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
	if normalized.Mode == releasesteps.GoImagesReleaseModeRollback {
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

	releaseInput := &releasesteps.Input{
		Versions:                  versions,
		GoImagesReleaseMode:       normalized.Mode,
		GoImagesSourceBranch:      source.Branch,
		GoImagesSourceVersion:     source.Commit,
		GoImagesSourceBuildID:     normalized.SourceBuildID,
		RunnerGitHubUser:          "ghost",
		TargetAzDOGoImagesRepo:    releasesteps.GoImagesInternalMirrorTarget,
		MicrosoftGoImagesPipeline: goImagesPipelineID,
	}
	steps, releaseState, err := releasesteps.CreateGoImagesPipelineGraphWithCheckpoint(
		releaseInput, nil, nil, disabledServices{}, s.checkpointReleaseState,
	)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images plan: %v", err))
		return
	}
	document, err := session.NewDocument(releaseInput, releaseState, steps, time.Now())
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create durable release session: %v", err))
		return
	}

	s.mu.Lock()
	if s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if err := s.sessionStore.Save(request.Context(), document); err != nil {
		s.mu.Unlock()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist release session: %v", err))
		return
	}
	s.steps = steps
	s.input = normalized
	s.source = source
	s.rollbackSource = rollbackSource
	s.releaseInput = releaseInput
	s.releaseState = releaseState
	s.document = document
	s.runner = &coordinator.StepRunner{}
	s.restoredFromDisk = false
	result := s.planResponseLocked(false)
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) planResponseLocked(restored bool) planResponse {
	parameters := map[string]string{}
	if s.releaseInput != nil {
		if derived, err := releasesteps.GoImagesPipelineParametersForMode(
			s.releaseInput.GoImagesReleaseMode,
			s.releaseInput.GoImagesSourceBuildID,
		); err == nil {
			parameters = derived
		}
	}
	result := planResponse{
		Input: s.input, Source: s.source, RollbackSource: s.rollbackSource, Steps: describeSteps(s.steps),
		Pipeline: pipelinePreview{
			DefinitionID: goImagesPipelineID, Organization: goImagesPipelineOrg, Project: goImagesPipelineProject,
			Name: goImagesPipelineName, Parameters: parameters, Locked: true,
		},
		Restored: restored,
	}
	if s.document != nil {
		result.SessionID = s.document.ID
	}
	result.Execution = s.executionResponseLocked()
	return result
}

func (s *Server) executionResponseLocked() executionResponse {
	result := executionResponse{Enabled: s.execution != nil}
	if s.document == nil || s.releaseInput == nil || len(s.steps) == 0 {
		return result
	}
	state := s.document.State.Day
	result.Run = pipelineRun{
		BuildID: state.GoImagesReleaseBuildID, Complete: state.GoImagesReleaseComplete,
		Result: state.GoImagesReleaseResult,
	}
	if result.Run.BuildID != "" {
		result.Run.URL = "https://dev.azure.com/dnceng/internal/_build/results?buildId=" + result.Run.BuildID
	}
	if !result.Enabled {
		result.UnavailableReason = "Real pipeline execution is disabled. The workflow can still be simulated."
		return result
	}
	plan, err := session.NewPlan(s.steps)
	if err != nil {
		result.UnavailableReason = "The release graph is invalid."
		return result
	}
	parameters, err := releasesteps.GoImagesPipelineParametersForMode(
		s.releaseInput.GoImagesReleaseMode,
		s.releaseInput.GoImagesSourceBuildID,
	)
	if err != nil {
		result.UnavailableReason = err.Error()
		return result
	}
	payload := struct {
		SessionID        string
		ExecutionDigest  string
		Mode             releasesteps.GoImagesReleaseMode
		Versions         []string
		SourceBranch     string
		SourceVersion    string
		SourceBuildID    string
		DefinitionID     int
		Parameters       map[string]string
		WorkflowRevision int
		WorkflowDigest   string
	}{
		SessionID: s.document.ID, ExecutionDigest: s.document.ExecutionDigest,
		Mode: s.releaseInput.GoImagesReleaseMode, Versions: append([]string(nil), s.releaseInput.Versions...),
		SourceBranch: s.releaseInput.GoImagesSourceBranch, SourceVersion: s.releaseInput.GoImagesSourceVersion,
		SourceBuildID: s.releaseInput.GoImagesSourceBuildID, DefinitionID: goImagesPipelineID,
		Parameters: parameters, WorkflowRevision: plan.WorkflowRevision, WorkflowDigest: plan.Digest,
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
	if errors.Is(err, session.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load release session: %w", err)
	}
	if document.Plan.WorkflowRevision == session.MigratableWorkflowRevision {
		document, err = s.migrateGoImagesWorkflowRevision5(document)
		if err != nil {
			return fmt.Errorf("migrate release session: %w", err)
		}
	}
	input := document.Input
	state := document.State
	steps, restoredState, err := releasesteps.CreateGoImagesPipelineGraphWithCheckpoint(
		&input, nil, &state, disabledServices{}, s.checkpointReleaseState,
	)
	if err != nil {
		return fmt.Errorf("reconstruct release session: %w", err)
	}
	plan, err := session.NewPlan(steps)
	if err != nil {
		return fmt.Errorf("fingerprint reconstructed release session: %w", err)
	}
	if err := document.MatchesPlan(plan); err != nil {
		return fmt.Errorf("restore release session: %w", err)
	}
	s.steps = steps
	s.input = PlanInput{Mode: input.GoImagesReleaseMode, SourceBuildID: input.GoImagesSourceBuildID}
	s.source = GoImagesSource{
		Branch: input.GoImagesSourceBranch, Commit: input.GoImagesSourceVersion,
		Versions: append([]string(nil), input.Versions...),
	}
	if input.GoImagesReleaseMode == releasesteps.GoImagesReleaseModeRollback {
		buildID, _ := strconv.Atoi(input.GoImagesSourceBuildID)
		s.rollbackSource = &GoImagesRollbackSource{BuildID: buildID, Versions: append([]string(nil), input.Versions...)}
	}
	s.releaseInput = &input
	s.releaseState = restoredState
	s.document = document
	s.runner = &coordinator.StepRunner{}
	s.restoredFromDisk = true
	return nil
}

func (s *Server) migrateGoImagesWorkflowRevision5(document *session.Document) (*session.Document, error) {
	if document.Plan.Digest != legacyGoImagesWorkflowRevision5Digest {
		return nil, fmt.Errorf("revision-5 plan digest %q is not the known go-images workflow", document.Plan.Digest)
	}
	if document.Input.MicrosoftGoImagesPipeline != goImagesPipelineID {
		return nil, fmt.Errorf("revision-5 session targets pipeline %d, expected %d", document.Input.MicrosoftGoImagesPipeline, goImagesPipelineID)
	}
	if document.Input.TargetAzDOGoImagesRepo != "" &&
		document.Input.TargetAzDOGoImagesRepo != releasesteps.GoImagesInternalMirrorTarget {

		return nil, fmt.Errorf("revision-5 session has unexpected mirror target %q", document.Input.TargetAzDOGoImagesRepo)
	}
	if !document.State.Day.GoImagesReleaseQueueAttempted {
		return nil, errors.New("revision-5 session has no checkpointed queue attempt")
	}
	buildID, err := strconv.Atoi(document.State.Day.GoImagesReleaseBuildID)
	if err != nil || buildID <= 0 {
		return nil, fmt.Errorf(
			"revision-5 session has no checkpointed build ID; refusing migration because queue status is uncertain",
		)
	}

	input := document.Input
	input.TargetAzDOGoImagesRepo = releasesteps.GoImagesInternalMirrorTarget
	_, initializedState, err := releasesteps.CreateGoImagesPipelineGraph(&input, nil, nil, disabledServices{})
	if err != nil {
		return nil, fmt.Errorf("initialize revision-6 state: %w", err)
	}
	state := document.State
	state.InputChecksum = initializedState.InputChecksum
	steps, migratedState, err := releasesteps.CreateGoImagesPipelineGraph(&input, nil, &state, disabledServices{})
	if err != nil {
		return nil, fmt.Errorf("create revision-6 graph: %w", err)
	}
	upgraded, err := document.UpgradeWorkflow(&input, migratedState, steps, time.Now())
	if err != nil {
		return nil, fmt.Errorf("upgrade revision-5 document: %w", err)
	}
	if err := s.sessionStore.Save(s.ctx, upgraded); err != nil {
		return nil, fmt.Errorf("persist revision-6 session: %w", err)
	}
	return upgraded, nil
}

func (s *Server) resumeRestoredMonitoring() error {
	if s.execution == nil || s.document == nil || s.releaseInput == nil || s.releaseState == nil {
		return nil
	}
	buildIDText := s.releaseState.Day.GoImagesReleaseBuildID
	if buildIDText == "" || s.releaseState.Day.GoImagesReleaseComplete {
		return nil
	}
	buildID, err := strconv.Atoi(buildIDText)
	if err != nil || buildID <= 0 {
		return fmt.Errorf("restore go-images monitoring with invalid build ID %q", buildIDText)
	}
	intent := s.executionResponseLocked()
	if !intent.Eligible || intent.PlanDigest == "" {
		return errors.New("restore go-images monitoring without an eligible execution plan")
	}
	service, err := s.execution.NewService(GoImagesExecutionRequest{
		Mode: s.releaseInput.GoImagesReleaseMode, SessionID: s.document.ID, ExecutionDigest: intent.PlanDigest,
		Versions: append([]string(nil), s.releaseInput.Versions...), SourceBuildID: s.releaseInput.GoImagesSourceBuildID,
		SourceVersion:        s.releaseInput.GoImagesSourceVersion,
		PreviousQueueAttempt: s.releaseState.Day.GoImagesReleaseQueueAttempted,
	})
	if err != nil {
		return fmt.Errorf("restore go-images monitoring service: %w", err)
	}
	monitor := importedRunMonitor{
		buildID: buildID,
		monitor: func(ctx context.Context, id int) error {
			return service.PollPipelineComplete(ctx, strconv.Itoa(id), nil)
		},
	}
	input := *s.releaseInput
	steps, state, err := releasesteps.CreateGoImagesPipelineGraphWithCheckpoint(
		&input, nil, s.releaseState, monitor, s.checkpointReleaseState,
	)
	if err != nil {
		return fmt.Errorf("restore go-images monitoring graph: %w", err)
	}
	actualPlan, err := session.NewPlan(steps)
	if err != nil || actualPlan.Digest != s.document.Plan.Digest ||
		actualPlan.WorkflowRevision != s.document.Plan.WorkflowRevision {

		return errors.New("restored go-images monitoring graph no longer matches the persisted plan")
	}
	s.steps = steps
	s.releaseState = state
	s.runner = &coordinator.StepRunner{}
	s.releaseRunning = true
	runner := s.runner
	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.finishRelease()
	}()
	return nil
}

func (s *Server) checkpointReleaseState(ctx context.Context, state *releasesteps.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionStore == nil {
		return nil
	}
	if s.document == nil {
		return errors.New("cannot checkpoint release state before creating a session document")
	}
	document, err := s.document.WithState(state, time.Now())
	if err != nil {
		return fmt.Errorf("update release session document: %w", err)
	}
	if err := s.sessionStore.Save(ctx, document); err != nil {
		return fmt.Errorf("persist release state checkpoint: %w", err)
	}
	s.document = document
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
	if s.document == nil || s.releaseInput == nil || s.releaseState == nil {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "prepare a go-images release first")
		return
	}
	intent := s.executionResponseLocked()
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
	if s.releaseState.Day.GoImagesReleaseComplete {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the go-images release is already complete")
		return
	}
	if s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a workflow is already running")
		return
	}
	preflight := s.execution.Preflight
	resolveSource := s.readOnly.ResolveCurrentSource
	validateRollback := s.readOnly.ValidateRollback
	newService := s.execution.NewService
	input := *s.releaseInput
	state := s.releaseState
	previousQueueAttempt := state.Day.GoImagesReleaseQueueAttempted
	sessionID := s.document.ID
	expectedPlan := s.document.Plan
	alreadyQueued := state.Day.GoImagesReleaseBuildID != ""
	s.releaseRunning = true
	s.mu.Unlock()

	if !alreadyQueued {
		current, err := resolveSource(request.Context())
		if err != nil {
			s.finishRelease()
			writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("re-resolve current microsoft/main: %v", err))
			return
		}
		if current.Branch != input.GoImagesSourceBranch || current.Commit != input.GoImagesSourceVersion {
			s.finishRelease()
			writeError(response, http.StatusConflict, "microsoft/main changed after this plan was prepared; refresh the plan before queueing")
			return
		}
		if input.GoImagesReleaseMode == releasesteps.GoImagesReleaseModeRollback {
			buildID, _ := strconv.Atoi(input.GoImagesSourceBuildID)
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
		Mode: input.GoImagesReleaseMode, SessionID: sessionID, ExecutionDigest: intent.PlanDigest,
		Versions: append([]string(nil), input.Versions...), SourceBuildID: input.GoImagesSourceBuildID,
		SourceVersion: input.GoImagesSourceVersion, PreviousQueueAttempt: previousQueueAttempt,
	})
	if err != nil {
		s.finishRelease()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images execution service: %v", err))
		return
	}
	steps, state, err := releasesteps.CreateGoImagesPipelineGraphWithCheckpoint(
		&input, nil, state, service, s.checkpointReleaseState,
	)
	if err != nil {
		s.finishRelease()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create go-images execution graph: %v", err))
		return
	}
	actualPlan, err := session.NewPlan(steps)
	if err != nil || actualPlan.Digest != expectedPlan.Digest || actualPlan.WorkflowRevision != expectedPlan.WorkflowRevision {
		s.finishRelease()
		writeError(response, http.StatusConflict, "go-images execution graph no longer matches the confirmed plan")
		return
	}

	s.mu.Lock()
	s.steps = steps
	s.releaseState = state
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
	case releasesteps.GoImagesReleaseModeNormal, releasesteps.GoImagesReleaseModeTest:
		if input.SourceBuildID != "" {
			return PlanInput{}, fmt.Errorf("%s release does not accept a source build ID", input.Mode)
		}
	case releasesteps.GoImagesReleaseModeRollback:
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
	if source.Branch != goImagesSourceBranch {
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

func describeSteps(steps []*coordinator.Step) []planStep {
	descriptions := make([]planStep, 0, len(steps))
	for _, step := range steps {
		description := planStep{ID: step.ID, Name: step.Name, DependsOn: make([]string, len(step.DependsOn))}
		if step.Timeout != coordinator.NoTimeout {
			description.Timeout = step.Timeout.String()
		}
		for i, dependency := range step.DependsOn {
			description.DependsOn[i] = dependency.ID
		}
		descriptions = append(descriptions, description)
	}
	return descriptions
}

func (s *Server) handleDemoStart(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "prepare a release before starting the simulation")
		return
	}
	if s.simulationRunning || s.releaseRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a workflow is already active")
		return
	}
	s.simulationRunning = true
	runner := s.runner
	steps := cloneForDemo(s.steps, s.demoDelay)
	s.mu.Unlock()
	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.mu.Lock()
		s.simulationRunning = false
		s.mu.Unlock()
	}()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "simulation started"})
}

func cloneForDemo(steps []*coordinator.Step, delay time.Duration) []*coordinator.Step {
	clones := make(map[*coordinator.Step]*coordinator.Step, len(steps))
	result := make([]*coordinator.Step, 0, len(steps))
	for _, step := range steps {
		clone := &coordinator.Step{
			ID: step.ID, Name: step.Name, Timeout: step.Timeout,
			Func: func(ctx context.Context) error {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			},
		}
		clones[step] = clone
		result = append(result, clone)
	}
	for original, clone := range clones {
		clone.DependsOn = make([]*coordinator.Step, len(original.DependsOn))
		for i, dependency := range original.DependsOn {
			clone.DependsOn[i] = clones[dependency]
		}
	}
	return result
}

func (s *Server) handleState(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	runner := s.runner
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, runner.Snapshot())
}

func (s *Server) handleEvents(response http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "prepare a release before subscribing")
		return
	}
	runner := s.runner
	s.mu.Unlock()
	streamRunnerEvents(response, request, runner)
}

func streamRunnerEvents(response http.ResponseWriter, request *http.Request, runner *coordinator.StepRunner) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusInternalServerError, "streaming is unsupported")
		return
	}
	initial, updates, unsubscribe := runner.Subscribe(64)
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	if err := writeServerEvent(response, initial); err != nil {
		return
	}
	flusher.Flush()
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case snapshot, ok := <-updates:
			if !ok {
				return
			}
			if err := writeServerEvent(response, snapshot); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := io.WriteString(response, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeServerEvent(writer io.Writer, snapshot coordinator.Snapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: state\ndata: %s\n\n", snapshot.Sequence, data)
	return err
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !isLoopbackHost(request.Host) {
			writeError(response, http.StatusMisdirectedRequest, "release UI only accepts loopback hosts")
			return
		}
		if cookie, err := request.Cookie(sessionCookieName); err == nil && secureEqual(cookie.Value, s.token) {
			next.ServeHTTP(response, request)
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/" && secureEqual(request.URL.Query().Get("token"), s.token) {
			http.SetCookie(response, &http.Cookie{
				Name: sessionCookieName, Value: s.token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(response, request, "/", http.StatusSeeOther)
			return
		}
		writeError(response, http.StatusUnauthorized, "open the launch URL printed by releaseagent")
	})
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return parsed.Scheme == scheme && parsed.Host == request.Host && parsed.Path == ""
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func decodeJSON(response http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}
