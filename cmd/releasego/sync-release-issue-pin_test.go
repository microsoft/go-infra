// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const noPinnedIssuesResponse = `{"data":{"repository":{"pinnedIssues":{"totalCount":0,"nodes":[]}}}}`

type releaseIssuePinTestRequest struct {
	Query     string
	Variables map[string]string
}

func readReleaseIssuePinRequest(t *testing.T, r *http.Request) releaseIssuePinTestRequest {
	t.Helper()
	var body releaseIssuePinTestRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode issue pin request: %v", err)
	}
	return body
}

func readReleaseIssuePinQuery(t *testing.T, r *http.Request) string {
	t.Helper()
	body := readReleaseIssuePinRequest(t, r)
	want := map[string]string{"issueID": "I_tracker"}
	if strings.Contains(body.Query, "repository(owner:") {
		want = map[string]string{"owner": "o", "repo": "r"}
	}
	if !reflect.DeepEqual(body.Variables, want) {
		t.Errorf("wrong issue pin variables: %v", body.Variables)
	}
	return body.Query
}

func checkReleaseIssuePinWarning(t *testing.T, r *http.Request) {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode issue pin warning: %v", err)
	}
	if !reflect.DeepEqual(body, map[string]string{"body": releaseIssuePinCapacityWarning}) {
		t.Errorf("wrong issue pin warning payload: %v", body)
	}
	if !strings.Contains(body["body"], "<!-- releasego:issue-pin-capacity-warning -->") {
		t.Error("issue pin warning has no stable marker")
	}
}

func TestSyncReleaseIssuePinCommand(t *testing.T) {
	for _, tt := range []struct {
		name        string
		state       string
		pinned      bool
		body        string
		pinCount    int
		mutation    string
		wantPinned  bool
		wantWarning bool
	}{
		{"close", "CLOSED", true, releaseDayIssueBodyPrefix, 3, "unpinIssue", false, false},
		{"reopen", "OPEN", false, releaseDayIssueBodyPrefix, 0, "pinIssue", true, false},
		{"last slot", "OPEN", false, releaseDayIssueBodyPrefix, 2, "pinIssue", true, false},
		{"full slots", "OPEN", false, releaseDayIssueBodyPrefix, 3, "", false, true},
		{"above limit", "OPEN", false, releaseDayIssueBodyPrefix, 4, "", false, true},
		{"already unpinned", "CLOSED", false, releaseDayIssueBodyPrefix, 3, "", false, false},
		{"already pinned", "OPEN", true, releaseDayIssueBodyPrefix, 3, "", true, false},
		{"updated report", "CLOSED", true, releaseDayIssueBodyPrefix + "\n\nUpdated build report", 3, "unpinIssue", false, false},
		{"unrelated issue", "CLOSED", true, "An unrelated release issue", 3, "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var queries, mutations, warnings atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
					// The GraphQL state, not an earlier REST response or event, must determine the pin.
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"number":42,"node_id":"I_tracker","state":"open","body":%q,"labels":[{"name":"Area-Release"}]}`, tt.body))
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					query := readReleaseIssuePinQuery(t, r)
					if strings.Contains(query, "mutation") {
						mutations.Add(1)
						if tt.mutation == "" || !strings.Contains(query, "updateIssuePin: "+tt.mutation+"(") {
							t.Errorf("unexpected issue pin mutation: %s", query)
						}
						releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"updateIssuePin":{"issue":{"id":"I_tracker","isPinned":%t}}}}`, tt.wantPinned))
					} else {
						queries.Add(1)
						if !strings.Contains(query, "node(id: $issueID)") || !strings.Contains(query, "state isPinned") ||
							!strings.Contains(query, "pinnedIssues(first: 1) { totalCount }") {

							t.Errorf("unexpected issue state query: %s", query)
						}
						releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":"I_tracker","state":%q,"isPinned":%t,"repository":{"pinnedIssues":{"totalCount":%d}}}}}`, tt.state, tt.pinned, tt.pinCount))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
					releaseGitHubJSON(t, w, http.StatusOK, `[]`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
					warnings.Add(1)
					checkReleaseIssuePinWarning(t, r)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"id":99,"html_url":"https://github.com/o/r/issues/42#issuecomment-99"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			if err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i=42"); err != nil {
				t.Fatal(err)
			}
			wantQueries, wantMutations := int32(1), int32(0)
			if tt.name == "unrelated issue" {
				wantQueries = 0
			}
			if tt.mutation != "" {
				wantMutations = 1
			}
			if queries.Load() != wantQueries || mutations.Load() != wantMutations {
				t.Fatalf("got %d queries and %d mutations, want %d and %d", queries.Load(), mutations.Load(), wantQueries, wantMutations)
			}
			wantWarnings := int32(0)
			if tt.wantWarning {
				wantWarnings = 1
			}
			if warnings.Load() != wantWarnings {
				t.Fatalf("got %d warning comments, want %d", warnings.Load(), wantWarnings)
			}
		})
	}
}

