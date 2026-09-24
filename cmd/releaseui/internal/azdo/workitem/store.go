// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azdoworkitem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/microsoft/go-infra/releaseui"
	"github.com/microsoft/go-infra/releaseui/contract"
)

// Store adapts Azure DevOps work items to releaseui's storage contract.
type Store struct {
	client     *Client
	assignedTo string
}

// NewStore creates a release store backed by Azure DevOps work items.
func NewStore(client *Client, assignedTo string) (*Store, error) {
	if client == nil {
		return nil, errors.New("release work item client is nil")
	}
	if strings.TrimSpace(assignedTo) == "" {
		return nil, errors.New("release work item assignee is empty")
	}
	return &Store{client: client, assignedTo: assignedTo}, nil
}

func (s *Store) Create(
	ctx context.Context,
	run *releaseui.ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	snapshot, err := releaseRunSnapshot(run, view, plan)
	if err != nil {
		return nil, err
	}
	title := "[releaseagent] " + run.ProcessID
	if plan != nil && strings.TrimSpace(plan.Subtitle) != "" {
		title = "[releaseagent] " + plan.Subtitle
	}
	item, err := s.client.Create(ctx, title, s.assignedTo, snapshot)
	if err != nil {
		return nil, err
	}
	return releaseRunRecord(item)
}

func (s *Store) Get(ctx context.Context, id int) (*releaseui.ReleaseRunRecord, error) {
	item, err := s.client.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return releaseRunRecord(item)
}

func (s *Store) Update(
	ctx context.Context,
	current *releaseui.ReleaseRunRecord,
	run *releaseui.ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*releaseui.ReleaseRunRecord, error) {
	if current == nil || current.ID <= 0 || current.Revision <= 0 {
		return nil, errors.New("current release run record is invalid")
	}
	snapshot, err := releaseRunSnapshot(run, view, plan)
	if err != nil {
		return nil, err
	}
	state := "Active"
	if current.Closed {
		state = "Closed"
	}
	item, err := s.client.Update(ctx, &WorkItem{
		ID: current.ID, Revision: current.Revision, State: state,
	}, snapshot)
	if errors.Is(err, ErrRevisionConflict) {
		return nil, fmt.Errorf("%w: %v", releaseui.ErrReleaseRunConflict, err)
	}
	if err != nil {
		return nil, err
	}
	return releaseRunRecord(item)
}

func (s *Store) Query(ctx context.Context, closed bool, limit int) ([]*releaseui.ReleaseRunRecord, error) {
	items, err := s.client.Query(ctx, closed, limit)
	if err != nil {
		return nil, err
	}
	records := make([]*releaseui.ReleaseRunRecord, 0, len(items))
	for _, item := range items {
		record, err := releaseRunRecord(item)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func releaseRunSnapshot(
	run *releaseui.ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) (*Snapshot, error) {
	status, err := run.Status()
	if err != nil {
		return nil, fmt.Errorf("refuse to persist invalid process run: %w", err)
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("marshal process run: %w", err)
	}
	return &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		ProcessID:     run.ProcessID,
		Status:        Status(status),
		Test:          view != nil && view.Test,
		IntentDigest:  run.Digest,
		Payload:       payload,
		Description:   releaseRunDescription(run, view, plan),
	}, nil
}

func releaseRunDescription(
	run *releaseui.ReleaseRunState,
	view *contract.RunView,
	plan *contract.Plan,
) *DescriptionSummary {
	fields := make([]DescriptionField, 0)
	if view != nil {
		if strings.TrimSpace(view.Summary) != "" {
			fields = append(fields, DescriptionField{Label: "Status", Value: view.Summary})
		}
		if strings.TrimSpace(view.Detail) != "" {
			fields = append(fields, DescriptionField{Label: "Detail", Value: view.Detail})
		}
	}
	if plan != nil {
		for _, fact := range plan.Facts {
			fields = append(fields, DescriptionField{Label: fact.Label, Value: fact.Value})
		}
	}
	return &DescriptionSummary{ProcessName: run.ProcessID, Fields: fields}
}

func releaseRunRecord(item *WorkItem) (*releaseui.ReleaseRunRecord, error) {
	if item == nil || item.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(item.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var run releaseui.ReleaseRunState
	if err := decoder.Decode(&run); err != nil {
		return nil, fmt.Errorf("decode process run: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode process run: trailing JSON content")
	}
	status, err := run.Status()
	if err != nil {
		return nil, fmt.Errorf("validate process run: %w", err)
	}
	if item.Snapshot.ProcessID != run.ProcessID || item.Snapshot.IntentDigest != run.Digest {
		return nil, errors.New("release work item identity does not match process run")
	}
	if item.Snapshot.Status != Status(status) {
		return nil, errors.New("release work item status does not match process run")
	}
	run.UpdatedAt = item.ChangedAt
	return &releaseui.ReleaseRunRecord{
		ID: item.ID, Revision: item.Revision, URL: item.URL,
		Closed: item.State == "Closed", UpdatedAt: item.ChangedAt, Run: run.Clone(),
	}, nil
}

var _ releaseui.ReleaseRunStore = (*Store)(nil)
