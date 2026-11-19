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
	run := func(id int64, matched bool, wantWrite string) {
		t.Helper()
		writes = nil
		if err := linkSummary(t.Context(), client, &event, id, matched, label); err != nil {
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
		for _, part := range parts {
			if !strings.Contains(body, part) {
				t.Fatalf("comment %q is missing %q", body, part)
			}
		}
	}
	run(10, false, "") // Clean PRs get no new comment.
	run(11, true, "POST")
	const filesLink = "[See the affected files](https://github.com/fork/runtime/actions/runs/11)"
	checkBody("review needed", filesLink, "After reviewing the diffs", "open **Labels** (shortcut: `L`) and select `"+label+"`")
	pending := body
	run(11, true, "") // Same run already linked.
	live.Labels = []*github.Label{{Name: label}}
	run(11, true, "PATCH") // Look at the live PR, not the event's labels.
	checkBody("acknowledged", filesLink, "carries over to new commits", "remove `"+label+"` under **Labels**")
	live.Labels = nil
	run(11, true, "PATCH")
	if body != pending {
		t.Fatalf("removing the label did not restore the review instructions: %q", body)
	}
	run(12, false, "PATCH")
	checkBody("no matching changes", "No action needed", "[View the scan summary](https://github.com/fork/runtime/actions/runs/12)")
	if strings.Contains(body, label) {
		t.Fatalf("clean comment still mentions the label: %q", body)
	}
	live.Head = &github.PullRequestBranch{SHA: new(strings.Repeat("c", 40))}
	run(13, true, "") // Older heads cannot overwrite the link.
	live = pr
	status = http.StatusForbidden
	run(13, true, "")
	status = http.StatusInternalServerError
	if err := linkSummary(t.Context(), client, &event, 13, true, label); err == nil {
		t.Fatal("unexpected API error was ignored")
	}
}
