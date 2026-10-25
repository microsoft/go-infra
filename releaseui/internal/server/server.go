// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package server implements the local browser UI for planning and observing releases.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/azdorunstore"
	"github.com/microsoft/go-infra/releaseui/internal/localserver"
	"github.com/microsoft/go-infra/releaseui/internal/processregistry"
	"github.com/microsoft/go-infra/releaseui/internal/processrun"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

//go:embed web/*
var webFiles embed.FS

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 5 * time.Second

	maxReleaseRecordImportSize = 1 << 20
)

type processRequest struct {
	runner   *coordinator.StepRunner
	prepared bool
}

// Option customizes a Server.
type Option func(*Server)

// WithProcesses sets the release process groups hosted by this server.
func WithProcesses(processes ...contract.ProcessGroup) Option {
	return func(server *Server) {
		server.configuredProcesses = append([]contract.ProcessGroup(nil), processes...)
	}
}

// WithReleaseRunStore replaces the built-in Azure-backed release store.
// Most callers should omit this option.
func WithReleaseRunStore(store runstore.Store) Option {
	return func(server *Server) {
		server.processRunStore = store
	}
}

// Server hosts local release processes and their independent sessions.
type Server struct {
	ctx                 context.Context
	localSession        *localserver.Session
	configuredProcesses []contract.ProcessGroup
	processes           *processregistry.Registry
	processRunStore     runstore.Store
	runs                *processrun.Manager
	history             *processrun.History
}

// New creates a local release UI server with built-in release persistence.
func New(ctx context.Context, options ...Option) (*Server, error) {
	if ctx == nil {
		return nil, errors.New("server context is nil")
	}
	localSession, err := localserver.NewSession()
	if err != nil {
		return nil, err
	}
	server := &Server{
		ctx: ctx, localSession: localSession,
	}
	for _, option := range options {
		option(server)
	}
	server.processes, err = processregistry.New(server.configuredProcesses...)
	if err != nil {
		return nil, err
	}
	if server.processRunStore == nil {
		server.processRunStore, err = azdorunstore.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("create release store: %w", err)
		}
	}
	server.runs = processrun.New(ctx, server.processes, server.processRunStore)
	server.history = server.runs.History()
	return server, nil
}

// LaunchURL adds the one-time session token to baseURL. Visiting this URL establishes an HTTP-only
// session cookie and immediately redirects to a clean URL without the token.
func (s *Server) LaunchURL(baseURL string) (string, error) {
	return s.localSession.LaunchURL(baseURL)
}

// Handler returns the HTTP handler for the local UI.
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(fmt.Sprintf("create embedded web filesystem: %v", err))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	for _, registered := range s.processes.Ordered() {
		definition := registered.Definition
		processID := definition.ID
		mux.HandleFunc("GET "+processregistry.Path(processID), s.handlePage)
		prefix := "/api/processes/" + processID
		mux.HandleFunc("GET "+prefix+"/preflight", func(response http.ResponseWriter, request *http.Request) {
			s.handleProcessRunPreflight(processID, response, request)
		})
		mux.HandleFunc("GET "+prefix+"/plan", s.getProcessPlan(processID))
		mux.HandleFunc("POST "+prefix+"/plan", s.prepareProcess(processID))
		mux.HandleFunc("POST "+prefix+"/start", s.requireActiveProcess(processID, func(response http.ResponseWriter, request *http.Request, _ processRequest) {
			s.handleStartProcessRun(processID, response, request, false)
		}))
		mux.HandleFunc("POST "+prefix+"/resume", s.requireActiveProcess(processID, func(response http.ResponseWriter, request *http.Request, _ processRequest) {
			s.handleStartProcessRun(processID, response, request, true)
		}))
		mux.HandleFunc("GET "+prefix+"/state", s.requireActiveProcess(processID, s.handleState))
		mux.HandleFunc("GET "+prefix+"/events", s.requireActiveProcess(processID, s.handleEvents))
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("POST /api/releases/{id}/select", s.handleSelectRelease)
	mux.HandleFunc("GET /api/releases/{id}/export", s.handleExportRelease)
	mux.HandleFunc("POST /api/releases/{id}/import", s.handleImportRelease)
	mux.HandleFunc("GET /api/processes/{id}", s.handleProcess)
	return s.localSession.Protect(mux)
}

func (s *Server) prepareProcess(processID string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.handlePrepareProcessRun(processID, response, request)
	}
}

func (s *Server) getProcessPlan(processID string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		s.handleGetProcessRun(processID, response)
	}
}

func (s *Server) requireActiveProcess(
	processID string,
	handler func(http.ResponseWriter, *http.Request, processRequest),
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		bound, err := s.captureProcessRequest(processID)
		if err != nil {
			writeError(response, http.StatusConflict, err.Error())
			return
		}
		handler(response, request, bound)
	}
}

func (s *Server) captureProcessRequest(processID string) (processRequest, error) {
	runner, prepared, ok := s.runs.Active(processID)
	if !ok {
		return processRequest{}, errors.New("release process is not configured")
	}
	return processRequest{runner: runner, prepared: prepared}, nil
}

