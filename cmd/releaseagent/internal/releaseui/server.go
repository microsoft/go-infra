// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package releaseui implements the local browser UI for planning and observing releases.
package releaseui

import (
	"context"
	"crypto/rand"
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
	"strings"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

const (
	sessionCookieName       = "releaseui_session"
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
	BuildID  int
	URL      string
	Versions []string
}

// GoImagesReadOnlyIntegration is the explicitly enabled Azure read boundary. It resolves current
// main and validates rollback builds but cannot queue, cancel, approve, or otherwise mutate a run.
type GoImagesReadOnlyIntegration struct {
	Preflight            func(context.Context) (string, error)
	ResolveCurrentSource func(context.Context) (GoImagesSource, error)
	ValidateRollback     func(context.Context, int) (GoImagesRollbackSource, error)
}

// GoImagesExecutionIntegration is the only real execution boundary. Its implementation must
// hardcode definition 1023 and microsoft/main, and derive parameters from the selected mode.
type GoImagesExecutionIntegration struct {
	NewService func(GoImagesExecutionRequest) (goimagesworkflow.Service, error)
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
	if server.readOnly != nil {
		if server.sessionStore == nil {
			return nil, errors.New("go-images source resolution requires a durable session store")
		}
		if server.readOnly.Preflight == nil || server.readOnly.ResolveCurrentSource == nil ||
			server.readOnly.ValidateRollback == nil {

			return nil, errors.New("go-images read-only integration is incomplete")
		}
	}
	if server.execution != nil {
		if server.sessionStore == nil || server.readOnly == nil {
			return nil, errors.New("go-images execution requires durable storage and read-only validation")
		}
		if server.execution.NewService == nil {
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
		processID := definition.ID
		mux.HandleFunc("GET "+processPath(processID), s.handlePage)
		workflow := definition.Workflow
		prefix := "/api/processes/" + processID
		preflight := workflow.Preflight
		if workflow.DurableAction {
			processName := definition.Name
			preflight = func(server *Server, response http.ResponseWriter, request *http.Request) {
				server.handleProcessRunPreflight(processID, processName, response, request)
			}
		}
		mux.HandleFunc("GET "+prefix+"/preflight", func(response http.ResponseWriter, request *http.Request) {
			preflight(s, response, request)
		})
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
		mux.HandleFunc("GET "+prefix+"/plan", s.getProcessPlan(processID, getPlan))
		mux.HandleFunc("POST "+prefix+"/plan", s.prepareProcess(processID, prepare))
		if workflow.Simulate != nil {
			mux.HandleFunc("POST "+prefix+"/simulate", s.requireActiveProcess(processID, workflow.Simulate))
		}
		mux.HandleFunc("POST "+prefix+"/start", s.requireActiveProcess(processID, start))
		mux.HandleFunc("GET "+prefix+"/state", s.requireActiveProcess(processID, (*Server).handleState))
		mux.HandleFunc("GET "+prefix+"/events", s.requireActiveProcess(processID, (*Server).handleEvents))
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/processes/{id}", s.handleProcess)
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
	Status    string   `json:"status,omitempty"`
}

type planResponse struct {
	Input     PlanInput         `json:"input"`
	Steps     []planStep        `json:"steps"`
	SessionID string            `json:"sessionId"`
	Execution executionResponse `json:"execution"`
	View      ProcessPlanView   `json:"view"`
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

type pipelineRun struct {
	BuildID   string `json:"buildId,omitempty"`
	URL       string `json:"url,omitempty"`
	LinkLabel string `json:"linkLabel,omitempty"`
	Complete  bool   `json:"complete"`
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
	Mark      string    `json:"mark"`
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
	Href        string `json:"href"`
}

type processDetail struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Mark             string         `json:"mark"`
	Description      string         `json:"description"`
	DocumentationURL string         `json:"documentationUrl"`
	Workflow         workflowDetail `json:"workflow"`
}

type workflowDetail struct {
	Heading     string         `json:"heading"`
	Description string         `json:"description,omitempty"`
	SubmitLabel string         `json:"submitLabel"`
	Inputs      []ProcessInput `json:"inputs"`
	CanSimulate bool           `json:"canSimulate"`
}

func (s *Server) handleProcess(response http.ResponseWriter, request *http.Request) {
	definition, ok := s.processes.process(request.PathValue("id"))
	if !ok {
		http.NotFound(response, request)
		return
	}
	detail := processDetail{
		ID: definition.ID, Name: definition.Name, Mark: definition.Mark,
		Description: definition.Description, DocumentationURL: definition.DocumentationURL,
		Workflow: workflowDetail{
			Heading: definition.Workflow.Heading, Description: definition.Workflow.Description,
			SubmitLabel: definition.Workflow.SubmitLabel,
			Inputs:      append([]ProcessInput(nil), definition.Workflow.Inputs...),
			CanSimulate: definition.Workflow.Simulate != nil,
		},
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
		Mark: "GI", Name: "Go images", Mode: string(s.goImages.document.Input.Mode),
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
		Mark: definition.Mark, Name: definition.Name,
		Status: status, RunID: runID, RunLabel: "Target", UpdatedAt: run.UpdatedAt, Href: processPath(run.ProcessID),
	}
}

func describeSteps(steps []*coordinator.Step) []planStep {
	descriptions := make([]planStep, 0, len(steps))
	for _, step := range steps {
		description := planStep{Name: step.Name, DependsOn: make([]string, len(step.DependsOn)), Status: "waiting"}
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
