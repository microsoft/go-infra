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

	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/contract"
)

// ReleaseRunStore persists confirmed release runs as explicitly identified work items.
type ReleaseRunStore interface {
	Create(context.Context, *contract.State) (*ReleaseRunRecord, error)
	Update(context.Context, *ReleaseRunRecord, *contract.State) (*ReleaseRunRecord, error)
}

// ReleaseRunRecord binds one release run to its Azure DevOps work item revision.
type ReleaseRunRecord struct {
	WorkItemID int
	Revision   int
	URL        string
	Run        *contract.State
	workItem   *azdoworkitem.WorkItem
}

type releaseWorkItemClient interface {
	Create(context.Context, string, string, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
	Update(context.Context, *azdoworkitem.WorkItem, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
}

type processRunWorkItemStore struct {
	client     releaseWorkItemClient
	assignedTo string
}

// NewReleaseRunWorkItemStore creates a release-run store backed by Azure DevOps.
func NewReleaseRunWorkItemStore(client releaseWorkItemClient, assignedTo string) (ReleaseRunStore, error) {
	if client == nil {
		return nil, errors.New("release work item client is nil")
	}
	if strings.TrimSpace(assignedTo) == "" {
		return nil, errors.New("release work item assignee is empty")
	}
	return &processRunWorkItemStore{client: client, assignedTo: assignedTo}, nil
}

func (s *processRunWorkItemStore) Create(ctx context.Context, run *contract.State) (*ReleaseRunRecord, error) {
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

func (s *processRunWorkItemStore) Update(
	ctx context.Context,
	current *ReleaseRunRecord,
	run *contract.State,
) (*ReleaseRunRecord, error) {
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

func processRunSnapshot(run *contract.State) (*azdoworkitem.Snapshot, error) {
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
		Description:   processRunDescription(run),
	}, nil
}

func processRunDescription(run *contract.State) *azdoworkitem.DescriptionSummary {
	words := strings.Split(run.ProcessID, "-")
	for index, word := range words {
		if word != "" {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	fields := []azdoworkitem.DescriptionField{
		{Label: "Intent", Value: run.View.IntentTitle},
		{Label: "Action", Value: run.View.ExecutionTitle},
	}
	for _, fact := range run.View.Facts {
		value := fact.Value
		if fact.Detail != "" {
			value += " · " + fact.Detail
		}
		fields = append(fields, azdoworkitem.DescriptionField{Label: fact.Label, Value: value, URL: fact.Href})
	}
	fields = append(fields, azdoworkitem.DescriptionField{
		Label: "Target", Value: strings.TrimPrefix(run.Target.LinkLabel, "Open "), URL: run.Target.URL,
	})
	if run.External != nil {
		value := strings.TrimPrefix(run.External.LinkLabel, "Open ")
		if run.External.Status != "" {
			value += " · " + run.External.Status
		}
		fields = append(fields, azdoworkitem.DescriptionField{
			Label: "External run", Value: value, URL: run.External.URL,
		})
	}
	return &azdoworkitem.DescriptionSummary{ProcessName: strings.Join(words, " "), Fields: fields}
}

func processRunRecord(workItem *azdoworkitem.WorkItem) (*ReleaseRunRecord, error) {
	if workItem == nil || workItem.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(workItem.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var run contract.State
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
	run.UpdatedAt = workItem.ChangedAt
	return &ReleaseRunRecord{
		WorkItemID: workItem.ID,
		Revision:   workItem.Revision,
		URL:        workItem.URL,
		Run:        CloneReleaseRunState(&run),
		workItem:   workItem,
	}, nil
}

func processRunStatus(run *contract.State) (azdoworkitem.Status, error) {
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
	case "canceled":
		return azdoworkitem.StatusCanceled, nil
	case "uncertain":
		return azdoworkitem.StatusUncertain, nil
	default:
		return "", fmt.Errorf("process run has invalid terminal result %q", run.Result)
	}
}

// NewReleaseRunState creates validated durable state from a prepared release plan.
func NewReleaseRunState(processID string, prepared contract.Plan) (*contract.State, error) {
	if prepared.VariantID == "" {
		return nil, errors.New("release plan variant ID is empty")
	}
	run := &contract.State{
		ProcessID: processID,
		VariantID: prepared.VariantID,
		Test:      prepared.Test,
		Input:     append(json.RawMessage(nil), prepared.Input...),
		Payload:   append(json.RawMessage(nil), prepared.Payload...),
		SessionID: prepared.SessionID,
		View:      cloneProcessPlanView(prepared.View),
		Target:    prepared.Target,
		UpdatedAt: time.Now().UTC(),
	}
	digest, err := processRunDigest(run)
	if err != nil {
		return nil, err
	}
	run.Digest = digest
	if run.SessionID == "" {
		run.SessionID = digest
	}
	if err := validateProcessRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

func processRunDigest(run *contract.State) (string, error) {
	payload := struct {
		ProcessID string
		VariantID string
		Test      bool
		Input     json.RawMessage
		Payload   json.RawMessage
		View      contract.PlanView
		Target    contract.Reference
	}{
		ProcessID: run.ProcessID,
		VariantID: run.VariantID,
		Test:      run.Test,
		Input:     run.Input,
		Payload:   run.Payload,
		View:      run.View,
		Target:    run.Target,
	}
	var data []byte
	var err error
	if len(run.LegacySteps) == 0 {
		data, err = json.Marshal(payload)
	} else {
		data, err = json.Marshal(struct {
			ProcessID string
			Test      bool
			Input     json.RawMessage
			Payload   json.RawMessage
			Steps     []contract.Step
			View      contract.PlanView
			Target    contract.Reference
		}{
			ProcessID: payload.ProcessID,
			Test:      payload.Test,
			Input:     payload.Input,
			Payload:   payload.Payload,
			Steps:     run.LegacySteps,
			View:      payload.View,
			Target:    payload.Target,
		})
	}
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest), nil
}

func validateProcessRun(run *contract.State) error {
	if run == nil {
		return errors.New("process run is nil")
	}
	if !processIDPattern.MatchString(run.ProcessID) {
		return fmt.Errorf("process run has invalid process ID %q", run.ProcessID)
	}
	if run.VariantID != "" && !processIDPattern.MatchString(run.VariantID) {
		return fmt.Errorf("process run has invalid variant ID %q", run.VariantID)
	}
	if !json.Valid(run.Input) || !json.Valid(run.Payload) {
		return errors.New("process run input or payload is invalid JSON")
	}
	if strings.TrimSpace(run.SessionID) == "" {
		return errors.New("process run session ID is empty")
	}
	if len(run.LegacySteps) > 0 {
		if err := validateProcessRunSteps(run.LegacySteps); err != nil {
			return err
		}
	}
	if strings.TrimSpace(run.View.IntentTitle) == "" || strings.TrimSpace(run.View.ExecutionTitle) == "" ||
		strings.TrimSpace(run.View.ExecutionConfirmation) == "" ||
		strings.TrimSpace(run.View.ExecutionButtonLabel) == "" {

		return errors.New("process run view is incomplete")
	}
	if err := validateProcessRunReference(run.Target); err != nil {
		return fmt.Errorf("validate process run target: %w", err)
	}
	if run.External != nil {
		if !run.Started {
			return errors.New("process run has an external run before starting")
		}
		if len(run.Checkpoint) == 0 {
			return errors.New("process run has an external run without a checkpoint")
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
	if run.Complete && run.Result != "succeeded" && run.Result != "failed" && run.Result != "canceled" && run.Result != "uncertain" {
		return fmt.Errorf("completed process run has invalid result %q", run.Result)
	}
	if run.Complete && run.External != nil && run.Result != "uncertain" {
		if !run.External.Terminal {
			return errors.New("completed process run has an incomplete external run")
		}
		if run.Result == "succeeded" && !run.External.Succeeded {
			return errors.New("successful process run has an unsuccessful external run")
		}
		if (run.Result == "failed" || run.Result == "canceled") && run.External.Succeeded {
			return errors.New("failed process run has a successful external run")
		}
	}
	return nil
}

func validateProcessRunReference(reference contract.Reference) error {
	if strings.TrimSpace(reference.ID) == "" || !strings.HasPrefix(reference.URL, "https://") ||
		strings.TrimSpace(reference.LinkLabel) == "" {

		return errors.New("process run reference is incomplete")
	}
	if reference.Succeeded && !reference.Terminal {
		return errors.New("process run reference succeeded before reaching a terminal state")
	}
	return nil
}

// CloneReleaseRunState returns an independent copy of run.
func CloneReleaseRunState(run *contract.State) *contract.State {
	if run == nil {
		return nil
	}
	clone := *run
	clone.Input = append(json.RawMessage(nil), run.Input...)
	clone.Payload = append(json.RawMessage(nil), run.Payload...)
	clone.LegacySteps = cloneProcessRunSteps(run.LegacySteps)
	clone.View = cloneProcessPlanView(run.View)
	clone.Checkpoint = append(json.RawMessage(nil), run.Checkpoint...)
	if run.External != nil {
		external := *run.External
		clone.External = &external
	}
	return &clone
}

func cloneProcessPlanView(view contract.PlanView) contract.PlanView {
	clone := view
	clone.Facts = append([]contract.PlanFact(nil), view.Facts...)
	if view.Request != nil {
		request := *view.Request
		request.Fields = append([]contract.RequestField(nil), view.Request.Fields...)
		clone.Request = &request
	}
	return clone
}

func validateProcessRunSteps(steps []contract.Step) error {
	if len(steps) == 0 {
		return errors.New("process run has no steps")
	}
	names := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if strings.TrimSpace(step.Name) == "" || step.Timeout <= 0 {
			return errors.New("process run step is invalid")
		}
		if _, exists := names[step.Name]; exists {
			return fmt.Errorf("process run repeats step %q", step.Name)
		}
		names[step.Name] = struct{}{}
	}
	for _, step := range steps {
		dependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, exists := names[dependency]; !exists || dependency == step.Name {
				return fmt.Errorf("process run step %q has invalid dependency %q", step.Name, dependency)
			}
			if _, exists := dependencies[dependency]; exists {
				return fmt.Errorf("process run step %q repeats dependency %q", step.Name, dependency)
			}
			dependencies[dependency] = struct{}{}
		}
	}
	return nil
}

func cloneProcessRunSteps(steps []contract.Step) []contract.Step {
	cloned := append([]contract.Step(nil), steps...)
	for index := range cloned {
		cloned[index].DependsOn = append([]string(nil), cloned[index].DependsOn...)
	}
	return cloned
}
