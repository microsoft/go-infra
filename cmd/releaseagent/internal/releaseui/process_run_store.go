// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
)

// ProcessRunStore persists confirmed process runs as explicitly identified work items.
type ProcessRunStore interface {
	Create(context.Context, *ProcessRun) (*ProcessRunRecord, error)
	Get(context.Context, int) (*ProcessRunRecord, error)
	Update(context.Context, *ProcessRunRecord, *ProcessRun) (*ProcessRunRecord, error)
}

// ProcessRunRecord binds one process run to its Azure DevOps work item revision.
type ProcessRunRecord struct {
	WorkItemID int
	Revision   int
	URL        string
	Run        *ProcessRun
	workItem   *azdoworkitem.WorkItem
}

// ProcessRun is the durable, process-neutral state of one reviewed external action.
type ProcessRun struct {
	ProcessID  string               `json:"processId"`
	Test       bool                 `json:"test,omitempty"`
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
	Test    bool
	Input   json.RawMessage
	Payload json.RawMessage
	Step    ProcessRunStep
	View    ProcessPlanView
	Target  ProcessRunReference
}

type releaseWorkItemClient interface {
	Create(context.Context, string, string, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
	Get(context.Context, int) (*azdoworkitem.WorkItem, error)
	Update(context.Context, *azdoworkitem.WorkItem, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
}

type processRunWorkItemStore struct {
	client     releaseWorkItemClient
	assignedTo string
}

// NewProcessRunWorkItemStore creates a generic process-run store backed by Azure DevOps.
func NewProcessRunWorkItemStore(client releaseWorkItemClient, assignedTo string) (ProcessRunStore, error) {
	if client == nil {
		return nil, errors.New("release work item client is nil")
	}
	if strings.TrimSpace(assignedTo) == "" {
		return nil, errors.New("release work item assignee is empty")
	}
	return &processRunWorkItemStore{client: client, assignedTo: assignedTo}, nil
}

func (s *processRunWorkItemStore) Create(ctx context.Context, run *ProcessRun) (*ProcessRunRecord, error) {
	snapshot, err := processRunSnapshot(run)
	if err != nil {
		return nil, err
	}
	workItem, err := s.client.Create(ctx, "[releaseagent] "+run.View.IntentTitle, s.assignedTo, snapshot)
	if err != nil {
		return nil, err
	}
	return processRunRecord(workItem)
}

func (s *processRunWorkItemStore) Get(ctx context.Context, workItemID int) (*ProcessRunRecord, error) {
	workItem, err := s.client.Get(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	return processRunRecord(workItem)
}

func (s *processRunWorkItemStore) Update(
	ctx context.Context,
	current *ProcessRunRecord,
	run *ProcessRun,
) (*ProcessRunRecord, error) {
	if current == nil || current.workItem == nil || current.WorkItemID != current.workItem.ID ||
		current.Revision != current.workItem.Revision {

		return nil, errors.New("current process run record is invalid")
	}
	snapshot, err := processRunSnapshot(run)
	if err != nil {
		return nil, err
	}
	workItem, err := s.client.Update(ctx, current.workItem, snapshot)
	if err != nil {
		return nil, err
	}
	return processRunRecord(workItem)
}

func processRunSnapshot(run *ProcessRun) (*azdoworkitem.Snapshot, error) {
	if err := validateProcessRun(run); err != nil {
		return nil, fmt.Errorf("refuse to persist invalid process run: %w", err)
	}
	status, err := processRunStatus(run)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("marshal process run: %w", err)
	}
	return &azdoworkitem.Snapshot{
		SchemaVersion: azdoworkitem.CurrentSchemaVersion,
		ProcessID:     run.ProcessID,
		Status:        status,
		Test:          run.Test,
		IntentDigest:  run.Digest,
		Payload:       payload,
	}, nil
}

func processRunRecord(workItem *azdoworkitem.WorkItem) (*ProcessRunRecord, error) {
	if workItem == nil || workItem.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(workItem.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var run ProcessRun
	if err := decoder.Decode(&run); err != nil {
		return nil, fmt.Errorf("decode process run: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode process run: trailing JSON content")
	}
	if err := validateProcessRun(&run); err != nil {
		return nil, fmt.Errorf("validate process run: %w", err)
	}
	if workItem.Snapshot.ProcessID != run.ProcessID || workItem.Snapshot.IntentDigest != run.Digest {
		return nil, errors.New("release work item identity does not match process run")
	}
	if workItem.Snapshot.Test != run.Test {
		return nil, errors.New("release work item test classification does not match process run")
	}
	status, err := processRunStatus(&run)
	if err != nil {
		return nil, err
	}
	if workItem.Snapshot.Status != status {
		return nil, errors.New("release work item status does not match process run")
	}
	return &ProcessRunRecord{
		WorkItemID: workItem.ID,
		Revision:   workItem.Revision,
		URL:        workItem.URL,
		Run:        cloneProcessRun(&run),
		workItem:   workItem,
	}, nil
}

func processRunStatus(run *ProcessRun) (azdoworkitem.Status, error) {
	if !run.Started {
		return "", errors.New("process run has not started")
	}
	if !run.Complete {
		if run.External != nil {
			return azdoworkitem.StatusRunning, nil
		}
		return azdoworkitem.StatusStarting, nil
	}
	switch run.Result {
	case "succeeded":
		return azdoworkitem.StatusSucceeded, nil
	case "failed":
		return azdoworkitem.StatusFailed, nil
	case "uncertain":
		return azdoworkitem.StatusUncertain, nil
	default:
		return "", fmt.Errorf("process run has invalid terminal result %q", run.Result)
	}
}

func newProcessRun(processID string, prepared ProcessPreparedRun) (*ProcessRun, error) {
	run := &ProcessRun{
		ProcessID: processID,
		Test:      prepared.Test,
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
		Test      bool
		Input     json.RawMessage
		Payload   json.RawMessage
		Step      ProcessRunStep
		View      ProcessPlanView
		Target    ProcessRunReference
	}{
		ProcessID: run.ProcessID,
		Test:      run.Test,
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
