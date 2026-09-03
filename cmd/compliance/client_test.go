// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComplianceClientWorkflowRequests(t *testing.T) {
	const token = "test-token"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		username, password, ok := r.BasicAuth()
		if !ok || username != "" || password != token {
			t.Errorf("unexpected basic authentication: ok=%v username=%q", ok, username)
		}
		expectedAccept := "application/json;api-version=3.0-preview.1;excludeUrls=true"
		if strings.Contains(r.URL.Path, "/_apis/wit/") {
			expectedAccept = "application/json"
		}
		if got := r.Header.Get("Accept"); got != expectedAccept {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json; odata.metadata=minimal")

		switch requests {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/account/v2/_odata/scopes('scope-id')" {
				t.Fatalf("scope request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query().Get("$expand"); got != "assessmentGroups($expand=modifiedByUser,assessments($expand=modifiedByUser))" {
				t.Errorf("scope expansion = %q", got)
			}
			_, _ = w.Write([]byte(`{"id":"scope-id","name":"Product","assessmentGroups":[]}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/account/v2/_odata/assessments('assessment-id')/work.retrieve" {
				t.Fatalf("work request = %s %s", r.Method, r.URL.Path)
			}
			var body assessmentWorkRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !body.UseLatestGraph || body.ScopeID != "scope-id" || body.PolicyNodeID != "policy-id" || len(body.Answers) != 1 {
				t.Errorf("work request body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"assessment-id","answers":[],"work":[],"questions":[{"nodeId":"question-id"}]}`))
		case 3:
			if r.Method != http.MethodGet || r.URL.Path != "/account/v2/_odata/questions/questions.getPolicyQuestions(policyId=('cai-security∕policy-id'))" {
				t.Fatalf("questions request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query().Get("graphVersion"); got != "123" {
				t.Errorf("graph version = %q", got)
			}
			_, _ = w.Write([]byte(`{"value":[{"nodeId":"question-id"}]}`))
		case 4:
			if r.Method != http.MethodPut || r.URL.Path != "/account/v2/_odata/assessments" {
				t.Fatalf("assessment request = %s %s", r.Method, r.URL.Path)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body["modifiedByUser"]; ok {
				t.Error("assessment update includes expanded modifiedByUser")
			}
			if got := rawArrayLength(t, body["answers"]); got != 1 {
				t.Errorf("assessment answer count = %d, want 1", got)
			}
			_, _ = w.Write([]byte(`{"id":"assessment-id","name":"Policy","scopeId":"scope-id","assessmentGroupId":"group-id","policyNodeId":"policy-id","answers":[]}`))
		case 5:
			if r.Method != http.MethodGet || r.URL.Path != "/account/v2/_odata/sessions" {
				t.Fatalf("session request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query().Get("$filter"); got != "id eq 'session-id'" {
				t.Errorf("session filter = %q", got)
			}
			_, _ = w.Write([]byte(`{"value":[{"id":"session-id","state":"complete","workItems":[]}]}`))
		case 6:
			if r.Method != http.MethodPost || r.URL.Path != "/account/v2/_odata/assessments('assessment-id')/sessions.generate" {
				t.Fatalf("generate request = %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`null`))
		case 7:
			if r.Method != http.MethodGet || r.URL.Path != "/account/v2/_odata/sessions" {
				t.Fatalf("latest session request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query().Get("$filter"); got != "assessmentId eq 'assessment-id'" {
				t.Errorf("latest session filter = %q", got)
			}
			if got := r.URL.Query().Get("$orderby"); got != "createdDateTime desc" {
				t.Errorf("latest session order = %q", got)
			}
			_, _ = w.Write([]byte(`{"value":[{"id":"new-session","assessmentId":"assessment-id","state":"complete","workItems":[]}]}`))
		case 8:
			if r.Method != http.MethodPatch || r.URL.Path != "/project/_apis/wit/workitems/123" {
				t.Fatalf("work item update = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json-patch+json" {
				t.Errorf("work item update content type = %q", got)
			}
			var patch []struct {
				Op    string `json:"op"`
				Path  string `json:"path"`
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			if len(patch) != 1 || patch[0].Op != "replace" || patch[0].Path != "/fields/System.State" || patch[0].Value != "Completed" {
				t.Errorf("work item patch = %+v", patch)
			}
			_, _ = w.Write([]byte(`{"id":123,"fields":{"System.State":"Completed"}}`))
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newComplianceClient(server.URL+"/account", token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.getScope(ctx, "scope-id"); err != nil {
		t.Fatal(err)
	}
	answer := json.RawMessage(`{"questionId":"question-id","answers":["yes"]}`)
	targetJSON := `{"id":"assessment-id","name":"Policy","scopeId":"scope-id","assessmentGroupId":"group-id","policyNodeId":"policy-id","answers":[],"modifiedByUser":{"id":"user"}}`
	var target assessment
	if err := json.Unmarshal([]byte(targetJSON), &target); err != nil {
		t.Fatal(err)
	}
	if _, err := client.getAssessmentWork(ctx, &target, []json.RawMessage{answer}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.getPolicyQuestions(ctx, "cai-security/policy-id", 123); err != nil {
		t.Fatal(err)
	}
	if _, err := client.writeAssessment(ctx, &target, []json.RawMessage{answer}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.getSession(ctx, "session-id"); err != nil {
		t.Fatal(err)
	}
	if err := client.generateSession(ctx, "assessment-id", map[string]json.RawMessage{"name": json.RawMessage(`"session"`)}); err != nil {
		t.Fatal(err)
	}
	latest, err := client.getLatestSession(ctx, "assessment-id")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != "new-session" {
		t.Fatalf("latest session ID = %q", latest.ID)
	}
	if err := client.setWorkItemState(ctx, server.URL+"/project", "123", "Completed"); err != nil {
		t.Fatal(err)
	}
	if requests != 8 {
		t.Fatalf("request count = %d, want 8", requests)
	}
}

func TestComplianceClientRejectsNonJSONErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<html>sensitive proxy response</html>"))
	}))
	defer server.Close()

	client, err := newComplianceClient(server.URL, "test-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.getScope(context.Background(), "scope-id")
	if err == nil {
		t.Fatal("getScope succeeded, want error")
	}
	if got := err.Error(); got != "GET scopes('scope-id'): HTTP 503" {
		t.Fatalf("error = %q", got)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatal("error includes non-JSON response body")
	}
}

func TestComplianceClientUsesBearerAuthForJWT(t *testing.T) {
	const token = "header.payload.signature"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if _, _, ok := r.BasicAuth(); ok {
			t.Error("request unexpectedly uses basic authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/_apis/wit/") {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"scope-id","name":"Product","assessmentGroups":[]}`))
	}))
	defer server.Close()

	client, err := newComplianceClient(server.URL+"/account", token, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.getScope(context.Background(), "scope-id"); err != nil {
		t.Fatal(err)
	}
	if err := client.setWorkItemState(context.Background(), server.URL+"/project", "123", "Completed"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func rawArrayLength(t *testing.T, data json.RawMessage) int {
	t.Helper()
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	return len(values)
}
