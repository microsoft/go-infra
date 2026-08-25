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
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

const (
	sessionCookieName       = "releaseui_session"
	goImagesPipelineID      = 1023
	goImagesSourceBranch    = "refs/heads/microsoft/main"
	goImagesProcessID       = "go-images"
	goImagesPipelineName    = "microsoft-go-images (official)"
	goImagesPipelineOrg     = "dnceng"
	goImagesPipelineProject = "internal"
)

//go:embed web/*
var webFiles embed.FS

// goImagesRuntime owns the specialized go-images plan and session state. Other release processes,
// including go-infra, use the generic ProcessRun lifecycle.
type goImagesRuntime struct {
	planInput      PlanInput
	source         GoImagesSource
	rollbackSource *GoImagesRollbackSource
	workflowInput  *goimagesworkflow.Input
	workflowState  *goimagesworkflow.State
	document       *goimagessession.Document
	restored       bool
}

// Server hosts a single local release session. Dashboard responses use slices so the storage model
// can grow to multiple concurrent sessions without changing the browser contract.
type Server struct {
	ctx              context.Context
	token            string
	demoDelay        time.Duration
	lookPath         executableLookup
	processes        *processRegistry
	activeProcessID  string
	processExecutors map[string]ProcessExecutor
	processRunStore  ProcessRunStore

	sessionStore goimagessession.Store
	readOnly     *GoImagesReadOnlyIntegration
	execution    *GoImagesExecutionIntegration

	mu                sync.Mutex
	goImages          goImagesRuntime
	steps             []*coordinator.Step
	runner            *coordinator.StepRunner
	simulationRunning bool
	releaseRunning    bool
	processRun        *ProcessRun
	processRunning    bool
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
	Mode    goimagesworkflow.Mode
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
	NewService   func(GoImagesExecutionRequest) (goimagesworkflow.Service, error)
}

// GoImagesExecutionRequest binds one real run to a confirmed durable plan.
type GoImagesExecutionRequest struct {
	Mode                 goimagesworkflow.Mode
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
func WithSessionStore(store goimagessession.Store) Option {
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
		ctx:              ctx,
		token:            base64.RawURLEncoding.EncodeToString(tokenBytes),
		demoDelay:        250 * time.Millisecond,
		lookPath:         defaultExecutableLookup,
		runner:           &coordinator.StepRunner{},
		processExecutors: make(map[string]ProcessExecutor),
	}
	var err error
	server.processes, err = defaultProcessRegistry()
	if err != nil {
		return nil, err
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
	if err := server.validateProcessExecutionConfiguration(); err != nil {
		return nil, err
	}
	if err := server.restoreSession(); err != nil {
		return nil, err
	}
	if err := server.restoreProcessRun(); err != nil {
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
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return "", fmt.Errorf("base URL must be an HTTP or HTTPS origin, got %q", baseURL)
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
	for _, definition := range s.processes.ordered {
		if !definition.Available {
			continue
		}
		processID := definition.ID
		mux.HandleFunc("GET "+processPath(processID), s.handlePage)
		workflow := definition.Workflow
		if workflow == nil {
			continue
		}
		prefix := "/api/processes/" + processID
		preflight := workflow.Preflight
		if workflow.DurableAction {
			processName := definition.Name
			preflight = func(server *Server, response http.ResponseWriter, request *http.Request) {
				server.handleProcessRunPreflight(processID, processName, response, request)
			}
		}
		if preflight != nil {
			mux.HandleFunc("GET "+prefix+"/preflight", func(response http.ResponseWriter, request *http.Request) {
				preflight(s, response, request)
			})
		}
		getPlan := workflow.GetPlan
		prepare := workflow.Prepare
		start := workflow.Start
		if workflow.DurableAction {
			getPlan = func(server *Server, response http.ResponseWriter, _ *http.Request) {
				server.handleGetProcessRun(processID, response)
			}
			prepare = func(server *Server, response http.ResponseWriter, request *http.Request) {
				server.handlePrepareProcessRun(processID, response, request)
			}
			start = func(server *Server, response http.ResponseWriter, request *http.Request) {
				server.handleStartProcessRun(processID, response, request)
			}
		}
		if getPlan != nil {
			mux.HandleFunc("GET "+prefix+"/plan", s.getProcessPlan(processID, getPlan))
		}
		if prepare != nil {
			mux.HandleFunc("POST "+prefix+"/plan", s.prepareProcess(processID, prepare))
		}
		if workflow.Simulate != nil {
			mux.HandleFunc("POST "+prefix+"/simulate", s.requireActiveProcess(processID, workflow.Simulate))
		}
		if start != nil {
			mux.HandleFunc("POST "+prefix+"/start", s.requireActiveProcess(processID, start))
		}
		if getPlan != nil {
			mux.HandleFunc("GET "+prefix+"/state", s.requireActiveProcess(processID, (*Server).handleState))
			mux.HandleFunc("GET "+prefix+"/events", s.requireActiveProcess(processID, (*Server).handleEvents))
		}
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/processes/{id}", s.handleProcess)
	mux.HandleFunc("GET /api/releases/ongoing", s.handleOngoingReleases)
	return s.withSecurityHeaders(s.authenticate(mux))
}

type processResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *processResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *processResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (s *Server) prepareProcess(processID string, handler ProcessHandler) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		writer := &processResponseWriter{ResponseWriter: response}
		handler(s, writer, request)
		if writer.status < http.StatusOK || writer.status >= http.StatusMultipleChoices {
			return
		}
		s.mu.Lock()
		s.activeProcessID = processID
		s.mu.Unlock()
	}
}

func (s *Server) getProcessPlan(processID string, handler ProcessHandler) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.mu.Lock()
		active := s.activeProcessID
		s.mu.Unlock()
		if active != "" && active != processID {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		handler(s, response, request)
	}
}

