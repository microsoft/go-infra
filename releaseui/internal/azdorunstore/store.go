// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package azdorunstore persists release UI runs in Azure DevOps work items.
package azdorunstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	azdotoken "github.com/microsoft/go-infra/azdo/token"
	azdoworkitem "github.com/microsoft/go-infra/azdo/workitem"
	"github.com/microsoft/go-infra/releaseui/contract"
	"github.com/microsoft/go-infra/releaseui/internal/runstore"
)

const (
	azureBaseURL    = "https://dev.azure.com/devdiv"
	azureProject    = "DEVDIV"
	azureItemType   = "Issue"
	azureTokenCache = 5 * time.Minute
)

type workItemClient interface {
	Create(context.Context, string, string, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
	Get(context.Context, int) (*azdoworkitem.WorkItem, error)
	Update(context.Context, *azdoworkitem.WorkItem, *azdoworkitem.Snapshot) (*azdoworkitem.WorkItem, error)
	Query(context.Context, bool, int) ([]*azdoworkitem.WorkItem, error)
}

type store struct {
	client     workItemClient
	assignedTo string
}

// New creates the built-in release store.
func New(ctx context.Context) (runstore.Store, error) {
	tokens := &azdotoken.CachingTokenProvider{
		Provider: azdotoken.AzureCLITokenProvider{Runner: azdotoken.ExecCommandRunner{}},
		TTL:      azureTokenCache,
	}
	client, err := azdoworkitem.NewClient(azdoworkitem.Config{
		BaseURL: azureBaseURL, Project: azureProject, WorkItemType: azureItemType,
	}, tokens)
	if err != nil {
		return nil, err
	}
	assignedTo, err := client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	return newStore(client, assignedTo)
}

func newStore(client workItemClient, assignedTo string) (*store, error) {
	if client == nil {
		return nil, errors.New("release work item client is nil")
	}
	assignedTo = strings.TrimSpace(assignedTo)
	if assignedTo == "" {
		return nil, errors.New("release work item assignee is empty")
	}
	return &store{client: client, assignedTo: assignedTo}, nil
}

func (s *store) Create(
	ctx context.Context,
	run *runstore.State,
	view *contract.RunView,
	plan *contract.Plan,
) (*runstore.Record, error) {
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

func (s *store) Get(ctx context.Context, id int) (*runstore.Record, error) {
	item, err := s.client.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return releaseRunRecord(item)
}

func (s *store) Update(
	ctx context.Context,
	current *runstore.Record,
	run *runstore.State,
	view *contract.RunView,
	plan *contract.Plan,
) (*runstore.Record, error) {
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
	item, err := s.client.Update(ctx, &azdoworkitem.WorkItem{
		ID: current.ID, Revision: current.Revision, State: state,
	}, snapshot)
	if errors.Is(err, azdoworkitem.ErrRevisionConflict) {
		return nil, fmt.Errorf("%w: %v", runstore.ErrConflict, err)
	}
	if err != nil {
		return nil, err
	}
	return releaseRunRecord(item)
}

func (s *store) Query(ctx context.Context, closed bool, limit int) ([]*runstore.Record, error) {
	items, err := s.client.Query(ctx, closed, limit)
	if err != nil {
		return nil, err
	}
	records := make([]*runstore.Record, 0, len(items))
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
	run *runstore.State,
	view *contract.RunView,
	plan *contract.Plan,
) (*azdoworkitem.Snapshot, error) {
	status, err := run.Status()
	if err != nil {
		return nil, fmt.Errorf("refuse to persist invalid process run: %w", err)
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("marshal process run: %w", err)
	}
	return &azdoworkitem.Snapshot{
		SchemaVersion: azdoworkitem.CurrentSchemaVersion,
		ProcessID:     run.ProcessID,
		Status:        azdoworkitem.Status(status),
		Test:          view != nil && view.Test,
		IntentDigest:  run.Digest,
		Payload:       payload,
		Description:   releaseRunDescription(run, view, plan),
	}, nil
}

func releaseRunDescription(
	run *runstore.State,
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

func releaseRunRecord(item *azdoworkitem.WorkItem) (*runstore.Record, error) {
	if item == nil || item.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(item.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var run runstore.State
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
	if item.Snapshot.Status != azdoworkitem.Status(status) {
		return nil, errors.New("release work item status does not match process run")
	}
	run.UpdatedAt = item.ChangedAt
	return &runstore.Record{
		ID: item.ID, Revision: item.Revision, URL: item.URL,
		Closed: item.State == "Closed", UpdatedAt: item.ChangedAt, Run: run.Clone(),
	}, nil
}

var _ runstore.Store = (*store)(nil)
