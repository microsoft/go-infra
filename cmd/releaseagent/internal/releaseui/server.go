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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
	"github.com/microsoft/go-infra/goversion"
)

const sessionCookieName = "releaseui_session"

const (
	goImagesReleasePipelineID = 1151
	smokeConfirmationPhrase   = "QUEUE PIPELINE 1151 SMOKE TEST"
)

var (
	//go:embed web/*
	webFiles embed.FS

	versionPattern = regexp.MustCompile(`^1\.[0-9]+(?:(?:\.[0-9]+)?(?:beta|rc)[1-9][0-9]*|\.[0-9]+)-[1-9][0-9]*(?:-[a-z][a-z0-9-]*)?$`)
	runnerPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
)

// Server hosts a single local release-planning session.
type Server struct {
	ctx       context.Context
	token     string
	demoDelay time.Duration
	lookPath  executableLookup

	sessionStore session.Store
	smoke        *GoImagesSmokeExecution

	mu               sync.Mutex
	steps            []*coordinator.Step
	input            PlanInput
	releaseInput     *releasesteps.Input
	releaseState     *releasesteps.State
	document         *session.Document
	runner           *coordinator.StepRunner
	demoRunning      bool
	externalRunning  bool
	restoredFromDisk bool
}

// GoImagesSmokeExecution is the explicitly enabled, server-controlled real execution boundary.
// Browser input cannot change the definition ID, variable-group allowlist, or service factory.
type GoImagesSmokeExecution struct {
	DefinitionID  int
	VariableGroup string
	Preflight     func(context.Context) (string, error)
	NewService    func(GoImagesServiceRequest) (releasesteps.GoImagesReleaseService, error)
	FindRuns      func(context.Context, []string) ([]PipelineRunCandidate, error)
	ValidateRun   func(context.Context, int, []string) (PipelineRunCandidate, error)
}

// GoImagesServiceRequest binds queued-run metadata to immutable confirmed input.
type GoImagesServiceRequest struct {
	SessionID       string
	Versions        []string
	ExecutionDigest string
}

// PipelineRunCandidate is a recent run matching a canonical version set.
type PipelineRunCandidate struct {
	BuildID         int               `json:"buildId"`
	Status          string            `json:"status"`
	Result          string            `json:"result,omitempty"`
	State           string            `json:"state"`
	URL             string            `json:"url,omitempty"`
	QueueTime       time.Time         `json:"queueTime,omitempty"`
	SessionID       string            `json:"sessionId,omitempty"`
	VersionSet      string            `json:"versionSet"`
	ExecutionDigest string            `json:"executionDigest,omitempty"`
	CreatedByUI     bool              `json:"createdByUI"`
	Parameters      map[string]string `json:"parameters,omitempty"`
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

// WithGoImagesSmokeExecution enables the confirmation-protected one-time smoke-test path.
func WithGoImagesSmokeExecution(execution GoImagesSmokeExecution) Option {
	return func(server *Server) {
		server.smoke = &execution
	}
}

// New creates a local release UI server. The server performs no external release operations.
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
	if server.smoke != nil {
		if server.sessionStore == nil {
			return nil, errors.New("go-images smoke execution requires a durable session store")
		}
		if server.smoke.DefinitionID != goImagesReleasePipelineID {
			return nil, fmt.Errorf("go-images smoke definition %d is not allowlisted", server.smoke.DefinitionID)
		}
		if server.smoke.VariableGroup == "" {
			return nil, errors.New("go-images smoke variable-group allowlist is empty")
		}
		if server.smoke.Preflight == nil || server.smoke.NewService == nil ||
			server.smoke.FindRuns == nil || server.smoke.ValidateRun == nil {

			return nil, errors.New("go-images smoke execution is missing preflight, service, or discovery behavior")
		}
	}
	if err := server.restoreSession(); err != nil {
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
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/plan", s.handleGetPlan)
	mux.HandleFunc("POST /api/plan", s.handlePlan)
	mux.HandleFunc("GET /api/preflight", s.handlePreflight)
	mux.HandleFunc("POST /api/demo/start", s.handleDemoStart)
	mux.HandleFunc("POST /api/go-images/smoke/start", s.handleSmokeStart)
	mux.HandleFunc("POST /api/go-images/runs/search", s.handleRunSearch)
	mux.HandleFunc("POST /api/go-images/runs/import", s.handleRunImport)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	return s.withSecurityHeaders(s.authenticate(mux))
}

func (s *Server) handleIndex(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}
	content, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(response, "embedded UI is unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = response.Write(content)
}

// PlanInput contains the immutable, non-secret inputs represented by this first UI iteration.
type PlanInput struct {
	Versions      []string `json:"versions"`
	Security      bool     `json:"security"`
	Runner        string   `json:"runner"`
	VariableGroup string   `json:"variableGroup"`
	ReleaseIssue  int      `json:"releaseIssue,omitempty"`
}

type planStep struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn,omitempty"`
	Timeout   string   `json:"timeout,omitempty"`
}

