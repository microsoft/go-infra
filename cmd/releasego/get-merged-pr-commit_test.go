// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetMergedPRCommitCheckRetries(t *testing.T) {
	for _, tt := range []struct {
		name           string
		firstFailures  int32
		secondFailures int32
		conclusion     string
		wantError      string
	}{
		{"success", 0, 0, "success", ""},
		{"first page recovers", 1, 0, "success", ""},
		{"second page recovers", 0, 1, "success", ""},
		{"first page exhausts retries", 5, 0, "success", "500"},
		{"second page exhausts retries", 0, 5, "success", "500"},
		{"failed check after retry", 0, 1, "failure", "failed check detected: https://github.com/o/r/actions/runs/99"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var prs, firstPage, secondPage atomic.Int32
			newReleaseGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
				}
				switch r.URL.Path {
				case "/repos/o/r/pulls/42":
					if prs.Add(1) == 1 {
						releaseGitHubJSON(t, w, http.StatusOK, `{"state":"open","head":{"sha":"head-sha"}}`)
					} else {
						releaseGitHubJSON(t, w, http.StatusOK, `{"merged":true,"merge_commit_sha":"merged-sha"}`)
					}
				case "/repos/o/r/commits/head-sha/check-runs":
					if r.URL.Query().Get("status") != "completed" {
						t.Error("check-run request must filter for completed checks")
					}
					switch r.URL.Query().Get("page") {
					case "":
						if firstPage.Add(1) <= tt.firstFailures {
							releaseGitHubJSON(t, w, http.StatusInternalServerError, `{"message":"temporary failure"}`)
							return
						}
						w.Header().Set("Link", `<https://api.github.com/repos/o/r/commits/head-sha/check-runs?status=completed&page=2>; rel="next"`)
						releaseGitHubJSON(t, w, http.StatusOK, `{"check_runs":[{"conclusion":"success"}]}`)
					case "2":
						if secondPage.Add(1) <= tt.secondFailures {
							releaseGitHubJSON(t, w, http.StatusInternalServerError, `{"message":"temporary failure"}`)
							return
						}
						releaseGitHubJSON(t, w, http.StatusOK, fmt.Sprintf(`{"check_runs":[{"conclusion":%q,"html_url":"https://github.com/o/r/actions/runs/99"}]}`, tt.conclusion))
					default:
						t.Errorf("unexpected page: %s", r.URL.RawQuery)
						http.NotFound(w, r)
					}
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})

			err := runGitHubCommand(t, handleGetMergedPRCommit, "-repo=o/r", "-pr=42", "-poll-delay=0")
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("got error %v, want %q", err, tt.wantError)
			}

			wantFirst := min(tt.firstFailures+1, 5)
			wantSecond := min(tt.secondFailures+1, 5)
			if tt.firstFailures == 5 {
				wantSecond = 0
			}
			if firstPage.Load() != wantFirst || secondPage.Load() != wantSecond {
				t.Errorf("page requests = (%d, %d), want (%d, %d)", firstPage.Load(), secondPage.Load(), wantFirst, wantSecond)
			}
			wantPRs := int32(1)
			if tt.wantError == "" {
				wantPRs = 2
			}
			if prs.Load() != wantPRs {
				t.Errorf("PR requests = %d, want %d", prs.Load(), wantPRs)
			}
		})
	}
}