func (s *Server) requireActiveProcess(processID string, handler ProcessHandler) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.mu.Lock()
		active := s.activeProcessID
		s.mu.Unlock()
		if active != "" && active != processID {
			writeError(response, http.StatusConflict, "prepare this release process first")
			return
		}
		handler(s, response, request)
	}
}

func (s *Server) handlePage(response http.ResponseWriter, request *http.Request) {
	name := "index.html"
	if request.URL.Path != "/" {
		var ok bool
		name, ok = s.processes.page(request.URL.Path)
		if !ok {
			http.NotFound(response, request)
			return
		}
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
	Mode          goimagesworkflow.Mode `json:"mode"`
	SourceBuildID string                `json:"sourceBuildId,omitempty"`
}

type planStep struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
	Status    string   `json:"status,omitempty"`
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
	View           ProcessPlanView         `json:"view"`
}

// ProcessPlanView contains process-neutral display data for the shared release page.
type ProcessPlanView struct {
	Subtitle              string                 `json:"subtitle"`
	IntentTitle           string                 `json:"intentTitle"`
	IntentBadge           string                 `json:"intentBadge,omitempty"`
	Facts                 []ProcessPlanFact      `json:"facts,omitempty"`
	Request               *ProcessRequestPreview `json:"request,omitempty"`
	ExecutionTitle        string                 `json:"executionTitle,omitempty"`
	ExecutionWarning      string                 `json:"executionWarning,omitempty"`
	ExecutionConfirmation string                 `json:"executionConfirmation,omitempty"`
	ExecutionButtonLabel  string                 `json:"executionButtonLabel,omitempty"`
}

// ProcessPlanFact is one resolved value shown while reviewing a prepared plan.
type ProcessPlanFact struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Href   string `json:"href,omitempty"`
}

// ProcessRequestPreview describes the locked external request without sending it.
type ProcessRequestPreview struct {
	Eyebrow string                `json:"eyebrow"`
	Title   string                `json:"title"`
	Target  string                `json:"target,omitempty"`
	Fields  []ProcessRequestField `json:"fields,omitempty"`
}