type planResponse struct {
	Input      PlanInput       `json:"input"`
	Steps      []planStep      `json:"steps"`
	Pipeline   pipelinePreview `json:"pipeline"`
	DemoOnly   bool            `json:"demoOnly"`
	SessionID  string          `json:"sessionId,omitempty"`
	Restored   bool            `json:"restored"`
	PlanDigest string          `json:"planDigest"`
	SmokeTest  bool            `json:"smokeTest"`
	Run        pipelineRun     `json:"run"`
}

type pipelinePreview struct {
	DefinitionID int               `json:"definitionId"`
	Organization string            `json:"organization"`
	Project      string            `json:"project"`
	Name         string            `json:"name"`
	Parameters   map[string]string `json:"parameters"`
}

type pipelineRun struct {
	BuildID  string `json:"buildId,omitempty"`
	URL      string `json:"url,omitempty"`
	Complete bool   `json:"complete"`
	Imported bool   `json:"imported"`
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
	normalized, err := normalizeInput(input)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if s.smoke != nil {
		normalized.ReleaseIssue = 0
	}

	releaseInput := &releasesteps.Input{
		Versions:                         normalized.Versions,
		Security:                         normalized.Security,
		RunnerGitHubUser:                 normalized.Runner,
		ReleaseIssue:                     normalized.ReleaseIssue,
		ReleaseConfigVariableGroup:       normalized.VariableGroup,
		MicrosoftGoImagesReleasePipeline: goImagesReleasePipelineID,
		GoImagesReleaseSmokeTest:         s.smoke != nil,
	}
	if s.smoke != nil && normalized.VariableGroup != s.smoke.VariableGroup {
		writeError(response, http.StatusBadRequest, "variable group is not allowlisted for the smoke test")
		return
	}
	steps, releaseState, err := releasesteps.CreateGoImagesReleasePipelineGraphWithCheckpoint(
		releaseInput,
		nil,
		nil,
		disabledServices{},
		s.checkpointReleaseState,
	)
	if err != nil {
		writeError(response, http.StatusBadRequest, fmt.Sprintf("create release plan: %v", err))
		return
	}

	document, err := session.NewDocument(releaseInput, releaseState, steps, time.Now())
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create durable release session: %v", err))
		return
	}

	s.mu.Lock()
	if s.demoRunning || s.externalRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot replace the plan while a workflow is running")
		return
	}
	if s.sessionStore != nil {
		if err := s.sessionStore.Save(request.Context(), document); err != nil {
			s.mu.Unlock()
			writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist release session: %v", err))
			return
		}
	}
	s.steps = steps
	s.input = normalized
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
	releaseIssue := 0
	if s.document != nil {
		releaseIssue = s.document.State.Day.ReleaseIssue
	} else if s.releaseState != nil {
		releaseIssue = s.releaseState.Day.ReleaseIssue
	}
	parameters, _ := releasesteps.GoImagesReleasePipelineParameters(s.releaseInput, releaseIssue)
	if s.document != nil && len(s.document.State.Day.GoImagesReleaseParameters) != 0 {
		parameters = clonePipelineParameters(s.document.State.Day.GoImagesReleaseParameters)
	}
	result := planResponse{
		Input: s.input,
		Steps: describeSteps(s.steps),
		Pipeline: pipelinePreview{
			DefinitionID: goImagesReleasePipelineID,
			Organization: "dnceng",
			Project:      "internal",
			Name:         "microsoft-go-infra-release-go-images",
			Parameters:   parameters,
		},
		DemoOnly:  true,
		Restored:  restored,
		SmokeTest: s.releaseInput != nil && s.releaseInput.GoImagesReleaseSmokeTest,
	}
	if s.document != nil {
		result.SessionID = s.document.ID
		result.PlanDigest = s.document.ExecutionDigest
	}
	if s.document != nil {
		result.Run.BuildID = s.document.State.Day.GoImagesReleaseBuildID
		result.Run.Complete = s.document.State.Day.GoImagesReleaseComplete
		result.Run.Imported = s.document.State.Day.GoImagesReleaseImported
	} else if s.releaseState != nil {
		result.Run.BuildID = s.releaseState.Day.GoImagesReleaseBuildID
		result.Run.Complete = s.releaseState.Day.GoImagesReleaseComplete
	}
	if result.Run.BuildID != "" {
		result.Run.URL = "https://dev.azure.com/dnceng/internal/_build/results?buildId=" + result.Run.BuildID
	}
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
	input := document.Input
	state := document.State
	steps, restoredState, err := releasesteps.CreateGoImagesReleasePipelineGraphWithCheckpoint(
		&input,
		nil,
		&state,
		disabledServices{},
		s.checkpointReleaseState,
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
	s.input = PlanInput{
		Versions:      append([]string(nil), input.Versions...),
		Security:      input.Security,
		Runner:        input.RunnerGitHubUser,
		VariableGroup: input.ReleaseConfigVariableGroup,
		ReleaseIssue:  restoredState.Day.ReleaseIssue,
	}
	s.releaseInput = &input
	s.releaseState = restoredState
	s.document = document
	s.runner = &coordinator.StepRunner{}
	s.restoredFromDisk = true
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

type smokeStartRequest struct {
	PlanDigest   string `json:"planDigest"`
	Confirmation string `json:"confirmation"`
}

func (s *Server) handleSmokeStart(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var start smokeStartRequest
	if err := decodeJSON(response, request, &start); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	if s.smoke == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-images smoke execution is not enabled")
		return
	}
	if s.document == nil || s.releaseInput == nil || s.releaseState == nil {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "create and persist a smoke-test plan first")
		return
	}
	if start.PlanDigest != s.document.ExecutionDigest {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "confirmation does not match the current plan digest")
		return
	}
	if start.Confirmation != smokeConfirmationPhrase {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "confirmation phrase is incorrect")
		return
	}
	if !s.releaseInput.GoImagesReleaseSmokeTest || s.releaseInput.ReleaseConfigVariableGroup != s.smoke.VariableGroup {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "persisted plan is not an allowlisted smoke test")
		return
	}
	if s.demoRunning || s.externalRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a workflow is already running")
		return
	}
	if s.releaseState.Day.GoImagesReleaseComplete {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "the one-time smoke test is already complete")
		return
	}
	preflight := s.smoke.Preflight
	newService := s.smoke.NewService
	sessionID := s.document.ID
	document := s.document
	input := *s.releaseInput
	state := s.releaseState
	s.externalRunning = true
	s.mu.Unlock()

	if _, err := preflight(request.Context()); err != nil {
		s.finishExternalRun()
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("Azure preflight failed: %v", err))
		return
	}
	service, err := newService(GoImagesServiceRequest{
		SessionID:       sessionID,
		Versions:        append([]string(nil), input.Versions...),
		ExecutionDigest: document.ExecutionDigest,
	})
	if err != nil {
		s.finishExternalRun()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create Azure pipeline service: %v", err))
		return
	}
	steps, state, err := releasesteps.CreateGoImagesReleasePipelineGraphWithCheckpoint(
		&input,
		nil,
		state,
		service,
		s.checkpointReleaseState,
	)
	if err != nil {
		s.finishExternalRun()
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create smoke-test graph: %v", err))
		return
	}
	plan, err := session.NewPlan(steps)
	if err != nil || document.MatchesPlan(plan) != nil {
		s.finishExternalRun()
		writeError(response, http.StatusConflict, "smoke-test graph no longer matches the confirmed plan")
		return
	}

	s.mu.Lock()
	s.steps = steps
	s.releaseState = state
	s.externalRunning = true
	runner := s.runner
	s.mu.Unlock()

	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.finishExternalRun()
	}()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "go-images smoke test started"})
}