func (s *Server) handlePage(response http.ResponseWriter, request *http.Request) {
	name := "index.html"
	if request.URL.Path != "/" {
		var ok bool
		name, ok = s.processes.Page(request.URL.Path)
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

func (s *Server) handleProcess(response http.ResponseWriter, request *http.Request) {
	registered, ok := s.processes.Lookup(request.PathValue("id"))
	if !ok {
		http.NotFound(response, request)
		return
	}
	group := registered.Group
	definition := group.Processes[0].Definition
	variants := make([]webview.WorkflowProcess, 0, len(group.Processes))
	for _, process := range group.Processes {
		processDefinition := process.Definition
		variant := webview.WorkflowProcess{
			ID: processDefinition.ID, Name: processDefinition.Name,
			Description: processDefinition.Description,
			Preamble:    processDefinition.InputPreamble, SubmitLabel: processDefinition.InputSubmitLabel,
			Inputs: process.Inputs,
		}
		if processDefinition.Notice != nil {
			variant.NoticeTitle = processDefinition.Notice.Title
			variant.Notice = processDefinition.Notice.Message
		}
		variants = append(variants, variant)
	}
	detail := webview.ProcessDetail{
		ID: registered.Definition.ID, Name: group.Identity.Name, Mark: group.Identity.Mark,
		Description: group.Identity.Description, DocumentationURL: group.Identity.DocumentationURL,
		Workflow: webview.WorkflowDetail{
			Heading: definition.InputPreamble, SubmitLabel: definition.InputSubmitLabel,
			Variants: variants,
		},
	}
	writeJSON(response, http.StatusOK, detail)
}

func (s *Server) handleProcessRunPreflight(
	processID string,
	response http.ResponseWriter,
	request *http.Request,
) {
	report, ok := s.runs.Preflight(request.Context(), processID)
	if !ok {
		http.NotFound(response, request)
		return
	}
	writeJSON(response, http.StatusOK, report)
}

func (s *Server) handleGetProcessRun(processID string, response http.ResponseWriter) {
	result, found := s.runs.Current(processID)
	if !found {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handlePrepareProcessRun(
	processID string,
	response http.ResponseWriter,
	request *http.Request,
) {
	if !localserver.SameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var encoded json.RawMessage
	if err := decodeJSON(response, request, &encoded); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.runs.Prepare(request.Context(), processID, encoded)
	if err != nil {
		writeProcessRunError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleStartProcessRun(
	processID string,
	response http.ResponseWriter,
	request *http.Request,
	resume bool,
) {
	if !localserver.SameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	var start webview.ReleaseStartRequest
	if err := decodeJSON(response, request, &start); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	execute := s.runs.Start
	if resume {
		execute = s.runs.Resume
	}
	if err := execute(request.Context(), processID, start); err != nil {
		writeProcessRunError(response, request, err)
		return
	}
	writeJSON(response, http.StatusAccepted, map[string]string{"status": "external action started"})
}

func (s *Server) handleDashboard(response http.ResponseWriter, request *http.Request) {
	writeJSON(response, http.StatusOK, s.history.Dashboard(request.Context()))
}

func (s *Server) handleSelectRelease(response http.ResponseWriter, request *http.Request) {
	if !localserver.SameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	href, err := s.history.Select(request.Context(), id)
	if err != nil {
		writeProcessRunError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"href": href})
}

func (s *Server) handleExportRelease(response http.ResponseWriter, request *http.Request) {
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	record, err := s.history.Export(request.Context(), id)
	if err != nil {
		writeProcessRunError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, record)
}

func (s *Server) handleImportRelease(response http.ResponseWriter, request *http.Request) {
	if !localserver.SameOrigin(request) {
		writeError(response, http.StatusForbidden, "request origin does not match the release UI")
		return
	}
	id, ok := releaseRecordID(response, request)
	if !ok {
		return
	}
	var imported webview.ReleaseRecordExport[*runstore.State]
	if err := decodeJSONLimit(response, request, &imported, maxReleaseRecordImportSize); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.history.Import(request.Context(), id, imported)
	if err != nil {
		writeProcessRunError(response, request, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (s *Server) handleState(response http.ResponseWriter, _ *http.Request, bound processRequest) {
	writeJSON(response, http.StatusOK, bound.runner.Snapshot())
}

func (s *Server) handleEvents(response http.ResponseWriter, request *http.Request, bound processRequest) {
	if !bound.prepared {
		writeError(response, http.StatusConflict, "prepare a release before subscribing")
		return
	}
	streamRunnerEvents(response, request, bound.runner)
}

// ListenAndServe runs the release UI on a loopback address until ctx is canceled or the HTTP
// server stops. The ready callback receives the one-time launch URL after the listener starts.
func ListenAndServe(ctx context.Context, address string, ready func(string), options ...Option) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", address, err)
	}
	defer listener.Close()

	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.IsLoopback() {
		return fmt.Errorf("refusing to serve release UI on non-loopback address %q", listener.Addr())
	}
	ui, err := New(ctx, options...)
	if err != nil {
		return err
	}
	launchURL, err := ui.LaunchURL("http://" + listener.Addr().String())
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           ui.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- httpServer.Serve(listener)
	}()
	if ready != nil {
		ready(launchURL)
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-serverResult:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("serve release UI: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		return fmt.Errorf("shut down release UI: %w", err)
	}
	return serveErr
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

func writeProcessRunError(response http.ResponseWriter, request *http.Request, err error) {
	var runErr *processrun.Error
	if !errors.As(err, &runErr) {
		writeError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if runErr.Kind == processrun.ErrorNotFound {
		http.NotFound(response, request)
		return
	}
	status := http.StatusInternalServerError
	switch runErr.Kind {
	case processrun.ErrorInvalid:
		status = http.StatusBadRequest
	case processrun.ErrorConflict:
		status = http.StatusConflict
	case processrun.ErrorPrecondition:
		status = http.StatusPreconditionFailed
	case processrun.ErrorStore:
		status = http.StatusBadGateway
	}
	writeError(response, status, err.Error())
}

func releaseRecordID(response http.ResponseWriter, request *http.Request) (int, bool) {
	id, err := strconv.Atoi(request.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(response, http.StatusBadRequest, "release record ID must be a positive integer")
		return 0, false
	}
	return id, true
}