func TestSyncReleaseIssuePinErrors(t *testing.T) {
	const openUnpinned = `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":2}}}}}`
	for _, tt := range []struct {
		name              string
		queryStatus       int
		queryResponse     string
		mutationStatus    int
		mutationResponse  string
		warningListStatus int
		warningListPage   string
		warningStatus     int
		warningResponse   string
		wantError         string
	}{
		{
			name:          "query forbidden",
			queryStatus:   http.StatusForbidden,
			queryResponse: `{"message":"Resource not accessible by integration"}`,
			wantError:     "Resource not accessible by integration",
		},
		{
			name:          "query GraphQL error",
			queryResponse: `{"data":{"node":null},"errors":[{"message":"Query denied"}]}`,
			wantError:     "Query denied",
		},
		{
			name:          "query missing node",
			queryResponse: `{"data":{"node":null}}`,
			wantError:     "did not return",
		},
		{
			name:          "query wrong node",
			queryResponse: `{"data":{"node":{"id":"I_other","state":"OPEN","isPinned":false}}}`,
			wantError:     "did not return",
		},
		{
			name:          "query missing pin state",
			queryResponse: `{"data":{"node":{"id":"I_tracker","state":"CLOSED"}}}`,
			wantError:     "did not return",
		},
		{
			name:          "query unknown issue state",
			queryResponse: `{"data":{"node":{"id":"I_tracker","state":"UNKNOWN","isPinned":false}}}`,
			wantError:     "unexpected release tracking issue state",
		},
		{
			name:          "query missing pin count",
			queryResponse: `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false}}}`,
			wantError:     "did not return the repository's pinned issue count",
		},
		{
			name:             "mutation GraphQL error",
			queryResponse:    openUnpinned,
			mutationResponse: `{"data":{"updateIssuePin":null},"errors":[{"message":"Pin denied"},{"message":"Pin failed"}]}`,
			wantError:        "Pin denied; Pin failed",
		},
		{
			name:             "mutation HTTP error",
			queryResponse:    openUnpinned,
			mutationStatus:   http.StatusInternalServerError,
			mutationResponse: `{"message":"Pin service unavailable"}`,
			wantError:        "Pin service unavailable",
		},
		{
			name:             "mutation missing result",
			queryResponse:    openUnpinned,
			mutationResponse: `{"data":{"updateIssuePin":null}}`,
			wantError:        "did not confirm",
		},
		{
			name:             "mutation missing pin state",
			queryResponse:    openUnpinned,
			mutationResponse: `{"data":{"updateIssuePin":{"issue":{"id":"I_tracker"}}}}`,
			wantError:        "did not confirm",
		},
		{
			name:             "mutation wrong pin state",
			queryResponse:    openUnpinned,
			mutationResponse: `{"data":{"updateIssuePin":{"issue":{"id":"I_tracker","isPinned":false}}}}`,
			wantError:        "did not confirm",
		},
		{
			name:             "mutation wrong node",
			queryResponse:    openUnpinned,
			mutationResponse: `{"data":{"updateIssuePin":{"issue":{"id":"I_other","isPinned":true}}}}`,
			wantError:        "did not confirm",
		},
		{
			name:              "warning lookup failure",
			queryResponse:     `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":3}}}}}`,
			warningListStatus: http.StatusForbidden,
			wantError:         "find pin-capacity warning:",
		},
		{
			name:              "warning later page lookup failure",
			queryResponse:     `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":3}}}}}`,
			warningListStatus: http.StatusForbidden,
			warningListPage:   "2",
			wantError:         "find pin-capacity warning:",
		},
		{
			name:            "warning comment failure",
			queryResponse:   `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":3}}}}}`,
			warningStatus:   http.StatusForbidden,
			warningResponse: `{"message":"Cannot create issue comments"}`,
			wantError:       "post pin-capacity warning:",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"number":42,"node_id":"I_tracker","body":%q}`, releaseDayIssueBodyPrefix))
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					status, body := tt.queryStatus, tt.queryResponse
					if strings.Contains(readReleaseIssuePinQuery(t, r), "mutation") {
						status, body = tt.mutationStatus, tt.mutationResponse
					}
					if status == 0 {
						status = http.StatusOK
					}
					releaseGitHubJSON(t, w, status, body)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
					if tt.warningListStatus != 0 {
						if r.URL.Query().Get("page") != tt.warningListPage {
							w.Header().Set("Link", `<https://api.github.com/repos/o/r/issues/42/comments?page=2>; rel="next"`)
							releaseGitHubJSON(t, w, http.StatusOK, `[]`)
							return
						}
						releaseGitHubJSON(t, w, tt.warningListStatus, `{"message":"Cannot list issue comments"}`)
						return
					}
					releaseGitHubJSON(t, w, http.StatusOK, `[]`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
					if tt.warningStatus == 0 {
						t.Error("unexpected pin-capacity warning")
						http.NotFound(w, r)
						return
					}
					checkReleaseIssuePinWarning(t, r)
					releaseGitHubJSON(t, w, tt.warningStatus, tt.warningResponse)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i=42")
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestSyncReleaseIssuePinRecheckFailures(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		response  string
		wantError string
	}{
		{"HTTP failure", http.StatusBadGateway, `{"message":"Recheck unavailable"}`, "Recheck unavailable"},
		{"missing capacity", http.StatusOK, `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false}}}`, "did not return the repository's pinned issue count"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var queries atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"number":42,"node_id":"I_tracker","body":%q}`, releaseDayIssueBodyPrefix))
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					if strings.Contains(readReleaseIssuePinQuery(t, r), "mutation") {
						releaseGitHubJSON(t, w, http.StatusOK, `{"errors":[{"message":"Pin failed"}]}`)
					} else if queries.Add(1)%2 == 0 {
						releaseGitHubJSON(t, w, tt.status, tt.response)
					} else {
						releaseGitHubJSON(t, w, http.StatusOK, `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":2}}}}}`)
					}
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i=42")
			if err == nil || !strings.Contains(err.Error(), "Pin failed") || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want pin failure and %q", err, tt.wantError)
			}
		})
	}
}

