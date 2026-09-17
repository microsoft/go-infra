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

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const sessionCookieName = "releaseui_session"

//go:embed web/*
var webFiles embed.FS

// Server hosts a single local release session.
type Server struct {
	ctx                 context.Context
	token               string
	demoDelay           time.Duration
	configuredProcesses []ReleaseProcess
	processes           *processRegistry
	activeProcessID     string
	processRunStore     ReleaseRunStore
	workItems           releaseWorkItemService
	initialWorkItem     *azdoworkitem.WorkItem

	selectionMu       sync.Mutex
	mu                sync.Mutex
	steps             []*coordinator.Step
	runner            *coordinator.StepRunner
	simulationRunning bool
	processRun        ReleaseRun
	processRunRecord  *ReleaseRunRecord
	processRunning    bool
}

// Option customizes a Server.
type Option func(*Server)

// WithDemoDelay changes the simulated duration of each step. It is primarily useful in tests.
func WithDemoDelay(delay time.Duration) Option {
	return func(server *Server) {
		server.demoDelay = delay
	}
}

// WithProcesses sets the release processes hosted by this server.
func WithProcesses(processes ...ReleaseProcess) Option {
	return func(server *Server) {
		server.configuredProcesses = append([]ReleaseProcess(nil), processes...)
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
		runner:    &coordinator.StepRunner{},
	}
	for _, option := range options {
		option(server)
	}
	var err error
	server.processes, err = newProcessRegistry(server.configuredProcesses...)
	if err != nil {
		return nil, err
	}
	if server.demoDelay < 0 {
		return nil, errors.New("demo delay cannot be negative")
	}
	if err := server.validateProcessExecutionConfiguration(); err != nil {
		return nil, err
	}
	initialWorkItem := server.initialWorkItem
	server.initialWorkItem = nil
	if initialWorkItem != nil {
		if _, err := server.restoreReleaseWorkItem(initialWorkItem); err != nil {
			return nil, fmt.Errorf("restore release work item %d: %w", initialWorkItem.ID, err)
		}
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
	for _, registered := range s.processes.ordered {
		definition := registered.definition
		processID := definition.ID
		mux.HandleFunc("GET "+processPath(processID), s.handlePage)
		prefix := "/api/processes/" + processID
		mux.HandleFunc("GET "+prefix+"/preflight", func(response http.ResponseWriter, request *http.Request) {
			s.handleProcessRunPreflight(processID, response, request)
		})
		mux.HandleFunc("GET "+prefix+"/plan", s.getProcessPlan(processID))
		mux.HandleFunc("POST "+prefix+"/plan", s.prepareProcess(processID))
		if definition.Workflow.CanSimulate {
			mux.HandleFunc("POST "+prefix+"/simulate", s.requireActiveProcess(processID, s.handleDemoStart))
		}
		mux.HandleFunc("POST "+prefix+"/start", s.requireActiveProcess(processID, func(response http.ResponseWriter, request *http.Request) {
			s.handleStartProcessRun(processID, response, request)
		}))
		mux.HandleFunc("GET "+prefix+"/state", s.requireActiveProcess(processID, s.handleState))
		mux.HandleFunc("GET "+prefix+"/events", s.requireActiveProcess(processID, s.handleEvents))
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("POST /api/release-work-items/{id}/select", s.handleSelectWorkItem)
	mux.HandleFunc("GET /api/release-work-items/{id}/export", s.handleExportWorkItem)
	mux.HandleFunc("POST /api/release-work-items/{id}/import", s.handleImportWorkItem)
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

func (s *Server) prepareProcess(processID string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		writer := &processResponseWriter{ResponseWriter: response}
		s.handlePrepareProcessRun(processID, writer, request)
		if writer.status < http.StatusOK || writer.status >= http.StatusMultipleChoices {
			return
		}
		s.mu.Lock()
		s.activeProcessID = processID
		s.mu.Unlock()
	}
}

func (s *Server) getProcessPlan(processID string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.mu.Lock()
		active := s.activeProcessID
		s.mu.Unlock()
		if active != "" && active != processID {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		s.handleGetProcessRun(processID, response)
	}
}

func (s *Server) requireActiveProcess(processID string, handler http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.mu.Lock()
		active := s.activeProcessID
		s.mu.Unlock()
		if active != "" && active != processID {
			writeError(response, http.StatusConflict, "prepare this release process first")
			return
		}
		handler(response, request)
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

type planStep struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Status    string   `json:"status,omitempty"`
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
	Result    string `json:"result,omitempty"`
	Complete  bool   `json:"complete"`
}

type workItemReference struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

type executionResponse struct {
	Enabled           bool               `json:"enabled"`
	Eligible          bool               `json:"eligible"`
	PlanDigest        string             `json:"planDigest,omitempty"`
	UnavailableReason string             `json:"unavailableReason,omitempty"`
	Run               pipelineRun        `json:"run"`
	WorkItem          *workItemReference `json:"workItem,omitempty"`
}

type dashboardResponse struct {
	Ongoing        []releaseSummary `json:"ongoing"`
	NeedsAttention []releaseSummary `json:"needsAttention"`
	Recent         []releaseSummary `json:"recent"`
	Processes      []processSummary `json:"processes"`
	TrackingError  string           `json:"trackingError,omitempty"`
}

type releaseSummary struct {
	Mark        string    `json:"mark"`
	Name        string    `json:"name"`
	Mode        string    `json:"mode,omitempty"`
	Status      string    `json:"status"`
	RunID       string    `json:"runId,omitempty"`
	RunLabel    string    `json:"runLabel,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Href        string    `json:"href"`
	WorkItemID  int       `json:"workItemId,omitempty"`
	WorkItemURL string    `json:"workItemUrl,omitempty"`
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
	registered, ok := s.processes.process(request.PathValue("id"))
	if !ok {
		http.NotFound(response, request)
		return
	}
	definition := registered.definition
	detail := processDetail{
		ID: definition.ID, Name: definition.Name, Mark: definition.Mark,
		Description: definition.Description, DocumentationURL: definition.DocumentationURL,
		Workflow: workflowDetail{
			Heading: definition.Workflow.Heading, Description: definition.Workflow.Description,
			SubmitLabel: definition.Workflow.SubmitLabel,
			Inputs:      append([]ProcessInput(nil), definition.Workflow.Inputs...),
			CanSimulate: definition.Workflow.CanSimulate,
		},
	}
	writeJSON(response, http.StatusOK, detail)
}

func (s *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	if s.workItems != nil {
		writeJSON(response, http.StatusOK, s.workItemDashboard(request.Context()))
		return
	}
	s.mu.Lock()
	result := dashboardResponse{
		Ongoing:        make([]releaseSummary, 0),
		NeedsAttention: make([]releaseSummary, 0),
		Recent:         make([]releaseSummary, 0),
		Processes:      s.processes.summaries(),
	}
	if s.processRun != nil {
		run := s.processRun.Snapshot()
		summary := s.processRunSummaryLocked(run)
		addDashboardRelease(&result, summary)
	}
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func addDashboardRelease(result *dashboardResponse, summary releaseSummary) {
	switch summary.Status {
	case "succeeded":
		result.Recent = append(result.Recent, summary)
	case "failed", "canceled", "uncertain":
		result.NeedsAttention = append(result.NeedsAttention, summary)
	default:
		result.Ongoing = append(result.Ongoing, summary)
	}
}

func (s *Server) processRunSummaryLocked(run *ReleaseRunState) releaseSummary {
	registered, _ := s.processes.process(run.ProcessID)
	definition := registered.definition
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
	if s.simulationRunning || s.processRunning {
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
		writeError(response, http.StatusUnauthorized, "open the launch URL printed by releaseui")
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
	return decodeJSONLimit(response, request, target, 64<<10)
}

func decodeJSONLimit(response http.ResponseWriter, request *http.Request, target any, limit int64) error {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
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
