// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/session"
)

func TestGoInfraActionStoreRoundTrip(t *testing.T) {
	store, err := NewGoInfraActionFileStore(filepath.Join(t.TempDir(), "action.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := testStoredGoInfraPlan(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModeDryRun}, nil)
	if err := store.Save(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != plan.Digest || loaded.Input != plan.Input || loaded.Started || loaded.Complete {
		t.Fatalf("loaded plan = %#v", loaded)
	}
	loaded.Digest = "tampered"
	if err := store.Save(context.Background(), loaded); err == nil {
		t.Fatal("tampered plan was persisted")
	}
}

func TestInterruptedGoInfraActionRestoresUncertain(t *testing.T) {
	store, err := NewGoInfraActionFileStore(filepath.Join(t.TempDir(), "action.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := testStoredGoInfraPlan(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish}, nil)
	plan.Started = true
	if err := store.Save(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	ui := newTestUI(t, WithGoInfraActionStore(store), WithGoInfraGitHubIntegration(github.integration()))
	response, err := ui.client.Get(ui.http.URL + "/api/processes/go-infra/plan")
	if err != nil {
		t.Fatal(err)
	}
	var restored goInfraPlanResponse
	decodeResponse(t, response, &restored)
	if response.StatusCode != http.StatusOK || !restored.Execution.Run.Complete || restored.Execution.Run.Result != "uncertain" {
		t.Fatalf("restored = %#v", restored)
	}
	response = postJSON(t, ui, "/api/processes/go-infra/plan", `{"action":"manual-dispatch","dispatchMode":"dry-run"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("replacement plan status = %d", response.StatusCode)
	}
	if len(github.dispatches) != 0 || github.labelCalls != 0 {
		t.Fatalf("dispatches = %v, label calls = %d", github.dispatches, github.labelCalls)
	}
	persisted, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Complete || persisted.Result != "uncertain" {
		t.Fatalf("persisted = %#v", persisted)
	}
}

func TestGoInfraIntegrationRequiresActionStore(t *testing.T) {
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	if _, err := New(context.Background(), WithGoInfraGitHubIntegration(github.integration())); err == nil {
		t.Fatal("go-infra integration was enabled without an action store")
	}
	store, err := NewGoInfraActionFileStore(filepath.Join(t.TempDir(), "action.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), WithGoInfraActionStore(store)); err == nil {
		t.Fatal("go-infra action store was enabled without the integration")
	}
}

func TestUncertainGoInfraActionDoesNotHideBehindGoImagesSession(t *testing.T) {
	actionStore, err := NewGoInfraActionFileStore(filepath.Join(t.TempDir(), "action.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan := testStoredGoInfraPlan(t, goInfraPlanInput{Action: goInfraActionManualDispatch, DispatchMode: goInfraDispatchModePublish}, nil)
	plan.Started = true
	if err := actionStore.Save(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	sessionStore, err := session.NewFileStore(filepath.Join(t.TempDir(), "go-images-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := GoImagesSource{Branch: goImagesSourceBranch, Commit: testSourceCommit, Versions: []string{"1.26.5-2"}}
	first := newTestUI(t, WithSessionStore(sessionStore), WithGoImagesReadOnlyIntegration(testReadOnly(&source, nil)))
	createTestPlan(t, first, `{"mode":"normal"}`)
	github := &fakeGoInfraGitHub{pullRequest: testGoInfraPullRequest()}
	_, err = New(
		context.Background(), WithSessionStore(sessionStore), WithGoInfraActionStore(actionStore),
		WithGoInfraGitHubIntegration(github.integration()),
	)
	if err == nil {
		t.Fatal("startup accepted simultaneous go-images and uncertain go-infra state")
	}
}

func testStoredGoInfraPlan(t *testing.T, input goInfraPlanInput, pullRequest *GoInfraPullRequest) *goInfraPlan {
	t.Helper()
	digest, err := goInfraPlanDigest(input, pullRequest)
	if err != nil {
		t.Fatal(err)
	}
	return &goInfraPlan{Input: input, PullRequest: pullRequest, Digest: digest}
}

var _ GoInfraActionStore = (*GoInfraActionFileStore)(nil)
