package releaseui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

// GoImagesSessionStore persists confirmed go-images releases as explicitly identified work items.
type GoImagesSessionStore interface {
	Create(context.Context, *goimagessession.Document) (*GoImagesSessionRecord, error)
	Get(context.Context, int) (*GoImagesSessionRecord, error)
	Update(context.Context, *GoImagesSessionRecord, *goimagessession.Document) (*GoImagesSessionRecord, error)
}

// GoImagesSessionRecord binds one session document to its Azure DevOps work item revision.
type GoImagesSessionRecord struct {
	WorkItemID int
	Revision   int
	URL        string
	Document   *goimagessession.Document
	workItem   *azdoworkitem.WorkItem
}

type goImagesWorkItemStore struct {
	client     releaseWorkItemClient
	assignedTo string
}

// NewGoImagesWorkItemStore creates a go-images session store backed by Azure DevOps.
func NewGoImagesWorkItemStore(client releaseWorkItemClient, assignedTo string) (GoImagesSessionStore, error) {
	if client == nil {
		return nil, errors.New("release work item client is nil")
	}
	if strings.TrimSpace(assignedTo) == "" {
		return nil, errors.New("release work item assignee is empty")
	}
	return &goImagesWorkItemStore{client: client, assignedTo: assignedTo}, nil
}

func (s *goImagesWorkItemStore) Create(
	ctx context.Context,
	document *goimagessession.Document,
) (*GoImagesSessionRecord, error) {
	snapshot, err := goImagesSnapshot(document)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("[releaseagent] Go images %s release: %s", document.Input.Mode, strings.Join(document.Input.Versions, ", "))
	workItem, err := s.client.Create(ctx, title, s.assignedTo, snapshot)
	if err != nil {
		return nil, err
	}
	return goImagesSessionRecord(workItem)
}

func (s *goImagesWorkItemStore) Get(ctx context.Context, workItemID int) (*GoImagesSessionRecord, error) {
	workItem, err := s.client.Get(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	return goImagesSessionRecord(workItem)
}

func (s *goImagesWorkItemStore) Update(
	ctx context.Context,
	current *GoImagesSessionRecord,
	document *goimagessession.Document,
) (*GoImagesSessionRecord, error) {
	if current == nil || current.workItem == nil || current.WorkItemID != current.workItem.ID ||
		current.Revision != current.workItem.Revision {

		return nil, errors.New("current go-images session record is invalid")
	}
	snapshot, err := goImagesSnapshot(document)
	if err != nil {
		return nil, err
	}
	workItem, err := s.client.Update(ctx, current.workItem, snapshot)
	if err != nil {
		return nil, err
	}
	return goImagesSessionRecord(workItem)
}

func goImagesSnapshot(document *goimagessession.Document) (*azdoworkitem.Snapshot, error) {
	if err := document.Validate(); err != nil {
		return nil, fmt.Errorf("refuse to persist invalid go-images session: %w", err)
	}
	status, err := goImagesStatus(document)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal go-images session: %w", err)
	}
	return &azdoworkitem.Snapshot{
		SchemaVersion: azdoworkitem.CurrentSchemaVersion,
		ProcessID:     goImagesProcessID,
		Status:        status,
		Test:          document.Input.Mode == goimagesworkflow.ModeTest,
		IntentDigest:  document.ExecutionDigest,
		Payload:       payload,
	}, nil
}

func goImagesSessionRecord(workItem *azdoworkitem.WorkItem) (*GoImagesSessionRecord, error) {
	if workItem == nil || workItem.Snapshot == nil {
		return nil, errors.New("release work item is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(workItem.Snapshot.Payload))
	decoder.DisallowUnknownFields()
	var document goimagessession.Document
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode go-images session: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode go-images session: trailing JSON content")
	}
	if err := document.Validate(); err != nil {
		return nil, fmt.Errorf("validate go-images session: %w", err)
	}
	if workItem.Snapshot.ProcessID != goImagesProcessID || workItem.Snapshot.IntentDigest != document.ExecutionDigest {
		return nil, errors.New("release work item identity does not match go-images session")
	}
	if workItem.Snapshot.Test != (document.Input.Mode == goimagesworkflow.ModeTest) {
		return nil, errors.New("release work item test classification does not match go-images session")
	}
	status, err := goImagesStatus(&document)
	if err != nil {
		return nil, err
	}
	if workItem.Snapshot.Status != status {
		return nil, errors.New("release work item status does not match go-images session")
	}
	return &GoImagesSessionRecord{
		WorkItemID: workItem.ID,
		Revision:   workItem.Revision,
		URL:        workItem.URL,
		Document:   &document,
		workItem:   workItem,
	}, nil
}

func goImagesStatus(document *goimagessession.Document) (azdoworkitem.Status, error) {
	state := document.State
	if !state.Complete {
		if state.BuildID != "" {
			return azdoworkitem.StatusRunning, nil
		}
		return azdoworkitem.StatusStarting, nil
	}
	switch state.Result {
	case "succeeded":
		return azdoworkitem.StatusSucceeded, nil
	case "failed":
		return azdoworkitem.StatusFailed, nil
	case "canceled":
		return azdoworkitem.StatusCanceled, nil
	case "uncertain":
		return azdoworkitem.StatusUncertain, nil
	default:
		return "", fmt.Errorf("go-images session has invalid terminal result %q", state.Result)
	}
}