// ProcessRequestField is one name/value pair in an external request preview.
type ProcessRequestField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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
	BuildID   string `json:"buildId,omitempty"`
	URL       string `json:"url,omitempty"`
	LinkLabel string `json:"linkLabel,omitempty"`
	Complete  bool   `json:"complete"`
	Result    string `json:"result,omitempty"`
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
	ID        string    `json:"id"`
	ProcessID string    `json:"processId"`
	Name      string    `json:"name"`
	Mode      string    `json:"mode,omitempty"`
	Status    string    `json:"status"`
	RunID     string    `json:"runId,omitempty"`
	RunLabel  string    `json:"runLabel,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Href      string    `json:"href"`
}

type processSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mark        string `json:"mark"`
	Description string `json:"description"`
	Href        string `json:"href,omitempty"`
	Available   bool   `json:"available"`
	Status      string `json:"status"`
}

type processDetail struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Mark             string          `json:"mark"`
	Description      string          `json:"description"`
	DocumentationURL string          `json:"documentationUrl"`
	Methods          []ProcessMethod `json:"methods"`
	Workflow         *workflowDetail `json:"workflow,omitempty"`
}

type workflowDetail struct {
	Heading      string         `json:"heading"`
	Description  string         `json:"description,omitempty"`
	SubmitLabel  string         `json:"submitLabel,omitempty"`
	Inputs       []ProcessInput `json:"inputs,omitempty"`
	Steps        []ProcessStep  `json:"steps,omitempty"`
	HasPreflight bool           `json:"hasPreflight"`
	CanPrepare   bool           `json:"canPrepare"`
	CanSimulate  bool           `json:"canSimulate"`
	CanStart     bool           `json:"canStart"`
}

func (s *Server) handleProcess(response http.ResponseWriter, request *http.Request) {
	definition, ok := s.processes.process(request.PathValue("id"))
	if !ok || !definition.Available {
		http.NotFound(response, request)
		return
	}
	detail := processDetail{
		ID: definition.ID, Name: definition.Name, Mark: definition.Mark,
		Description: definition.Description, DocumentationURL: definition.DocumentationURL,
		Methods: append([]ProcessMethod(nil), definition.Methods...),
	}
	if definition.Workflow != nil {
		detail.Workflow = &workflowDetail{
			Heading: definition.Workflow.Heading, Description: definition.Workflow.Description,
			SubmitLabel:  definition.Workflow.SubmitLabel,
			Inputs:       append([]ProcessInput(nil), definition.Workflow.Inputs...),
			Steps:        append([]ProcessStep(nil), definition.Workflow.Steps...),
			HasPreflight: definition.Workflow.Preflight != nil || definition.Workflow.DurableAction,
			CanPrepare:   definition.Workflow.Prepare != nil || definition.Workflow.DurableAction,
			CanSimulate:  definition.Workflow.Simulate != nil,
			CanStart:     definition.Workflow.Start != nil || definition.Workflow.DurableAction,
		}
	}
	writeJSON(response, http.StatusOK, detail)
}

func (s *Server) handleDashboard(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	result := dashboardResponse{
		Ongoing:   make([]releaseSummary, 0),
		Recent:    make([]releaseSummary, 0),
		Processes: s.processes.summaries(),
	}
	if s.goImages.document != nil {
		summary := s.releaseSummaryLocked()
		if s.goImages.document.State.Complete {
			result.Recent = append(result.Recent, summary)
		} else {
			result.Ongoing = append(result.Ongoing, summary)
		}
	}
	if s.processRun != nil {
		summary := s.processRunSummaryLocked()
		if s.processRun.Complete {
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
			mode = goimagesworkflow.ModeNormal
		}
		releases = append(releases, releaseSummary{
			ID: "azdo-" + strconv.Itoa(run.BuildID), ProcessID: goImagesProcessID, Name: "Go images",
			Mode: string(mode), Status: run.Status, RunID: strconv.Itoa(run.BuildID), RunLabel: "Azure build",
			UpdatedAt: run.Queued, Href: run.URL,
		})
	}
	writeJSON(response, http.StatusOK, map[string]any{"releases": releases})
}

func (s *Server) releaseSummaryLocked() releaseSummary {
	state := s.goImages.document.State
	status := "ready"
	switch {
	case state.Complete:
		status = state.Result
		if status == "" {
			status = "succeeded"
		}
	case state.BuildID != "":
		status = "running"
	case state.QueueAttempted:
		status = "reconciling"
	}
	return releaseSummary{
		ID: s.goImages.document.ID, ProcessID: goImagesProcessID, Name: "Go images", Mode: string(s.goImages.document.Input.Mode),
		Status: status, RunID: state.BuildID, RunLabel: "Azure build",
		UpdatedAt: s.goImages.document.UpdatedAt, Href: "/go-images",
	}
}

func (s *Server) processRunSummaryLocked() releaseSummary {
	run := s.processRun
	definition, _ := s.processes.process(run.ProcessID)
	status := "ready"
	runID := ""
	if run.Started {
		status = "starting"
		runID = run.Target.ID
		if run.External != nil {
			runID = run.External.ID
			status = run.External.Status
			if status == "" {
				status = "running"
			}
		}
	}
	if run.Complete {
		status = run.Result
	}
	return releaseSummary{
		ID: run.Digest, ProcessID: run.ProcessID, Name: definition.Name,
		Status: status, RunID: runID, RunLabel: "Target", UpdatedAt: run.UpdatedAt, Href: processPath(run.ProcessID),
	}
}

func (s *Server) handleGetPlan(response http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	result := s.planResponseLocked(s.goImages.restored)
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
		SourceBranch:  source.Branch,
		SourceVersion: source.Commit,
		SourceBuildID: normalized.SourceBuildID,
		MirrorTarget:  goimagesworkflow.InternalMirrorTarget,
		PipelineID:    goImagesPipelineID,
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
	result := s.planResponseLocked(false)
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) planResponseLocked(restored bool) planResponse {
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
	result := planResponse{
		Input: s.goImages.planInput, Source: s.goImages.source, RollbackSource: s.goImages.rollbackSource, Steps: steps,
		Pipeline: pipelinePreview{
			DefinitionID: goImagesPipelineID, Organization: goImagesPipelineOrg, Project: goImagesPipelineProject,
			Name: goImagesPipelineName, Parameters: parameters, Locked: true,
		},
		Restored: restored,
	}
	if s.goImages.document != nil {
		result.SessionID = s.goImages.document.ID
	}
	result.Execution = s.executionResponseLocked()
	result.View = goImagesPlanView(result)
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

func goImagesPlanView(plan planResponse) ProcessPlanView {
	modeName := string(plan.Input.Mode)
	if modeName != "" {
		modeName = strings.ToUpper(modeName[:1]) + modeName[1:]
	}
	view := ProcessPlanView{
		Subtitle:    fmt.Sprintf("%s release · pipeline %d · %d steps", modeName, plan.Pipeline.DefinitionID, len(plan.Steps)),
		IntentBadge: plan.Pipeline.Parameters["publishRepoPrefix"],
		Facts: []ProcessPlanFact{{
			Label: "Pipeline source", Value: plan.Source.Branch, Detail: plan.Source.Commit,
		}},
		Request: &ProcessRequestPreview{
			Eyebrow: "Azure DevOps request preview · not sent",
			Title:   fmt.Sprintf("Pipeline %d · %s", plan.Pipeline.DefinitionID, plan.Pipeline.Name),
			Target:  plan.Pipeline.Organization + "/" + plan.Pipeline.Project,
		},
	}
	if plan.Restored {
		view.Subtitle += " · restored from disk"
	}
	for _, name := range sortedMapKeys(plan.Pipeline.Parameters) {
		view.Request.Fields = append(view.Request.Fields, ProcessRequestField{Name: name, Value: plan.Pipeline.Parameters[name]})
	}
	switch plan.Input.Mode {
	case goimagesworkflow.ModeNormal:
		view.IntentTitle = "Build current main and publish production images"
		view.ExecutionTitle = "Run production release"
		view.ExecutionWarning = "This builds current main, performs production signing, and publishes production images under public/."
		view.ExecutionConfirmation = "Confirm run to build, sign, and publish current main to public/."
		view.ExecutionButtonLabel = "Run production release"
	case goimagesworkflow.ModeRollback:
		view.IntentTitle = "Republish artifacts from build " + plan.Input.SourceBuildID
		view.ExecutionTitle = "Run rollback / republish"
		view.ExecutionWarning = "This republishes artifacts from build " + plan.Input.SourceBuildID + " under public/. It does not rebuild those images."
		view.ExecutionConfirmation = "Confirm run to republish artifacts from build " + plan.Input.SourceBuildID + " to public/."
		view.ExecutionButtonLabel = "Run rollback"
		if plan.RollbackSource != nil {
			view.Facts = append(view.Facts, ProcessPlanFact{
				Label: "Artifact source", Value: fmt.Sprintf("Pipeline %d build %d", goImagesPipelineID, plan.RollbackSource.BuildID),
				Href: plan.RollbackSource.URL,
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

func (s *Server) executionResponseLocked() executionResponse {
	result := executionResponse{Enabled: s.execution != nil}
	if s.goImages.document == nil || s.goImages.workflowInput == nil || len(s.steps) == 0 {
		return result
	}
	state := s.goImages.document.State
	result.Run = pipelineRun{
		BuildID: state.BuildID, Complete: state.Complete,
		Result: state.Result,
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
		SourceBranch     string
		SourceVersion    string
		SourceBuildID    string
		DefinitionID     int
		Parameters       map[string]string
		WorkflowRevision int
		WorkflowDigest   string
	}{
		SessionID: s.goImages.document.ID, ExecutionDigest: s.goImages.document.ExecutionDigest,
		Mode: s.goImages.workflowInput.Mode, Versions: append([]string(nil), s.goImages.workflowInput.Versions...),
		SourceBranch: s.goImages.workflowInput.SourceBranch, SourceVersion: s.goImages.workflowInput.SourceVersion,
		SourceBuildID: s.goImages.workflowInput.SourceBuildID, DefinitionID: goImagesPipelineID,
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
		Branch: input.SourceBranch, Commit: input.SourceVersion,
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
	intent := s.executionResponseLocked()
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
	monitor := importedRunMonitor{
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
	preflight := s.execution.Preflight
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
		if current.Branch != input.SourceBranch || current.Commit != input.SourceVersion {
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
		description := planStep{Name: step.Name, DependsOn: make([]string, len(step.DependsOn)), Status: "waiting"}
		if step.Timeout != coordinator.NoTimeout {
			description.Timeout = step.Timeout.String()
		}
		for i, dependency := range step.DependsOn {
			description.DependsOn[i] = dependency.Name
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
	if s.simulationRunning || s.releaseRunning || s.processRunning {
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
			Name: step.Name, Timeout: step.Timeout,
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