func (s *Server) finishExternalRun() {
	s.mu.Lock()
	s.externalRunning = false
	s.mu.Unlock()
}

type runSearchRequest struct {
	Versions []string `json:"versions"`
}

func (s *Server) handleRunSearch(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var search runSearchRequest
	if err := decodeJSON(response, request, &search); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	versions, err := normalizeVersions(search.Versions)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	if s.smoke == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-images run discovery is not enabled")
		return
	}
	preflight := s.smoke.Preflight
	findRuns := s.smoke.FindRuns
	s.mu.Unlock()
	if _, err := preflight(request.Context()); err != nil {
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("Azure preflight failed: %v", err))
		return
	}
	candidates, err := findRuns(request.Context(), versions)
	if err != nil {
		writeError(response, http.StatusBadGateway, fmt.Sprintf("find go-images runs: %v", err))
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"versions":   versions,
		"candidates": candidates,
	})
}

type runImportRequest struct {
	BuildID       int      `json:"buildId"`
	Versions      []string `json:"versions"`
	Runner        string   `json:"runner"`
	Security      bool     `json:"security"`
	VariableGroup string   `json:"variableGroup"`
	ReleaseIssue  int      `json:"releaseIssue,omitempty"`
}

func (s *Server) handleRunImport(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var importRequest runImportRequest
	if err := decodeJSON(response, request, &importRequest); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if importRequest.BuildID <= 0 {
		writeError(response, http.StatusBadRequest, "build ID must be positive")
		return
	}
	normalized, err := normalizeInput(PlanInput{
		Versions:      importRequest.Versions,
		Runner:        importRequest.Runner,
		Security:      importRequest.Security,
		VariableGroup: importRequest.VariableGroup,
		ReleaseIssue:  importRequest.ReleaseIssue,
	})
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	normalized.ReleaseIssue = 0

	s.mu.Lock()
	if s.smoke == nil {
		s.mu.Unlock()
		writeError(response, http.StatusForbidden, "go-images run import is not enabled")
		return
	}
	if normalized.VariableGroup != s.smoke.VariableGroup {
		s.mu.Unlock()
		writeError(response, http.StatusBadRequest, "variable group is not allowlisted for run import")
		return
	}
	if s.demoRunning || s.externalRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "cannot import while a workflow is running")
		return
	}
	preflight := s.smoke.Preflight
	validateRun := s.smoke.ValidateRun
	s.mu.Unlock()
	if _, err := preflight(request.Context()); err != nil {
		writeError(response, http.StatusPreconditionFailed, fmt.Sprintf("Azure preflight failed: %v", err))
		return
	}
	candidate, err := validateRun(request.Context(), importRequest.BuildID, normalized.Versions)
	if err != nil {
		writeError(response, http.StatusConflict, fmt.Sprintf("validate selected run: %v", err))
		return
	}

	releaseInput := &releasesteps.Input{
		Versions:                         normalized.Versions,
		Security:                         normalized.Security,
		RunnerGitHubUser:                 normalized.Runner,
		ReleaseConfigVariableGroup:       normalized.VariableGroup,
		MicrosoftGoImagesReleasePipeline: goImagesReleasePipelineID,
		GoImagesReleaseSmokeTest:         true,
	}
	steps, releaseState, err := releasesteps.CreateGoImagesReleasePipelineGraphWithCheckpoint(
		releaseInput,
		nil,
		nil,
		disabledServices{},
		s.checkpointReleaseState,
	)
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create imported run plan: %v", err))
		return
	}
	releaseState.Day.GoImagesReleaseBuildID = strconv.Itoa(candidate.BuildID)
	releaseState.Day.GoImagesReleaseComplete = candidate.State == "succeeded"
	releaseState.Day.GoImagesReleaseImported = true
	releaseState.Day.GoImagesReleaseParameters = clonePipelineParameters(candidate.Parameters)
	document, err := session.NewDocument(releaseInput, releaseState, steps, time.Now())
	if err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("create imported run session: %v", err))
		return
	}
	if candidate.CreatedByUI && candidate.ExecutionDigest != "" && candidate.ExecutionDigest != document.ExecutionDigest {
		writeError(response, http.StatusConflict, "selected UI run does not match the reconstructed execution digest")
		return
	}
	if err := s.sessionStore.Save(request.Context(), document); err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Sprintf("persist imported run: %v", err))
		return
	}

	s.mu.Lock()
	s.steps = steps
	s.input = normalized
	s.releaseInput = releaseInput
	s.releaseState = releaseState
	s.document = document
	s.runner = &coordinator.StepRunner{}
	s.restoredFromDisk = false
	result := s.planResponseLocked(false)
	s.mu.Unlock()
	writeJSON(response, http.StatusOK, result)
}

