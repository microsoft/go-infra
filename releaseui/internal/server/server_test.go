// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
	"github.com/microsoft/go-infra/releaseui/internal/webview"
	"github.com/microsoft/go-infra/releaseui/runstore"
)

func TestProcessSelectionNavigation(t *testing.T) {
	example := exampleProcess()
	processes := exampleProcess()
	processes.definition = testProcessDefinition("processes")
	ui := newTestUIWithProcess(t, example,
		WithProcesses(newFakeProcessGroup(example), newFakeProcessGroup(processes)),
		WithReleaseRunStore(newMemoryProcessRunStore()),
	)
	sharedTopbar := []string{
		`<a class="brand" href="/"`,
		`<a class="topbar-link" href="/processes">New process</a>`,
		`id="server-connection"`,
		`src="/assets/topbar.js"`,
	}
	for _, test := range []struct {
		path    string
		status  int
		present []string
		absent  []string
	}{
		{
			path:   "/",
			status: http.StatusOK,
			present: []string{
				`<title>Release UI</title>`,
				`id="ongoing-releases"`,
			},
			absent: []string{`id="process-grid"`, `id="process-list"`, `class="release-guide-link"`},
		},
		{
			path:   "/processes",
			status: http.StatusOK,
			present: []string{
				`<title>Run a process · Release UI</title>`,
				`id="process-list"`, `src="/assets/processes.js"`,
			},
			absent: []string{`id="ongoing-releases"`},
		},
		{
			path:   "/processes/example",
			status: http.StatusOK,
			present: []string{
				`<title>Release Process · Release UI</title>`,
				`id="plan-form"`,
			},
		},
		{
			path:    "/processes/processes",
			status:  http.StatusOK,
			present: []string{`id="plan-form"`},
		},
		{
			path:    "/releases/42",
			status:  http.StatusOK,
			present: []string{`id="plan-form"`},
		},
		{path: "/example", status: http.StatusNotFound},
		{path: "/processes/unknown", status: http.StatusNotFound},
		{path: "/processes/example/extra", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response, err := ui.client.Get(ui.http.URL + test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if response.Request.URL.Path != test.path {
				t.Fatalf("redirected to %q", response.Request.URL.Path)
			}
			present := test.present
			absent := test.absent
			if test.status == http.StatusOK {
				present = append(present, sharedTopbar...)
				absent = append(absent, `>Instructions <span aria-hidden="true">↗</span></a>`)
			}
			for _, text := range present {
				if !strings.Contains(string(body), text) {
					t.Errorf("page is missing %q", text)
				}
			}
			for _, text := range absent {
				if strings.Contains(string(body), text) {
					t.Errorf("page unexpectedly contains %q", text)
				}
			}
		})
	}

	health, err := ui.client.Get(ui.http.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	closeResponse(t, health)
	if health.StatusCode != http.StatusNoContent {
		t.Fatalf("health status = %d, want %d", health.StatusCode, http.StatusNoContent)
	}

	response, err := ui.client.Get(ui.http.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard webview.DashboardResponse
	decodeResponse(t, response, &dashboard)
	if len(dashboard.Processes) != 2 || dashboard.Processes[0].Href != "/processes/example" ||
		dashboard.Processes[1].Href != "/processes/processes" {

		t.Fatalf("dashboard processes = %#v", dashboard.Processes)
	}
}

func TestBrandRenderedAcrossPages(t *testing.T) {
	const brand = "Example <Go & Co>"
	ui := newTestUIWithProcess(t, exampleProcess(),
		WithBrand(brand),
		WithInstructionsURL("https://example.test/docs?source=release&lang=en"),
		WithReleaseRunStore(newMemoryProcessRunStore()),
	)
	for _, path := range []string{"/", "/processes", "/processes/example", "/releases/42"} {
		response, err := ui.client.Get(ui.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		closeResponse(t, response)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusOK)
		}
		page := string(body)
		for _, expected := range []string{
			`data-brand="Example &lt;Go &amp; Co&gt;"`,
			`<small>Example &lt;Go &amp; Co&gt;</small>`,
			`Example &lt;Go &amp; Co&gt; Release UI`,
		} {
			if !strings.Contains(page, expected) {
				t.Errorf("GET %s page is missing %q", path, expected)
			}
		}
		header, _, ok := strings.Cut(page, "</header>")
		if !ok {
			t.Errorf("GET %s page is missing the top bar", path)
			continue
		}
		const instructionsLink = `<a class="topbar-link" href="https://example.test/docs?source=release&amp;lang=en" target="_blank" rel="noreferrer" aria-label="Instructions (opens in a new tab)">Instructions <span aria-hidden="true">↗</span></a>`
		if !strings.Contains(header, instructionsLink) {
			t.Errorf("GET %s top bar is missing the configured instructions link", path)
		}
		if count := strings.Count(page, `href="https://example.test/docs?source=release&amp;lang=en"`); count != 1 {
			t.Errorf("GET %s has %d instructions links, want 1", path, count)
		}
	}
}

type queryLinkedProcessRunStore struct{ *memoryProcessRunStore }

func (queryLinkedProcessRunStore) QueryURL() string {
	return "https://example.test/query?wiql=SELECT+%5BSystem.Id%5D&lang=en"
}

func TestDashboardHistoryQueryLink(t *testing.T) {
	const link = `href="https://example.test/query?wiql=SELECT&#43;%5BSystem.Id%5D&amp;lang=en"`
	for _, test := range []struct {
		name  string
		store runstore.Store
		want  bool
	}{
		{name: "linked", store: queryLinkedProcessRunStore{newMemoryProcessRunStore()}, want: true},
		{name: "unlinked", store: newMemoryProcessRunStore()},
	} {
		t.Run(test.name, func(t *testing.T) {
			ui := newTestUI(t, WithReleaseRunStore(test.store))
			response, err := ui.client.Get(ui.http.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			closeResponse(t, response)
			if err != nil {
				t.Fatal(err)
			}
			_, history, found := strings.Cut(string(body), `<section id="recent-section"`)
			if !found {
				t.Fatal("recent history section not found")
			}
			history, _, _ = strings.Cut(history, "</section>")
			sectionTag, _, _ := strings.Cut(history, ">")
			if strings.Contains(sectionTag, " hidden") {
				t.Error("recent history section is initially hidden")
			}
			if !strings.Contains(history, "Loading recent history") {
				t.Error("recent history is missing its loading placeholder")
			}
			if got := strings.Contains(history, link); got != test.want {
				t.Errorf("history query link present = %v, want %v\n%s", got, test.want, history)
			}
			if got := strings.Contains(string(body), ">Query <span"); got != test.want {
				t.Errorf("query label present = %v, want %v", got, test.want)
			}
		})
	}
}

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
