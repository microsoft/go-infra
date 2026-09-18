// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
	if err := run.Validate(); err != nil {
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
	if err := run.Validate(); err != nil {
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
		Run:        run.Clone(),
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
	case contract.ResultSucceeded:
		return azdoworkitem.StatusSucceeded, nil
	case contract.ResultFailed:
		return azdoworkitem.StatusFailed, nil
	case contract.ResultCanceled:
		return azdoworkitem.StatusCanceled, nil
	case contract.ResultUncertain:
		return azdoworkitem.StatusUncertain, nil
	default:
		return "", fmt.Errorf("process run has invalid terminal result %q", run.Result)
	}
}
