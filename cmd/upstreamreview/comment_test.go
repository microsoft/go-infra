// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
)

func TestLinkSummary(t *testing.T) {
	const label = "backend review complete"
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	pr := github.PullRequest{State: new("open"), Base: &github.PullRequestBranch{SHA: &base}, Head: &github.PullRequestBranch{SHA: &head}, UpdatedAt: &github.Timestamp{Time: time.Unix(100, 0).UTC()}}
	event := github.PullRequestEvent{Repo: &github.Repository{FullName: new("fork/runtime")}, Number: new(7), PullRequest: &pr}
	var body string
	var writes []string
	status := http.StatusOK
	deleteStatus := http.StatusOK
	labelEvent := int64(21)
	appliedAt := *pr.UpdatedAt
	live := pr
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := r.Method + " " + r.URL.Path; op {
		case "GET /repos/fork/runtime/issues/7/events":
			if err := json.NewEncoder(w).Encode([]*github.IssueEvent{{
				Event: new("labeled"), Label: &github.Label{Name: label}, ID: &labelEvent, CreatedAt: &appliedAt,
			}}); err != nil {
				t.Error(err)
			}
		case "DELETE /repos/fork/runtime/issues/7/labels/" + label:
			writes = append(writes, "DELETE")
			if deleteStatus != http.StatusOK {
				http.Error(w, `{"message":"cannot delete label"}`, deleteStatus)
			}
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
	scan := scanResult{Base: base, Head: head, Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40), Attempt: 2}
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
		if !strings.Contains(body, footer) {
			t.Fatalf("comment does not link to the scanned commits: %q", body)
		}
		for _, part := range parts {
			if !strings.Contains(body, part) {
				t.Fatalf("comment %q is missing %q", body, part)
			}
		}
	}
	run(10, clean, "") // Clean PRs get no new comment.
	run(11, reviewNeeded, "POST")
	const filesLink = ". [See the affected files](https://github.com/fork/runtime/actions/runs/11/attempts/2)."
	checkBody("review needed", filesLink, "After reviewing the diffs", "open **Labels** (shortcut: `L`) and select `"+label+"`")
	pending := body
	run(11, reviewNeeded, "") // Same scan already linked.
	live.Labels = []*github.Label{{Name: label}}
	scan.LabelEvent = labelEvent
	run(11, acknowledged, "PATCH")
	checkBody("acknowledged", filesLink, "covers upstream Go `"+scan.Old[:12]+"` to `"+scan.New[:12]+"`", "remove `"+label+"` under **Labels**", scan.ackMarker())
	live.Labels = nil
	scan.LabelEvent = 0
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
	run(13, reviewNeeded, "")
	live = pr
	invalid := scan
	invalid.Verdict = "unknown"
	if err := linkSummary(t.Context(), client, &event, 13, invalid, label); err == nil {
		t.Fatal("accepted an unknown verdict")
	}
	status = http.StatusForbidden
	if err := linkSummary(t.Context(), client, &event, 13, scan, label); err == nil {
		t.Fatal("comment write failure was ignored")
	}
	status = http.StatusOK
	live.Labels = []*github.Label{{Name: label}}
	scan.LabelEvent = labelEvent
	labelEvent++
	run(14, reviewNeeded, "") // Do not remove a newer application of the label.
	labelEvent--
	appliedAt.Time = appliedAt.Add(time.Minute)
	run(14, reviewNeeded, "") // An old event must not delete an application made after it fired.
	appliedAt = *pr.UpdatedAt
	live.Head = &github.PullRequestBranch{SHA: &other}
	run(14, reviewNeeded, "") // Or remove it from a newer PR head.
	live.Head = pr.Head
	run(14, reviewNeeded, "DELETE,PATCH")
	checkBody("review needed", "remove it and apply it again")
	body = "" // A clean diff must still clear stale labels when there's no comment to edit.
	run(15, clean, "DELETE")
	deleteStatus = http.StatusForbidden
	if err := linkSummary(t.Context(), client, &event, 15, scan, label); err == nil || !strings.Contains(err.Error(), "remove stale label") {
		t.Fatalf("want label cleanup error, got %v", err)
	}
}