func TestSyncReleaseIssuePinRetriesAfterAppliedMutation(t *testing.T) {
	var pinned atomic.Bool
	var mutations atomic.Int32
	newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
			releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"number":42,"node_id":"I_tracker","body":%q}`, releaseDayIssueBodyPrefix))
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			query := readReleaseIssuePinQuery(t, r)
			if strings.Contains(query, "mutation") {
				mutations.Add(1)
				pinned.Store(true)
				releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Response lost after pinning"}`)
			} else {
				releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":%t,"repository":{"pinnedIssues":{"totalCount":1}}}}}`, pinned.Load()))
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i=42"); err != nil {
		t.Fatal(err)
	}
	if !pinned.Load() || mutations.Load() != 1 {
		t.Fatalf("retry repeated an applied mutation: pinned=%t mutations=%d", pinned.Load(), mutations.Load())
	}
}

func TestReleaseDayIssuePinCapacityWarning(t *testing.T) {
	for _, tt := range []struct {
		name          string
		race          bool
		queryFailures int32
	}{
		{"full", false, 0},
		{"race", true, 0},
		{"race on final retry", true, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var created, queries, mutations, warnings atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
					created.Add(1)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"number":42,"node_id":"I_tracker","html_url":"https://github.com/o/r/issues/42"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					query := readReleaseIssuePinQuery(t, r)
					if strings.Contains(query, "repository(owner:") {
						releaseGitHubJSON(t, w, http.StatusOK, noPinnedIssuesResponse)
					} else if strings.Contains(query, "mutation") {
						mutations.Add(1)
						releaseGitHubJSON(t, w, http.StatusOK, `{"errors":[{"message":"No available pin slots"}]}`)
					} else {
						if queries.Add(1) <= tt.queryFailures {
							releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Temporary query failure"}`)
							return
						}
						count := 3
						if tt.race && mutations.Load() == 0 {
							count = 2
						}
						releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":%d}}}}}`, count))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
					releaseGitHubJSON(t, w, http.StatusOK, `[]`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
					warnings.Add(1)
					checkReleaseIssuePinWarning(t, r)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"id":99,"html_url":"https://github.com/o/r/issues/42#issuecomment-99"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			output, err := captureCommandOutput(t, func() error {
				return runGitHubCommand(t, handleCreateReleaseDayIssue, "-repo=o/r", "-releases=1.27.0-1", "-set-azdo-variable-name=ReleaseIssue")
			})
			if err != nil {
				t.Fatalf("full pin slots failed the release: %v", err)
			}
			if !strings.Contains(output, "##vso[task.setvariable variable=ReleaseIssue]42") {
				t.Fatalf("release issue variable was not emitted: %s", output)
			}
			if created.Load() != 1 || warnings.Load() != 1 {
				t.Fatalf("got %d created issues and %d warnings, want one of each", created.Load(), warnings.Load())
			}
			wantMutations := int32(0)
			if tt.race {
				wantMutations = 1
			}
			if mutations.Load() != wantMutations {
				t.Fatalf("got %d pin mutations, want %d", mutations.Load(), wantMutations)
			}
			if want := tt.queryFailures + 1 + wantMutations; queries.Load() != want {
				t.Fatalf("got %d pin state queries, want %d", queries.Load(), want)
			}
		})
	}
}

func TestReleaseDayIssuePinWarningIsRetrySafe(t *testing.T) {
	for _, tt := range []struct {
		name         string
		existingPage int
		postFailures int32
		loseResponse bool
		rerun        bool
	}{
		{"lost response", 0, 0, true, false},
		{"lost final response", 0, 4, true, false},
		{"command rerun", 0, 0, false, true},
		{"existing warning", 1, 0, false, false},
		{"warning on later page", 2, 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var created, posted, saved, secondPage atomic.Int32
			if tt.existingPage != 0 {
				saved.Store(1)
			}
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
					created.Add(1)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"number":42,"node_id":"I_tracker","html_url":"https://github.com/o/r/issues/42"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"number":42,"node_id":"I_tracker","body":%q}`, releaseDayIssueBodyPrefix))
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					query := readReleaseIssuePinQuery(t, r)
					if strings.Contains(query, "repository(owner:") {
						releaseGitHubJSON(t, w, http.StatusOK, noPinnedIssuesResponse)
						return
					}
					if strings.Contains(query, "mutation") {
						t.Error("attempted to pin with no available slots")
					}
					releaseGitHubJSON(t, w, http.StatusOK, `{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":false,"repository":{"pinnedIssues":{"totalCount":3}}}}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
					if tt.existingPage == 2 && r.URL.Query().Get("page") != "2" {
						w.Header().Set("Link", `<https://api.github.com/repos/o/r/issues/42/comments?page=2>; rel="next"`)
						releaseGitHubJSON(t, w, http.StatusOK, `[{"id":98,"body":"An unrelated comment"}]`)
						return
					}
					if r.URL.Query().Get("page") == "2" {
						secondPage.Add(1)
					}
					if saved.Load() == 0 {
						releaseGitHubJSON(t, w, http.StatusOK, `[]`)
						return
					}
					body := releaseIssuePinCapacityWarning
					if tt.existingPage != 0 {
						body = "<!-- releasego:issue-pin-capacity-warning -->\n\nAn earlier capacity warning."
					}
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`[{"id":99,"body":%q,"html_url":"https://github.com/o/r/issues/42#issuecomment-99"}]`, body))
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
					checkReleaseIssuePinWarning(t, r)
					n := posted.Add(1)
					if n <= tt.postFailures {
						releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Temporary comment failure"}`)
						return
					}
					saved.Add(1)
					if tt.loseResponse && n == tt.postFailures+1 {
						releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Response lost after comment was saved"}`)
						return
					}
					releaseGitHubJSON(t, w, http.StatusCreated, `{"id":99,"html_url":"https://github.com/o/r/issues/42#issuecomment-99"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			output, err := captureCommandOutput(t, func() error {
				return runGitHubCommand(t, handleCreateReleaseDayIssue, "-repo=o/r", "-releases=1.27.0-1", "-set-azdo-variable-name=ReleaseIssue")
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "##vso[task.setvariable variable=ReleaseIssue]42") {
				t.Fatalf("release issue variable was not emitted: %s", output)
			}
			if tt.rerun {
				if err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i=42"); err != nil {
					t.Fatal(err)
				}
			}
			wantPosts := tt.postFailures + 1
			if tt.existingPage != 0 {
				wantPosts = 0
			}
			if created.Load() != 1 || saved.Load() != 1 || posted.Load() != wantPosts {
				t.Fatalf("created=%d saved=%d posted=%d, want 1, 1, %d", created.Load(), saved.Load(), posted.Load(), wantPosts)
			}
			if tt.existingPage == 2 && secondPage.Load() == 0 {
				t.Error("did not check the second page for an existing warning")
			}
		})
	}
}

func TestReleaseDayIssuePinCleanup(t *testing.T) {
	type pinnedIssue struct {
		state, body               string
		currentState, currentBody string
		alreadyUnpinned           bool
	}
	closed := pinnedIssue{state: "CLOSED", body: releaseDayIssueBodyPrefix}
	open := pinnedIssue{state: "OPEN", body: releaseDayIssueBodyPrefix}
	unrelated := pinnedIssue{state: "CLOSED", body: "Not a release tracker"}
	reopened, edited, unpinned := closed, closed, closed
	reopened.currentState = "OPEN"
	edited.currentBody = unrelated.body
	unpinned.alreadyUnpinned = true

	for _, tt := range []struct {
		name              string
		pins              []pinnedIssue
		wantUnpins        []int
		wantWarnings      int32
		listFailures      int32
		unpinFailures     int32
		loseUnpinResponse bool
	}{
		{name: "no existing pins"},
		{name: "cleanup with free slots", pins: []pinnedIssue{closed}, wantUnpins: []int{0}},
		{name: "mixed pins", pins: []pinnedIssue{closed, open, unrelated}, wantUnpins: []int{0}},
		{name: "multiple closed trackers", pins: []pinnedIssue{closed, closed, open}, wantUnpins: []int{0, 1}},
		{name: "all closed trackers", pins: []pinnedIssue{closed, closed, closed}, wantUnpins: []int{0, 1, 2}},
		{name: "preserve other pins", pins: []pinnedIssue{open, unrelated}},
		{name: "full with open trackers", pins: []pinnedIssue{open, open, open}, wantWarnings: 1},
		{name: "full with unrelated issues", pins: []pinnedIssue{unrelated, unrelated, unrelated}, wantWarnings: 1},
		{name: "tracker reopened", pins: []pinnedIssue{reopened}},
		{name: "tracker body changed", pins: []pinnedIssue{edited}},
		{name: "tracker manually unpinned", pins: []pinnedIssue{unpinned}},
		{name: "list retries", pins: []pinnedIssue{closed}, wantUnpins: []int{0}, listFailures: 4},
		{name: "unpin retries", pins: []pinnedIssue{closed}, wantUnpins: []int{0}, unpinFailures: 1},
		{name: "lost unpin response", pins: []pinnedIssue{closed}, wantUnpins: []int{0}, loseUnpinResponse: true},
		{name: "lost final unpin response", pins: []pinnedIssue{closed}, wantUnpins: []int{0}, unpinFailures: 4, loseUnpinResponse: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinned := make([]atomic.Bool, len(tt.pins))
			wantUnpinned := make([]bool, len(tt.pins))
			issueIndexes := make(map[string]int)
			var nodes []string
			for i, pin := range tt.pins {
				pinned[i].Store(!pin.alreadyUnpinned)
				id := fmt.Sprintf("I_old_%d", i+1)
				issueIndexes[id] = i
				nodes = append(nodes, fmt.Sprintf(`{"id":"PIN_%d","issue":{"id":%q,"number":%d,"state":%q,"body":%q}}`, i+1, id, i+1, pin.state, pin.body))
			}
			for _, i := range tt.wantUnpins {
				wantUnpinned[i] = true
			}
			countPinned := func() int {
				count := 0
				for i := range pinned {
					if pinned[i].Load() {
						count++
					}
				}
				return count
			}
			var created, lists, unpins, pins, warnings atomic.Int32
			var newPinned atomic.Bool
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
					created.Add(1)
					for _, i := range tt.wantUnpins {
						if pinned[i].Load() {
							t.Errorf("created a tracker before cleaning up old pin %d", i)
						}
					}
					releaseGitHubJSON(t, w, http.StatusCreated, `{"number":42,"node_id":"I_tracker","html_url":"https://github.com/o/r/issues/42"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					request := readReleaseIssuePinRequest(t, r)
					if strings.Contains(request.Query, "repository(owner:") {
						if !reflect.DeepEqual(request.Variables, map[string]string{"owner": "o", "repo": "r"}) ||
							!strings.Contains(request.Query, "nodes { issue {") {

							t.Errorf("unexpected pinned issue query: %+v", request)
						}
						if created.Load() != 0 {
							t.Error("listed old pins after creating the tracker")
						}
						if lists.Add(1) <= tt.listFailures {
							releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Temporary list failure"}`)
						} else {
							releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"repository":{"pinnedIssues":{"totalCount":%d,"nodes":[%s]}}}}`, len(nodes), strings.Join(nodes, ",")))
						}
						return
					}
					id := request.Variables["issueID"]
					if !reflect.DeepEqual(request.Variables, map[string]string{"issueID": id}) {
						t.Errorf("unexpected issue pin variables: %+v", request.Variables)
					}
					if id == "I_tracker" {
						if created.Load() != 1 {
							t.Error("updated new tracker pin before creating it")
						}
						if strings.Contains(request.Query, "mutation") {
							if !strings.Contains(request.Query, "updateIssuePin: pinIssue(") || countPinned() >= 3 {
								t.Errorf("unexpected new tracker mutation: %s", request.Query)
							}
							pins.Add(1)
							newPinned.Store(true)
							releaseGitHubJSON(t, w, http.StatusOK, `{"data":{"updateIssuePin":{"issue":{"id":"I_tracker","isPinned":true}}}}`)
						} else {
							releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":"I_tracker","state":"OPEN","isPinned":%t,"repository":{"pinnedIssues":{"totalCount":%d}}}}}`, newPinned.Load(), countPinned()))
						}
						return
					}
					i, ok := issueIndexes[id]
					if !ok {
						t.Errorf("unexpected issue ID: %q", id)
						http.NotFound(w, r)
						return
					}
					if strings.Contains(request.Query, "mutation") {
						if !wantUnpinned[i] || !pinned[i].Load() || !strings.Contains(request.Query, "updateIssuePin: unpinIssue(") {
							t.Errorf("unexpected old tracker mutation: %+v", request)
						}
						if unpins.Add(1) <= tt.unpinFailures {
							releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Temporary unpin failure"}`)
							return
						}
						pinned[i].Store(false)
						if tt.loseUnpinResponse {
							releaseGitHubJSON(t, w, http.StatusBadGateway, `{"message":"Response lost after unpinning"}`)
						} else {
							releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"updateIssuePin":{"issue":{"id":%q,"isPinned":false}}}}`, id))
						}
						return
					}
					pin := tt.pins[i]
					state, body := pin.state, pin.body
					if pin.currentState != "" {
						state = pin.currentState
					}
					if pin.currentBody != "" {
						body = pin.currentBody
					}
					releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"data":{"node":{"id":%q,"state":%q,"body":%q,"isPinned":%t,"repository":{"pinnedIssues":{"totalCount":%d}}}}}`, id, state, body, pinned[i].Load(), countPinned()))
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
					releaseGitHubJSON(t, w, http.StatusOK, `[]`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
					warnings.Add(1)
					checkReleaseIssuePinWarning(t, r)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"id":99}`)
				default:
					t.Errorf("unexpected cleanup request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			output, err := captureCommandOutput(t, func() error {
				return runGitHubCommand(t, handleCreateReleaseDayIssue, "-repo=o/r", "-releases=1.27.0-1", "-set-azdo-variable-name=ReleaseIssue")
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output, "##vso[task.setvariable variable=ReleaseIssue]42") {
				t.Fatalf("release issue variable was not emitted: %s", output)
			}
			for i, pin := range tt.pins {
				want := !wantUnpinned[i] && !pin.alreadyUnpinned
				if got := pinned[i].Load(); got != want {
					t.Errorf("old issue %d has pinned=%t, want %t", i, got, want)
				}
			}
			if created.Load() != 1 || lists.Load() != tt.listFailures+1 ||
				unpins.Load() != int32(len(tt.wantUnpins))+tt.unpinFailures {

				t.Errorf("got created=%d lists=%d unpins=%d", created.Load(), lists.Load(), unpins.Load())
			}
			if warnings.Load() != tt.wantWarnings || pins.Load() != 1-tt.wantWarnings || newPinned.Load() != (tt.wantWarnings == 0) {
				t.Errorf("got warnings=%d pins=%d newPinned=%t", warnings.Load(), pins.Load(), newPinned.Load())
			}
		})
	}
}

func TestReleaseDayIssuePinCleanupErrors(t *testing.T) {
	listResponse := fmt.Sprintf(`{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[{"issue":{"id":"I_old","number":1,"state":"CLOSED","body":%q}}]}}}}`, releaseDayIssueBodyPrefix)
	stateResponse := fmt.Sprintf(`{"data":{"node":{"id":"I_old","state":"CLOSED","isPinned":true,"body":%q}}}`, releaseDayIssueBodyPrefix)
	for _, tt := range []struct {
		name, phase, response, wantError string
		status                           int
	}{
		{name: "list HTTP error", phase: "list", status: http.StatusForbidden, response: `{"message":"Cannot list pins"}`, wantError: "Cannot list pins"},
		{name: "list GraphQL error", phase: "list", response: `{"errors":[{"message":"Cannot list pins"}]}`, wantError: "Cannot list pins"},
		{name: "missing repository", phase: "list", response: `{"data":{}}`, wantError: "did not return"},
		{name: "missing connection", phase: "list", response: `{"data":{"repository":{}}}`, wantError: "did not return"},
		{name: "missing count", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"nodes":[]}}}}`, wantError: "did not return"},
		{name: "incomplete nodes", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[]}}}}`, wantError: "did not return all"},
		{name: "missing issue", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[{"issue":null}]}}}}`, wantError: "invalid pinned issue"},
		{name: "missing issue ID", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[{"issue":{"number":1,"state":"CLOSED"}}]}}}}`, wantError: "invalid pinned issue"},
		{name: "invalid issue number", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[{"issue":{"id":"I_old","number":0,"state":"CLOSED"}}]}}}}`, wantError: "invalid pinned issue"},
		{name: "invalid issue state", phase: "list", response: `{"data":{"repository":{"pinnedIssues":{"totalCount":1,"nodes":[{"issue":{"id":"I_old","number":1,"state":"UNKNOWN"}}]}}}}`, wantError: "invalid pinned issue"},
		{name: "state query error", phase: "state", response: `{"errors":[{"message":"Cannot read tracker"}]}`, wantError: "Cannot read tracker"},
		{name: "unpin HTTP error", phase: "unpin", status: http.StatusForbidden, response: `{"message":"Cannot unpin tracker"}`, wantError: "Cannot unpin tracker"},
		{name: "unpin GraphQL error", phase: "unpin", response: `{"errors":[{"message":"Cannot unpin tracker"}]}`, wantError: "Cannot unpin tracker"},
		{name: "missing unpin confirmation", phase: "unpin", response: `{"data":{}}`, wantError: "did not confirm"},
		{name: "wrong unpinned issue", phase: "unpin", response: `{"data":{"updateIssuePin":{"issue":{"id":"I_other","isPinned":false}}}}`, wantError: "did not confirm"},
		{name: "wrong unpinned state", phase: "unpin", response: `{"data":{"updateIssuePin":{"issue":{"id":"I_old","isPinned":true}}}}`, wantError: "did not confirm"},
		{name: "failed unpin recheck", phase: "recheck", response: `{"errors":[{"message":"Cannot recheck tracker"}]}`, wantError: "Cannot recheck tracker"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var created, stateQueries atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
					created.Add(1)
					releaseGitHubJSON(t, w, http.StatusCreated, `{"number":42,"node_id":"I_tracker"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					request := readReleaseIssuePinRequest(t, r)
					phase := "state"
					response := stateResponse
					if strings.Contains(request.Query, "repository(owner:") {
						phase, response = "list", listResponse
					} else if strings.Contains(request.Query, "mutation") {
						phase, response = "unpin", `{"errors":[{"message":"Cannot unpin tracker"}]}`
					} else if stateQueries.Add(1)%2 == 0 && tt.phase == "recheck" {
						phase = "recheck"
					}
					status := http.StatusOK
					if phase == tt.phase {
						response = tt.response
						if tt.status != 0 {
							status = tt.status
						}
					}
					releaseGitHubJSON(t, w, status, response)
				default:
					t.Errorf("unexpected cleanup request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := runGitHubCommand(t, handleCreateReleaseDayIssue, "-repo=o/r", "-releases=1.27.0-1")
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want %q", err, tt.wantError)
			}
			if tt.phase == "recheck" && !strings.Contains(err.Error(), "Cannot unpin tracker") {
				t.Errorf("recheck lost the original unpin error: %v", err)
			}
			if created.Load() != 0 {
				t.Error("created a tracker despite cleanup failure")
			}
		})
	}
}

func TestSyncReleaseIssuePinInvalidIssueNumber(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			err := runGitHubCommand(t, handleSyncReleaseIssuePin, "-repo=o/r", "-i="+value)
			if err == nil || !strings.Contains(err.Error(), "positive issue number") {
				t.Fatalf("got error %v, want invalid issue number", err)
			}
		})
	}
}
