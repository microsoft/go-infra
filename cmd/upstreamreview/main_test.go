// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhook(t *testing.T) {
	const secret = "test webhook secret"
	for _, tt := range []struct {
		name, event, fields, key string
		status                   int
		want                     work
	}{
		{"push", "push", `"ref":"refs/heads/microsoft/main"`, secret, 202, work{base: "microsoft/main"}},
		{"review dismissed", "pull_request_review", `"action":"dismissed","pull_request":{"number":4}`, secret, 202, work{number: 4}},
		{"new head", "pull_request", `"action":"synchronize","pull_request":{"number":3}`, secret, 202, work{number: 3}},
		{"untrusted", "pull_request_review", `"pull_request":{"number":4}`, "wrong secret", 401, work{}},
		{"not a PR action we handle", "pull_request", `"action":"review_requested","pull_request":{"number":2}`, secret, 204, work{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			queue := make(chan work, 1)
			body := `{"installation":{"id":12},"repository":{"full_name":"o/r"},` + tt.fields + `}`
			mac := hmac.New(sha256.New, []byte(tt.key))
			mac.Write([]byte(body))
			r := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-GitHub-Event", tt.event)
			r.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%x", mac.Sum(nil)))
			w := httptest.NewRecorder()
			webhook("o/r", 12, []byte(secret), queue).ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body)
			}
			var got work
			select {
			case got = <-queue:
			default:
			}
			if got != tt.want {
				t.Fatalf("queued %+v, want %+v", got, tt.want)
			}
		})
	}
}
