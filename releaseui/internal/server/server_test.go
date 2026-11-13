// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
)

func TestListenAndServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	result := make(chan error, 1)
	go func() {
		result <- ListenAndServe(ctx, "127.0.0.1:0", func(launchURL string) {
			ready <- launchURL
		}, WithProcesses(newFakeProcessGroup(exampleProcess())), WithReleaseRunStore(newMemoryProcessRunStore()))
	}()

	var launchURL string
	select {
	case launchURL = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for release UI listener")
	}
	parsed, err := url.Parse(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "http" || parsed.Query().Get("token") == "" {
		t.Fatalf("launch URL = %q", launchURL)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("launch status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for release UI shutdown")
	}
}

func TestListenAndServeRejectsNonLoopbackAddress(t *testing.T) {
	err := ListenAndServe(
		context.Background(), "0.0.0.0:0", nil,
		WithProcesses(newFakeProcessGroup(exampleProcess())),
	)
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("error = %v, want non-loopback rejection", err)
	}
}

func TestListenAndServeWaitsForFinalCheckpoint(t *testing.T) {
	for _, failure := range []error{nil, errors.New("final save failed")} {
		name := "saved"
		if failure != nil {
			name = "save failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			save := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(save) }) }
			t.Cleanup(func() {
				cancel()
				unblock()
			})
			store := &shutdownTestStore{
				memoryProcessRunStore: newMemoryProcessRunStore(),
				finalStarted:          make(chan struct{}), finalRelease: save, finalErr: failure,
			}
			state := testProcessRun(t)
			state.Started, state.Checkpointed = true, true
			record := testReleaseRunRecord(1, state, false, time.Now().UTC())
			seedReleaseRecord(store.memoryProcessRunStore, record)
			executing := make(chan struct{})
			process := exampleProcess()
			process.build = func(*fakeReleaseRun) ([]*coordinator.Step, error) {
				return exampleProcessSteps(func(ctx context.Context, _ coordinator.StepControl) error {
					close(executing)
					<-ctx.Done()
					return ctx.Err()
				}), nil
			}
			ready, result := make(chan string, 1), make(chan error, 1)
			go func() {
				result <- ListenAndServe(ctx, "127.0.0.1:0", func(launchURL string) { ready <- launchURL },
					WithProcesses(newFakeProcessGroup(process)), WithReleaseRunStore(store))
			}()
			var launchURL string
			select {
			case launchURL = <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("listener did not start")
			}
			parsed, err := url.Parse(launchURL)
			if err != nil {
				t.Fatal(err)
			}
			baseURL := parsed.Scheme + "://" + parsed.Host
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
			response, err := client.Get(launchURL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			body, err := json.Marshal(webview.ReleaseStartRequest{
				RecordID: record.ID, Revision: record.Revision, PlanDigest: state.Digest, Confirmed: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(http.MethodPost, baseURL+"/api/processes/example/resume", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", baseURL)
			response, err = client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("resume status = %d", response.StatusCode)
			}
			select {
			case <-executing:
			case <-time.After(5 * time.Second):
				t.Fatal("saved run did not start")
			}
			cancel()
			select {
			case <-store.finalStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not attempt final persistence")
			}
			select {
			case err := <-result:
				t.Fatalf("listener exited before final persistence: %v", err)
			default:
			}
			unblock()
			select {
			case err := <-result:
				if !errors.Is(err, failure) {
					t.Fatalf("shutdown error = %v, want %v", err, failure)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			if failure == nil && store.latest(t).Executing {
				t.Fatal("successful shutdown retained its execution reservation")
			}
		})
	}
}
