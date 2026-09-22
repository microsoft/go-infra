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
	Create(context.Context, *ReleaseRunState, *contract.RunView, *contract.Plan) (*ReleaseRunRecord, error)
	Update(context.Context, *ReleaseRunRecord, *ReleaseRunState, *contract.RunView, *contract.Plan) (*ReleaseRunRecord, error)
}

// ReleaseRunRecord binds one release run to its Azure DevOps work item revision.
type ReleaseRunRecord struct {
	WorkItemID int
	Revision   int
	URL        string
	Run        *ReleaseRunState
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

func (s *processRunWorkItemStore) Create(
	ctx context.Context,
	run *ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*ReleaseRunRecord, error) {
	snapshot, err := processRunSnapshot(run, view, plan)
	if err != nil {
		return nil, err
	}
	title := "[releaseagent] " + run.ProcessID
	if plan != nil && strings.TrimSpace(plan.Subtitle) != "" {
		title = "[releaseagent] " + plan.Subtitle
	}
	workItem, err := s.client.Create(ctx, title, s.assignedTo, snapshot)
	if err != nil {
		return nil, err
	}
	return processRunRecord(workItem)
}

func (s *processRunWorkItemStore) Update(
	ctx context.Context,
	current *ReleaseRunRecord,
	run *ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*ReleaseRunRecord, error) {
	if current == nil || current.workItem == nil || current.WorkItemID != current.workItem.ID ||
		current.Revision != current.workItem.Revision {

		return nil, errors.New("current process run record is invalid")
	}
	snapshot, err := processRunSnapshot(run, view, plan)
	if err != nil {
		return nil, err
	}
	workItem, err := s.client.Update(ctx, current.workItem, snapshot)
	if err != nil {
		return nil, err
	}
	return processRunRecord(workItem)
}

func processRunSnapshot(
	run *ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*azdoworkitem.Snapshot, error) {
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
	test := view != nil && view.Test
	return &azdoworkitem.Snapshot{
		SchemaVersion: azdoworkitem.CurrentSchemaVersion,
		ProcessID:     run.ProcessID, Status: status, Test: test,
		IntentDigest: run.Digest, Payload: payload,
		Description: processRunDescription(run, view, plan),
	}, nil
}

func processRunDescription(
	run *ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) *azdoworkitem.DescriptionSummary {
	fields := make([]azdoworkitem.DescriptionField, 0)
	if view != nil {
		if strings.TrimSpace(view.Summary) != "" {
			fields = append(fields, azdoworkitem.DescriptionField{Label: "Status", Value: view.Summary})
		}
		if strings.TrimSpace(view.Detail) != "" {
			fields = append(fields, azdoworkitem.DescriptionField{Label: "Detail", Value: view.Detail})
		}
	}
	if plan != nil {
		for _, fact := range plan.Facts {
			fields = append(fields, azdoworkitem.DescriptionField{Label: fact.Label, Value: fact.Value})
		}
	}
	return &azdoworkitem.DescriptionSummary{ProcessName: run.ProcessID, Fields: fields}
}

func processRunRecord(workItem *azdoworkitem.WorkItem) (*ReleaseRunRecord, error) {
	if workItem == nil || workItem.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(workItem.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var run ReleaseRunState
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
	status, err := processRunStatus(&run)
	if err != nil {
		return nil, err
	}
	if workItem.Snapshot.Status != status {
		return nil, errors.New("release work item status does not match process run")
	}
	run.UpdatedAt = workItem.ChangedAt
	return &ReleaseRunRecord{
		WorkItemID: workItem.ID, Revision: workItem.Revision, URL: workItem.URL,
		Run: run.Clone(), workItem: workItem,
	}, nil
}

func processRunStatus(run *ReleaseRunState) (azdoworkitem.Status, error) {
	if !run.Started {
		return "", errors.New("process run has not started")
	}
	if !run.Complete {
		return azdoworkitem.StatusRunning, nil
	}
	switch run.Result {
	case resultSucceeded:
		return azdoworkitem.StatusSucceeded, nil
	case resultFailed:
		return azdoworkitem.StatusFailed, nil
	case resultCanceled:
		return azdoworkitem.StatusCanceled, nil
	case resultUncertain:
		return azdoworkitem.StatusUncertain, nil
	default:
		return "", fmt.Errorf("process run has invalid terminal result %q", run.Result)
	}
}
