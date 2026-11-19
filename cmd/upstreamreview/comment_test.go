// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
)

func TestLinkSummary(t *testing.T) {
	const label = "backend review complete"
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	pr := github.PullRequest{State: new("open"), Base: &github.PullRequestBranch{SHA: &base}, Head: &github.PullRequestBranch{SHA: &head}}
	event := github.PullRequestEvent{Repo: &github.Repository{FullName: new("fork/runtime")}, Number: new(7), PullRequest: &pr}
	var body string
	var writes []string
	status := http.StatusOK
	live := pr
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := r.Method + " " + r.URL.Path; op {
		case "GET /repos/fork/runtime/issues/7/comments":
			var comments []*github.IssueComment
			if r.URL.Query().Get("page") != "2" {
				w.Header().Set("Link", `<?page=2>; rel="next"`)
				comments = append(comments, &github.IssueComment{ID: new(int64(11)), User: &github.User{Login: new("someone-else")}, Body: new(commentMarker + "not ours")})
			} else if body != "" {
				comments = append(comments, &github.IssueComment{ID: new(int64(42)), User: &github.User{Login: new("github-actions[bot]")}, Body: &body})
			}
			if err := json.NewEncoder(w).Encode(comments); err != nil {
				t.Error(err)
			}
		case "GET /repos/fork/runtime/pulls/7":
			if err := json.NewEncoder(w).Encode(live); err != nil {
				t.Error(err)
			}
		case "POST /repos/fork/runtime/issues/7/comments", "PATCH /repos/fork/runtime/issues/comments/42":
			if status != http.StatusOK {
				http.Error(w, `{"message":"write denied"}`, status)
				return
			}
			var request github.IssueCommentRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			body = request.Body
			writes = append(writes, r.Method)
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request: %s", op)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	scan := scanResult{Base: base, Head: head, Attempt: 2}
	run := func(id int64, v verdict, wantWrite string) {
		t.Helper()
		writes = nil
		scan.Verdict = v
		if err := linkSummary(t.Context(), client, &event, id, scan, label); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(writes, ","); got != wantWrite {
			t.Fatalf("writes = %q; want %q", got, wantWrite)
		}
	}
	checkBody := func(state string, parts ...string) {
		t.Helper()
		if !strings.HasPrefix(body, commentMarker+"### Upstream Go review \u2014 "+state+"\n\n") {
			t.Fatalf("expected heading for %q in %q", state, body)
		}
		footer := fmt.Sprintf("_Last scanned PR head [`%s`](https://github.com/fork/runtime/commit/%s) against base [`%s`](https://github.com/fork/runtime/commit/%s)._", head[:12], head, base[:12], base)
		if !strings.HasSuffix(body, footer) {
			t.Fatalf("comment does not link to the scanned commits: %q", body)
		}
		for _, part := range parts {
			if !strings.Contains(body, part) {
				t.Fatalf("comment %q is missing %q", body, part)
			}
		}
	}
	run(10, clean, "")                           // Clean PRs get no new comment.
	live.Labels = []*github.Label{{Name: label}} // Label added after the scan.
	run(11, reviewNeeded, "POST")
	const filesLink = ". [See the affected files](https://github.com/fork/runtime/actions/runs/11/attempts/2)."
	checkBody("review needed", filesLink, "After reviewing the diffs", "open **Labels** (shortcut: `L`) and select `"+label+"`")
	pending := body
	run(11, reviewNeeded, "") // Same scan already linked.
	live.Labels = nil         // Label removed after the next scan.
	run(11, acknowledged, "PATCH")
	checkBody("acknowledged", filesLink, "carries over to new commits", "remove `"+label+"` under **Labels**")
	run(11, reviewNeeded, "PATCH")
	if body != pending {
		t.Fatalf("removing the label did not restore the review instructions: %q", body)
	}
	run(12, clean, "PATCH")
	checkBody("no matching changes", "No action needed", "[View the scan summary](https://github.com/fork/runtime/actions/runs/12/attempts/2)")
	if strings.Contains(body, label) {
		t.Fatalf("clean comment still mentions the label: %q", body)
	}
	// Even when the event and live PR agree, the scan's SHAs must still match.
	other := strings.Repeat("c", 40)
	scan.Base = other
	run(13, reviewNeeded, "")
	scan.Base, scan.Head = base, other
	run(13, clean, "")
	scan.Head = head
	live.State = new("closed")
	run(13, acknowledged, "")
	live = pr
	if err := linkSummary(t.Context(), client, &event, 13, scanResult{Verdict: "unknown", Base: base, Head: head, Attempt: 1}, label); err == nil {
		t.Fatal("accepted an unknown verdict")
	}
	status = http.StatusForbidden
	run(13, reviewNeeded, "")
	status = http.StatusInternalServerError
	if err := linkSummary(t.Context(), client, &event, 13, scan, label); err == nil {
		t.Fatal("unexpected API error was ignored")
	}
}
