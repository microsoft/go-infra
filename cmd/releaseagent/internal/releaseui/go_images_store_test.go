package releaseui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/azdoworkitem"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagessession"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/goimagesworkflow"
)

type fakeGoImagesWorkItemClient struct {
	item *azdoworkitem.WorkItem
}

func (f *fakeGoImagesWorkItemClient) Create(
	_ context.Context,
	title, assignedTo string,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if title != "[releaseagent] Go images normal release: 1.26.5-2" || assignedTo != "Release Operator" {
		return nil, fmt.Errorf("unexpected title %q or assignee %q", title, assignedTo)
	}
	f.item = &azdoworkitem.WorkItem{
		ID: 42, Revision: 1, URL: "https://example.invalid/workitems/42", Snapshot: snapshot,
	}
	return f.item, nil
}

func (f *fakeGoImagesWorkItemClient) Get(context.Context, int) (*azdoworkitem.WorkItem, error) {
	return f.item, nil
}

func (f *fakeGoImagesWorkItemClient) Update(
	_ context.Context,
	current *azdoworkitem.WorkItem,
	snapshot *azdoworkitem.Snapshot,
) (*azdoworkitem.WorkItem, error) {
	if current != f.item {
		return nil, errors.New("unexpected current work item")
	}
	f.item = &azdoworkitem.WorkItem{
		ID: current.ID, Revision: current.Revision + 1, URL: current.URL, Snapshot: snapshot,
	}
	return f.item, nil
}

func TestGoImagesWorkItemStoreRoundTrip(t *testing.T) {
	client := &fakeGoImagesWorkItemClient{}
	store, err := NewGoImagesWorkItemStore(client, "Release Operator")
	if err != nil {
		t.Fatal(err)
	}
	document := testGoImagesDocument(t)
	record, err := store.Create(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if record.WorkItemID != 42 || record.Revision != 1 || record.Document.ID != document.ID ||
		client.item.Snapshot.Status != azdoworkitem.StatusStarting {

		t.Fatalf("record = %#v, snapshot = %#v", record, client.item.Snapshot)
	}

	state := document.State
	state.QueueAttempted = true
	state.BuildID = "888"
	state.Complete = true
	state.Result = "succeeded"
	document, err = document.WithState(&state, document.UpdatedAt.Add(1))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(context.Background(), record, document)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || !updated.Document.State.Complete ||
		client.item.Snapshot.Status != azdoworkitem.StatusSucceeded {

		t.Fatalf("updated = %#v, snapshot = %#v", updated, client.item.Snapshot)
	}
	description, err := azdoworkitem.RenderDescription(client.item.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"<strong>Process</strong></td><td>Go images",
		"<strong>Status</strong></td><td>Succeeded",
		"<strong>Mode</strong></td><td>Normal",
		"<strong>Versions</strong></td><td>1.26.5-2",
		"<strong>Publication</strong></td><td>public/",
		"microsoft-go-images/commit/" + testSourceCommit,
		`_build/results?buildId=888">888</a>`,
		"<strong>Created</strong>",
		"<strong>Last checkpoint</strong>",
	} {
		if !strings.Contains(description, text) {
			t.Fatalf("description does not contain %q: %s", text, description)
		}
	}
}

func TestGoImagesTestModeClassification(t *testing.T) {
	for _, test := range []struct {
		mode goimagesworkflow.Mode
		want bool
	}{
		{mode: goimagesworkflow.ModeNormal},
		{mode: goimagesworkflow.ModeRollback},
		{mode: goimagesworkflow.ModeTest, want: true},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			document := testGoImagesDocumentMode(t, test.mode)
			snapshot, err := goImagesSnapshot(document)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Test != test.want {
				t.Fatalf("snapshot.Test = %v, want %v", snapshot.Test, test.want)
			}
		})
	}
}

func testGoImagesDocument(t *testing.T) *goimagessession.Document {
	return testGoImagesDocumentMode(t, goimagesworkflow.ModeNormal)
}

func testGoImagesDocumentMode(t *testing.T, mode goimagesworkflow.Mode) *goimagessession.Document {
	t.Helper()
	input := &goimagesworkflow.Input{
		Versions: []string{"1.26.5-2"}, Mode: mode,
		SourceVersion: testSourceCommit,
	}
	if mode == goimagesworkflow.ModeRollback {
		input.SourceBuildID = "3019035"
	}
	steps, state, err := goimagesworkflow.NewGraphWithCheckpoint(input, nil, disabledGoImagesService{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := goimagessession.NewDocument(input, state, steps, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return document
}

type memoryGoImagesSessionStore struct {
	mu        sync.Mutex
	nextID    int
	records   map[int]*GoImagesSessionRecord
	createErr error
}

func newMemoryGoImagesSessionStore() *memoryGoImagesSessionStore {
	return &memoryGoImagesSessionStore{nextID: 1, records: make(map[int]*GoImagesSessionRecord)}
}

func (s *memoryGoImagesSessionStore) Create(
	_ context.Context,
	document *goimagessession.Document,
) (*GoImagesSessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	record := &GoImagesSessionRecord{
		WorkItemID: s.nextID, Revision: 1,
		URL:      fmt.Sprintf("https://example.invalid/workitems/%d", s.nextID),
		Document: cloneGoImagesDocument(document),
	}
	s.records[record.WorkItemID] = record
	s.nextID++
	return cloneGoImagesSessionRecord(record), nil
}

func (s *memoryGoImagesSessionStore) Get(_ context.Context, id int) (*GoImagesSessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return nil, errors.New("go-images session record not found")
	}
	return cloneGoImagesSessionRecord(record), nil
}

func (s *memoryGoImagesSessionStore) Update(
	_ context.Context,
	current *GoImagesSessionRecord,
	document *goimagessession.Document,
) (*GoImagesSessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[current.WorkItemID]
	if !ok || record.Revision != current.Revision {
		return nil, azdoworkitem.ErrRevisionConflict
	}
	record = &GoImagesSessionRecord{
		WorkItemID: record.WorkItemID, Revision: record.Revision + 1,
		URL: record.URL, Document: cloneGoImagesDocument(document),
	}
	s.records[record.WorkItemID] = record
	return cloneGoImagesSessionRecord(record), nil
}

func (s *memoryGoImagesSessionStore) seed(document *goimagessession.Document) int {
	record, err := s.Create(context.Background(), document)
	if err != nil {
		panic(err)
	}
	return record.WorkItemID
}

func (s *memoryGoImagesSessionStore) latest(t *testing.T) *goimagessession.Document {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(s.records))
	}
	for _, record := range s.records {
		return cloneGoImagesDocument(record.Document)
	}
	panic("unreachable")
}

func (s *memoryGoImagesSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func cloneGoImagesSessionRecord(record *GoImagesSessionRecord) *GoImagesSessionRecord {
	clone := *record
	clone.Document = cloneGoImagesDocument(record.Document)
	return &clone
}

func cloneGoImagesDocument(document *goimagessession.Document) *goimagessession.Document {
	data, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	var clone goimagessession.Document
	if err := json.Unmarshal(data, &clone); err != nil {
		panic(err)
	}
	return &clone
}

var _ GoImagesSessionStore = (*memoryGoImagesSessionStore)(nil)