func normalizeInput(input PlanInput) (PlanInput, error) {
	if len(input.Versions) == 0 {
		return PlanInput{}, errors.New("at least one release version is required")
	}
	if len(input.Versions) > 8 {
		return PlanInput{}, errors.New("at most eight release versions can be planned at once")
	}

	normalized := PlanInput{
		Security:      input.Security,
		Runner:        strings.TrimSpace(input.Runner),
		VariableGroup: strings.TrimSpace(input.VariableGroup),
		ReleaseIssue:  input.ReleaseIssue,
	}
	if normalized.Runner == "" {
		normalized.Runner = "ghost"
	}
	if !runnerPattern.MatchString(normalized.Runner) {
		return PlanInput{}, fmt.Errorf("GitHub runner name %q is invalid", normalized.Runner)
	}
	if normalized.VariableGroup == "" {
		return PlanInput{}, errors.New("release configuration variable group is required")
	}
	if normalized.ReleaseIssue < 0 {
		return PlanInput{}, errors.New("release issue cannot be negative")
	}

	normalizedVersions, err := normalizeVersions(input.Versions)
	if err != nil {
		return PlanInput{}, err
	}
	normalized.Versions = normalizedVersions
	return normalized, nil
}

func normalizeVersions(versions []string) ([]string, error) {
	if len(versions) == 0 {
		return nil, errors.New("at least one release version is required")
	}
	if len(versions) > 8 {
		return nil, errors.New("at most eight release versions can be planned at once")
	}
	seen := make(map[string]struct{}, len(versions))
	var normalized []string
	for _, rawVersion := range versions {
		rawVersion = strings.TrimSpace(rawVersion)
		if !versionPattern.MatchString(rawVersion) {
			return nil, fmt.Errorf("release version %q is invalid; include the Microsoft revision, for example 1.26.1-1", rawVersion)
		}
		version := goversion.New(rawVersion).Full()
		if _, ok := seen[version]; ok {
			return nil, fmt.Errorf("release version %q is duplicated", version)
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
		description := planStep{
			ID:        step.ID,
			Name:      step.Name,
			DependsOn: make([]string, len(step.DependsOn)),
		}
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

func clonePipelineParameters(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for name, value := range values {
		clone[name] = value
	}
	return clone
}

func (s *Server) handleDemoStart(response http.ResponseWriter, request *http.Request) {
	if !sameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}

	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "create a release plan before starting the demo")
		return
	}
	if s.demoRunning {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "a demo run is already active")
		return
	}
	s.demoRunning = true
	runner := s.runner
	steps := cloneForDemo(s.steps, s.demoDelay)
	s.mu.Unlock()

	go func() {
		_ = runner.Execute(s.ctx, steps)
		s.mu.Lock()
		s.demoRunning = false
		s.mu.Unlock()
	}()
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "demo started"})
}

func cloneForDemo(steps []*coordinator.Step, delay time.Duration) []*coordinator.Step {
	clones := make(map[*coordinator.Step]*coordinator.Step, len(steps))
	result := make([]*coordinator.Step, 0, len(steps))
	for _, step := range steps {
		clone := &coordinator.Step{
			ID:      step.ID,
			Name:    step.Name,
			Timeout: step.Timeout,
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
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeError(response, http.StatusInternalServerError, "streaming is unsupported")
		return
	}

	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		writeError(response, http.StatusConflict, "create a release plan before subscribing")
		return
	}
	runner := s.runner
	s.mu.Unlock()

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
				Name:     sessionCookieName,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
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
	if err := json.NewEncoder(response).Encode(value); err != nil {
		return
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}
