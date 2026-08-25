// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/atomicfile"
)

const (
	processRunSchemaVersion = 1
	maxProcessRunSize       = 64 << 10
)

var ErrProcessRunNotFound = errors.New("process run not found")

// ProcessRunStore persists the server's single current reviewed process intent without credentials.
type ProcessRunStore interface {
	Load(context.Context) (*ProcessRun, error)
	Save(context.Context, *ProcessRun) error
}

// ProcessRun is the durable, process-neutral state of one reviewed external action.
type ProcessRun struct {
	ProcessID  string               `json:"processId"`
	Input      json.RawMessage      `json:"input"`
	Payload    json.RawMessage      `json:"payload"`
	Digest     string               `json:"digest"`
	Step       ProcessRunStep       `json:"step"`
	View       ProcessPlanView      `json:"view"`
	Target     ProcessRunReference  `json:"target"`
	External   *ProcessRunReference `json:"external,omitempty"`
	Checkpoint json.RawMessage      `json:"checkpoint,omitempty"`
	Started    bool                 `json:"started"`
	Complete   bool                 `json:"complete"`
	Result     string               `json:"result,omitempty"`
	UpdatedAt  time.Time            `json:"-"`
}

// ProcessRunStep is the single external action represented by a process run.
type ProcessRunStep struct {
	Name    string        `json:"name"`
	Timeout time.Duration `json:"timeout"`
}

// ProcessRunReference is a link and terminal-state summary for an external action.
type ProcessRunReference struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	LinkLabel string `json:"linkLabel"`
	Status    string `json:"status,omitempty"`
	Terminal  bool   `json:"terminal,omitempty"`
	Succeeded bool   `json:"succeeded,omitempty"`
}

// ProcessPreparedRun is the immutable plan returned by a process-specific executor.
type ProcessPreparedRun struct {
	Input   json.RawMessage
	Payload json.RawMessage
	Step    ProcessRunStep
	View    ProcessPlanView
	Target  ProcessRunReference
}

