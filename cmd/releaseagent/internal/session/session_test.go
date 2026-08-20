// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

func TestDocumentPlanFingerprint(t *testing.T) {
	document := testDocument(t)
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}

	steps := testSteps()
	plan, err := NewPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.MatchesPlan(plan); err != nil {
		t.Fatalf("unchanged graph did not match: %v", err)
	}
	steps[0].Timeout = time.Second
	plan, err = NewPlan(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.MatchesPlan(plan); err == nil {
		t.Fatal("changed graph unexpectedly matched persisted plan")
	}

	document = testDocument(t)
	document.Plan.WorkflowRevision++
	if err := document.Validate(); err == nil {
		t.Fatal("unsupported workflow revision unexpectedly passed validation")
	}

	document = testDocument(t)
	document.Plan.Steps[0].Name = "tampered"
	if err := document.Validate(); err == nil {
		t.Fatal("tampered plan unexpectedly passed validation")
	}

	document = testDocument(t)
	document.Plan.Steps[1].Name = document.Plan.Steps[0].Name
	digest, err := planDigest(document.Plan.Steps)
	if err != nil {
		t.Fatal(err)
	}
	document.Plan.Digest = digest
	if err := document.Validate(); err == nil {
		t.Fatal("duplicate persisted step name unexpectedly passed validation")
	}
}

func TestFileStoreRoundTripAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "session.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load missing session error = %v, want ErrNotFound", err)
	}

	document := testDocument(t)
	if err := store.Save(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != document.ID || loaded.Plan.Digest != document.Plan.Digest {
		t.Fatalf("loaded document differs: got %#v, want %#v", loaded, document)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o600 {
			t.Fatalf("session permissions = %o, want 600", permissions)
		}
	}

	document.UpdatedAt = document.UpdatedAt.Add(time.Minute)
	if err := store.Save(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.UpdatedAt.Equal(document.UpdatedAt) {
		t.Fatalf("updated time = %v, want %v", loaded.UpdatedAt, document.UpdatedAt)
	}
}

func TestMigratableWorkflowDocumentLoadsAndUpgrades(t *testing.T) {
	document := testDocument(t)
	document.Plan.WorkflowRevision = MigratableWorkflowRevision
	var err error
	document.Plan.Digest, err = legacyPlanDigest(document.Plan.Steps)
	if err != nil {
		t.Fatal(err)
	}
	document.ExecutionDigest, err = executionDigest(document.Input, document.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(); err == nil {
		t.Fatal("migratable document passed current validation")
	}
	if err := document.ValidateLoadable(); err != nil {
		t.Fatalf("migratable document did not pass load validation: %v", err)
	}

	path := filepath.Join(t.TempDir(), "session.json")
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := document.UpdatedAt.Add(time.Minute)
	upgraded, err := loaded.UpgradeWorkflow(&loaded.Input, &loaded.State, testSteps(), updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.ID != document.ID || upgraded.Plan.WorkflowRevision != CurrentWorkflowRevision ||
		!upgraded.UpdatedAt.Equal(updatedAt) {

		t.Fatalf("upgraded document = %#v", upgraded)
	}
	if err := store.Save(context.Background(), upgraded); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentWithStateDoesNotMutateOriginal(t *testing.T) {
	document := testDocument(t)
	state := document.State
	state.Day.ReleaseIssue = 42
	updatedAt := document.UpdatedAt.Add(time.Minute)
	updated, err := document.WithState(&state, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if document.State.Day.ReleaseIssue != 0 {
		t.Fatalf("original release issue = %d, want 0", document.State.Day.ReleaseIssue)
	}
	if updated.State.Day.ReleaseIssue != 42 {
		t.Fatalf("updated release issue = %d, want 42", updated.State.Day.ReleaseIssue)
	}
	if !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated time = %v, want %v", updated.UpdatedAt, updatedAt)
	}
	if updated.ExecutionDigest != document.ExecutionDigest {
		t.Fatalf("state update changed execution digest: %q != %q", updated.ExecutionDigest, document.ExecutionDigest)
	}
}

func TestDocumentExecutionDigestDetectsInputChange(t *testing.T) {
	document := testDocument(t)
	document.Input.RunnerGitHubUser = "someone-else"
	if err := document.Validate(); err == nil {
		t.Fatal("modified immutable input unexpectedly passed validation")
	}
}

func TestFileStoreRejectsUnknownAndInvalidDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("unknown session field unexpectedly loaded")
	}

	document := testDocument(t)
	document.SchemaVersion++
	if err := store.Save(context.Background(), document); err == nil {
		t.Fatal("unsupported session schema unexpectedly saved")
	}
}

func TestFileStoreLease(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcquireLease()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lease error = %v, want ErrLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release returned error: %v", err)
	}
	second, err := store.AcquireLease()
	if err != nil {
		t.Fatalf("lease after release failed: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func testDocument(t *testing.T) *Document {
	t.Helper()
	input := &releasesteps.Input{
		Versions:               []string{"1.26.1-1"},
		RunnerGitHubUser:       "ghost",
		TargetRepo:             "microsoft/go",
		TargetAzDORepo:         "dnceng/internal/_git/microsoft-go",
		TargetGoImagesRepo:     "microsoft/go-images",
		TargetAzDOGoImagesRepo: "dnceng/internal/_git/microsoft-go-images",
	}
	_, state, err := releasesteps.CreateStepGraph(input, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	document, err := NewDocument(input, state, testSteps(), time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func testSteps() []*coordinator.Step {
	root := coordinator.NewRootStep("Root", coordinator.NoTimeout, func(context.Context) error { return nil })
	leaf := coordinator.NewStep("Leaf", time.Minute, func(context.Context) error { return nil }, root)
	return []*coordinator.Step{root, leaf}
}
