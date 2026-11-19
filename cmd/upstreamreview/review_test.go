// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
	"github.com/microsoft/go-infra/importdiff"
)

func testClient(t *testing.T, handler http.HandlerFunc) *github.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	client, err := github.NewClient(github.WithHTTPClient(s.Client()), github.WithURLs(new(s.URL+"/"), nil))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCurrentTeamApproval(t *testing.T) {
	const approved = "APPROVED"
	user := func(login, kind string) *github.User {
		return &github.User{Login: &login, Type: &kind}
	}
	review := func(login, kind, state, rev string) *github.PullRequestReview {
		return &github.PullRequestReview{User: user(login, kind), State: &state, CommitID: &rev}
	}
	member := review("member", "User", approved, "head")
	for _, tt := range []struct {
		name    string
		reviews []*github.PullRequestReview
		want    bool
	}{
		{"current approval then comment", []*github.PullRequestReview{member, review("member", "User", "COMMENTED", "head")}, true},
		{"stale approval", []*github.PullRequestReview{review("member", "User", approved, "old")}, false},
		{"dismissed", []*github.PullRequestReview{review("member", "User", "DISMISSED", "head")}, false},
		{"superseded", []*github.PullRequestReview{member, review("member", "User", "CHANGES_REQUESTED", "head")}, false},
		{"ineligible approvals", []*github.PullRequestReview{
			review("author", "User", approved, "head"), review("bot", "Bot", approved, "head"), review("outsider", "User", approved, "head"),
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/repos/o/r/pulls/7/reviews":
					if r.URL.Query().Get("page") == "" {
						// The verdict is deliberately on the next page.
						w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, r.Host, r.URL.Path))
						fmt.Fprint(w, "[]")
					} else {
						if err := json.NewEncoder(w).Encode(tt.reviews); err != nil {
							t.Error(err)
						}
					}
				case "/orgs/o/teams/team/memberships/member":
					fmt.Fprint(w, `{"state":"active"}`)
				case "/orgs/o/teams/team/memberships/outsider":
					http.NotFound(w, r)
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			})
			g := gate{client: client, owner: "o", repo: "r", team: "team"}
			pr := &github.PullRequest{Number: new(7), User: user("author", "User"), Head: &github.PullRequestBranch{SHA: new("head")}}
			if got, err := g.approved(t.Context(), pr); err != nil || got != tt.want {
				t.Errorf("approved = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestGateTransitions(t *testing.T) {
	base, head, merge := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	old, updated := strings.Repeat("d", 40), strings.Repeat("e", 40)
	var statuses []github.RepoStatus
	var report string
	var reviews []*github.PullRequestReview
	var scans, requests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"number":7,"state":"open","user":{"login":"author"},"head":{"sha":%q},"base":{"sha":%q},"merge_commit_sha":%q,"requested_teams":[{"slug":"team"}]}`, head, base, merge)
	})
	mux.HandleFunc("GET /repos/o/r/git/commits/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q},"parents":[{"sha":%q},{"sha":%q}]}`, id, id, base, head)
	})
	mux.HandleFunc("GET /repos/o/r/git/trees/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := old
		if r.PathValue("id") == merge {
			id = updated
		}
		fmt.Fprintf(w, `{"tree":[{"path":"go","type":"commit","mode":"160000","sha":%q}]}`, id)
	})
	mux.HandleFunc("GET /repos/o/r/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(reviews); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /orgs/o/teams/team/memberships/member", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"state":"active"}`)
	})
	mux.HandleFunc("GET /repos/o/r/commits/{id}/statuses", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != merge {
			t.Errorf("status looked up on %q, want current merge", r.PathValue("id"))
		}
		list := slices.Clone(statuses)
		slices.Reverse(list)
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("POST /repos/o/r/statuses/{id}", func(w http.ResponseWriter, r *http.Request) {
		var s github.RepoStatus
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil || r.PathValue("id") != merge || s.GetContext() != checkName {
			t.Errorf("unexpected status on %s: %v, %v", r.PathValue("id"), s, err)
		}
		s.Creator = &github.User{Login: new("app[bot]")}
		statuses = append(statuses, s)
		fmt.Fprint(w, "{}")
	})
	mux.HandleFunc("GET /repos/o/r/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":1,"user":{"login":"app[bot]"},"body":%q}]`, report)
	})
	mux.HandleFunc("POST /repos/o/r/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		var c github.IssueCommentRequest
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil || !strings.Contains(c.Body, "example.go") || !strings.Contains(c.Body, old+".."+updated) {
			t.Errorf("unexpected report: %s (%v)", c.Body, err)
		}
		report = c.Body
		requests++
		fmt.Fprint(w, "{}")
	})
	g := gate{client: testClient(t, mux.ServeHTTP), owner: "o", repo: "r", team: "team", slug: "app", scans: make(map[int]result)}
	g.compare = func(_ context.Context, a, b string) ([]importdiff.Change, error) {
		if a != old || b != updated {
			t.Fatalf("compared %q to %q; want base to proposed merge gitlinks", a, b)
		}
		scans++
		return []importdiff.Change{{Path: "src/example.go"}}, nil
	}
	run := func(want string) {
		t.Helper()
		if err := g.reconcile(t.Context(), 7); err != nil {
			t.Fatal(err)
		}
		if len(statuses) == 0 || statuses[len(statuses)-1].GetState() != want {
			t.Fatalf("statuses = %v; want latest %s", statuses, want)
		}
	}
	run("pending")
	reviews = []*github.PullRequestReview{{User: &github.User{Type: new("User"), Login: new("member")}, State: new("APPROVED"), CommitID: new(head)}}
	run("success")
	run("success") // Redelivery does not duplicate a status, comment, or scan.
	reviews[0].State = new("DISMISSED")
	run("pending")
	if len(statuses) != 3 || scans != 1 || requests != 1 {
		t.Fatalf("statuses %d, scans %d, comments %d", len(statuses), scans, requests)
	}
	reviews[0].State = new("APPROVED")
	run("success")
	head, merge = strings.Repeat("4", 40), strings.Repeat("5", 40)
	run("pending") // Same upstream diff; the previous approval does not transfer.
	// Target branch advances to the proposed pin. The PR no longer changes it,
	// even though its head was based on a different revision.
	base, merge, old = strings.Repeat("1", 40), strings.Repeat("2", 40), updated
	run("success")
	if scans != 1 {
		t.Fatal("scanned an unrelated PR")
	}
	// An invalid/missing gitlink must not result in a clean scan.
	merge, updated = strings.Repeat("3", 40), ""
	if err := g.reconcile(t.Context(), 7); err == nil || statuses[len(statuses)-1].GetState() != "error" {
		t.Fatalf("unreadable gitlink: %v, statuses %v", err, statuses)
	}
}