type processRunDocument struct {
	SchemaVersion int        `json:"schemaVersion"`
	Run           ProcessRun `json:"run"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// ProcessRunFileStore atomically persists one process run.
type ProcessRunFileStore struct {
	path string
	mu   sync.Mutex
}

// NewProcessRunFileStore creates a file-backed process run store.
func NewProcessRunFileStore(path string) (*ProcessRunFileStore, error) {
	if path == "" {
		return nil, errors.New("process run file path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("make process run path absolute: %w", err)
	}
	return &ProcessRunFileStore{path: filepath.Clean(absolute)}, nil
}

func (s *ProcessRunFileStore) Load(ctx context.Context) (*ProcessRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var document processRunDocument
	if err := atomicfile.ReadJSON(s.path, maxProcessRunSize, &document); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrProcessRunNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read process run: %w", err)
	}
	if document.SchemaVersion != processRunSchemaVersion {
		return nil, fmt.Errorf("unsupported process run schema %d", document.SchemaVersion)
	}
	if err := validateProcessRun(&document.Run); err != nil {
		return nil, fmt.Errorf("validate process run: %w", err)
	}
	result := cloneProcessRun(&document.Run)
	result.UpdatedAt = document.UpdatedAt
	return result, nil
}

func (s *ProcessRunFileStore) Save(ctx context.Context, run *ProcessRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProcessRun(run); err != nil {
		return fmt.Errorf("refuse to save invalid process run: %w", err)
	}
	now := time.Now().UTC()
	run.UpdatedAt = now
	document := processRunDocument{
		SchemaVersion: processRunSchemaVersion,
		Run:           *cloneProcessRun(run),
		UpdatedAt:     now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicfile.WriteJSON(s.path, ".process-run-*.tmp", maxProcessRunSize, document); err != nil {
		return fmt.Errorf("write process run: %w", err)
	}
	return nil
}

func newProcessRun(processID string, prepared ProcessPreparedRun) (*ProcessRun, error) {
	run := &ProcessRun{
		ProcessID: processID,
		Input:     append(json.RawMessage(nil), prepared.Input...),
		Payload:   append(json.RawMessage(nil), prepared.Payload...),
		Step:      prepared.Step,
		View:      prepared.View,
		Target:    prepared.Target,
		UpdatedAt: time.Now().UTC(),
	}
	digest, err := processRunDigest(run)
	if err != nil {
		return nil, err
	}
	run.Digest = digest
	if err := validateProcessRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

func processRunDigest(run *ProcessRun) (string, error) {
	payload := struct {
		ProcessID string
		Input     json.RawMessage
		Payload   json.RawMessage
		Step      ProcessRunStep
		View      ProcessPlanView
		Target    ProcessRunReference
	}{
		ProcessID: run.ProcessID,
		Input:     run.Input,
		Payload:   run.Payload,
		Step:      run.Step,
		View:      run.View,
		Target:    run.Target,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), nil
}

func validateProcessRun(run *ProcessRun) error {
	if run == nil {
		return errors.New("process run is nil")
	}
	if !processIDPattern.MatchString(run.ProcessID) {
		return fmt.Errorf("process run has invalid process ID %q", run.ProcessID)
	}
	if !json.Valid(run.Input) || !json.Valid(run.Payload) {
		return errors.New("process run input or payload is invalid JSON")
	}
	if strings.TrimSpace(run.Step.Name) == "" || run.Step.Timeout <= 0 {
		return errors.New("process run step is invalid")
	}
	if strings.TrimSpace(run.View.IntentTitle) == "" || strings.TrimSpace(run.View.ExecutionConfirmation) == "" ||
		strings.TrimSpace(run.View.ExecutionButtonLabel) == "" {

		return errors.New("process run view is incomplete")
	}
	if err := validateProcessRunReference(run.Target); err != nil {
		return fmt.Errorf("validate process run target: %w", err)
	}
	if (run.External == nil) != (len(run.Checkpoint) == 0) {
		return errors.New("process run checkpoint and external run must be recorded together")
	}
	if run.External != nil {
		if !run.Started {
			return errors.New("process run has an external run before starting")
		}
		if err := validateProcessRunReference(*run.External); err != nil {
			return fmt.Errorf("validate external process run: %w", err)
		}
	}
	if len(run.Checkpoint) > 0 && (!run.Started || !json.Valid(run.Checkpoint)) {
		return errors.New("process run checkpoint is invalid")
	}
	digest, err := processRunDigest(run)
	if err != nil || !secureEqual(digest, run.Digest) {
		return errors.New("process run digest does not match its content")
	}
	if run.Complete && !run.Started {
		return errors.New("process run completed before it started")
	}
	if !run.Complete && run.Result != "" {
		return errors.New("incomplete process run has a result")
	}
	if run.Complete && run.Result != "succeeded" && run.Result != "failed" && run.Result != "uncertain" {
		return fmt.Errorf("completed process run has invalid result %q", run.Result)
	}
	if run.Complete && run.External != nil && run.Result != "uncertain" {
		if !run.External.Terminal {
			return errors.New("completed process run has an incomplete external run")
		}
		if run.Result == "succeeded" && !run.External.Succeeded {
			return errors.New("successful process run has an unsuccessful external run")
		}
		if run.Result == "failed" && run.External.Succeeded {
			return errors.New("failed process run has a successful external run")
		}
	}
	return nil
}

func validateProcessRunReference(reference ProcessRunReference) error {
	if strings.TrimSpace(reference.ID) == "" || !strings.HasPrefix(reference.URL, "https://") ||
		strings.TrimSpace(reference.LinkLabel) == "" {

		return errors.New("process run reference is incomplete")
	}
	if reference.Succeeded && !reference.Terminal {
		return errors.New("process run reference succeeded before reaching a terminal state")
	}
	return nil
}

func cloneProcessRun(run *ProcessRun) *ProcessRun {
	if run == nil {
		return nil
	}
	clone := *run
	clone.Input = append(json.RawMessage(nil), run.Input...)
	clone.Payload = append(json.RawMessage(nil), run.Payload...)
	clone.Checkpoint = append(json.RawMessage(nil), run.Checkpoint...)
	if run.External != nil {
		external := *run.External
		clone.External = &external
	}
	return &clone
}
