// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunCompletionPlanDryRun(t *testing.T) {
	answer := json.RawMessage(`{"questionId":"question-1","answers":["yes"]}`)
	target := decodeAssessment(t, `{"id":"target","name":"Policy","scopeId":"scope","assessmentGroupId":"current","policyNodeId":"policy","graphVersion":1,"answers":[]}`)
	source := decodeAssessment(t, `{"id":"source","name":"Policy","graphVersion":1,"answers":[{"questionId":"question-1","answers":["yes"]}],"lastSession":{"sessionId":"source-session"}}`)
	api := &fakeComplianceAPI{
		work:          &assessmentWork{Questions: []question{{NodeID: "question-1"}}, Work: []json.RawMessage{json.RawMessage(`{}`)}},
		sourceSession: decodeSession(t, `{"id":"source-session","state":"complete","projectId":"project","witConfigId":"config","areaPath":"Project\\Old Area","iterationPath":"Project\\Old Iteration"}`),
	}
	plan := &completionPlan{
		TargetGroup: &assessmentGroup{ID: "current", Name: "Current"},
		Items: []completionPlanItem{{
			Target:      target,
			Source:      source,
			SourceGroup: &assessmentGroup{Name: "Previous"},
		}},
	}

	results, err := runCompletionPlan(context.Background(), api, &scope{ID: "scope", Name: "Product"}, plan, completionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if api.writeCalls != 0 || api.generateCalls != 0 {
		t.Fatalf("dry run made writes: assessment=%d session=%d", api.writeCalls, api.generateCalls)
	}
	result := results[0]
	if result.Status != "ready" || result.CopiedAnswers != 1 || result.QuestionCount != 1 || result.WorkCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := len(api.workAnswers); got != len([]json.RawMessage{answer}) {
		t.Fatalf("work answer count = %d, want 1", got)
	}
}

func TestRunCompletionPlanAppliesAndGeneratesSession(t *testing.T) {
	target := decodeAssessment(t, `{"id":"target","name":"Policy","scopeId":"scope","assessmentGroupId":"current","policyNodeId":"policy","graphVersion":1,"answers":[]}`)
	source := decodeAssessment(t, `{"id":"source","name":"Policy","graphVersion":1,"answers":[{"questionId":"question-1","answers":["yes"]}],"lastSession":{"sessionId":"source-session"}}`)
	api := &fakeComplianceAPI{
		work:          &assessmentWork{Questions: []question{{NodeID: "question-1"}}},
		sourceSession: decodeSession(t, `{"id":"source-session","state":"complete","projectId":"project","witConfigId":"config","areaPath":"Project\\Old Area","iterationPath":"Project\\Old Iteration","workItems":[{"itemType":"child","nodeId":"activity","workItemId":"old","workItemState":{"state":"Completed","isCompletedCategory":true}}]}`),
		generated:     decodeSession(t, `{"id":"generated-session","assessmentId":"target","state":"complete","serverUrl":"https://dev.azure.com/org/project","workItems":[{"itemType":"parent","workItemId":"122","workItemState":{"state":"Proposed"}},{"itemType":"child","nodeId":"activity","workItemId":"123","workItemState":{"state":"Proposed"}}]}`),
	}
	plan := &completionPlan{
		TargetGroup: &assessmentGroup{ID: "current", Name: "Current"},
		Items: []completionPlanItem{{
			Target:      target,
			Source:      source,
			SourceGroup: &assessmentGroup{Name: "Previous"},
		}},
	}

	results, err := runCompletionPlan(context.Background(), api, &scope{ID: "scope", Name: "Product"}, plan, completionOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if api.writeCalls != 1 || api.generateCalls != 1 {
		t.Fatalf("writes: assessment=%d session=%d", api.writeCalls, api.generateCalls)
	}
	if got := string(api.generationPayload["name"]); got != `"Policy for Current of Product"` {
		t.Fatalf("session name = %s", got)
	}
	if got := string(api.generationPayload["projectId"]); got != `"project"` {
		t.Fatalf("project ID = %s", got)
	}
	if got := string(api.generationPayload["areaPath"]); got != `"Project"` {
		t.Fatalf("area path = %s, want project root", got)
	}
	if got := string(api.generationPayload["iterationPath"]); got != `"Project"` {
		t.Fatalf("iteration path = %s, want project root", got)
	}
	if got := string(api.generationPayload["workItemTags"]); got != `[]` {
		t.Fatalf("work item tags = %s, want []", got)
	}
	result := results[0]
	if result.Status != "complete" || result.SessionID != "generated-session" || result.WorkItemCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if got := api.stateUpdates["123"]; got != "Completed" {
		t.Fatalf("work item state update = %q, want Completed", got)
	}
}

func TestRunCompletionPlanBlocksDroppedAnswers(t *testing.T) {
	target := decodeAssessment(t, `{"id":"target","name":"Policy","scopeId":"scope","assessmentGroupId":"current","policyNodeId":"policy","graphVersion":1,"answers":[]}`)
	source := decodeAssessment(t, `{"id":"source","name":"Policy","graphVersion":1,"answers":[{"questionId":"old-question","answers":["yes"]}],"lastSession":{"sessionId":"source-session"}}`)
	api := &fakeComplianceAPI{work: &assessmentWork{Questions: []question{{NodeID: "new-question"}}}}
	plan := &completionPlan{
		TargetGroup: &assessmentGroup{ID: "current", Name: "Current"},
		Items:       []completionPlanItem{{Target: target, Source: source, SourceGroup: &assessmentGroup{Name: "Previous"}}},
	}

	results, err := runCompletionPlan(context.Background(), api, &scope{Name: "Product"}, plan, completionOptions{Apply: true})
	if err == nil {
		t.Fatal("runCompletionPlan succeeded, want incompatibility error")
	}
	if results[0].Status != "needs review" || results[0].DroppedAnswers != 1 {
		t.Fatalf("result = %+v", results[0])
	}
	if api.writeCalls != 0 || api.generateCalls != 0 {
		t.Fatal("incompatible answer plan made writes")
	}
}

func TestFilterAnswersForQuestionsRejectsChangedDefinition(t *testing.T) {
	answer := json.RawMessage(`{"questionId":"question-1","answers":["yes"]}`)
	source := decodeQuestion(t, `{"nodeId":"question-1","questionPlainText":"Old question","answerTemplate":{"type":"boolean"}}`)
	current := decodeQuestion(t, `{"nodeId":"question-1","questionPlainText":"New question","answerTemplate":{"type":"boolean"}}`)

	answers, dropped, err := filterAnswersForQuestions([]json.RawMessage{answer}, []question{source}, []question{current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 0 || dropped != 1 {
		t.Fatalf("answers = %d, dropped = %d; want 0, 1", len(answers), dropped)
	}
}

func TestValidateExplicitAnswers(t *testing.T) {
	questions := []question{
		decodeQuestion(t, `{"nodeId":"choice","answerTemplate":{"options":[{"value":"Yes"},{"value":"No"}]}}`),
		decodeQuestion(t, `{"nodeId":"text","answerTemplate":{"type":"text"}}`),
	}
	answers := []json.RawMessage{
		json.RawMessage(`{"questionId":"choice","answers":["Yes"]}`),
		json.RawMessage(`{"questionId":"text","answers":["Go"]}`),
	}
	if err := validateExplicitAnswers(answers, questions); err != nil {
		t.Fatal(err)
	}
	if err := validateExplicitAnswers(answers[:1], questions); err == nil {
		t.Fatal("incomplete answer override succeeded")
	}
}

func TestPlanWorkItemClosuresNormalizesAndApprovesCurrentNode(t *testing.T) {
	source := decodeSession(t, `{"id":"source","state":"complete","workItems":[{"itemType":"child","nodeId":"Activity existing","workItemId":"1","workItemState":{"state":"Completed","isCompletedCategory":true}}]}`)
	target := decodeSession(t, `{"id":"target","state":"complete","serverUrl":"https://dev.azure.com/org/project","workItems":[{"itemType":"child","nodeId":"existing","workItemId":"2","workItemState":{"state":"Proposed"}},{"itemType":"child","nodeId":"Activity new","workItemId":"3","workItemState":{"state":"Proposed"}}]}`)

	closures, err := planWorkItemClosures(source, target, map[string]struct{}{"new": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(closures) != 2 || closures[0].State != "Completed" || closures[1].State != "Completed" {
		t.Fatalf("closures = %+v", closures)
	}
}

func TestPlanWorkItemClosuresRejectsDuplicateNormalizedApprovals(t *testing.T) {
	source := decodeSession(t, `{"id":"source","state":"complete"}`)
	target := decodeSession(t, `{"id":"target","state":"complete"}`)

	_, err := planWorkItemClosures(source, target, map[string]struct{}{"new": {}, "Activity new": {}})
	if err == nil || !strings.Contains(err.Error(), "are duplicates") {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitForSessionRejectsEmptyResponse(t *testing.T) {
	_, err := waitForSession(context.Background(), &fakeComplianceAPI{}, nil, 0)
	if err == nil || err.Error() != "completion session response is empty" {
		t.Fatalf("error = %v", err)
	}
}

func TestWaitForNewSessionAcceptsReusedFailedSessionID(t *testing.T) {
	api := &fakeComplianceAPI{generated: &session{ID: "reused-session", State: "complete"}}

	got, err := waitForNewSession(context.Background(), api, "assessment", "reused-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "reused-session" || got.State != "complete" {
		t.Fatalf("session = %+v", got)
	}
}

func TestRunCompletionPlanReconcilesExistingSession(t *testing.T) {
	targetSession := decodeSession(t, `{"id":"target-session","state":"complete","serverUrl":"https://dev.azure.com/org/project","workItems":[{"itemType":"parent","workItemId":"200","workItemState":{"state":"Proposed"}},{"itemType":"child","nodeId":"activity","workItemId":"201","workItemState":{"state":"Proposed"}}]}`)
	target := decodeAssessment(t, `{"id":"target","name":"Policy","lastSession":{"sessionId":"target-session"}}`)
	source := decodeAssessment(t, `{"id":"source","name":"Policy","lastSession":{"sessionId":"source-session"}}`)
	api := &fakeComplianceAPI{
		sourceSession: decodeSession(t, `{"id":"source-session","state":"complete","workItems":[{"itemType":"parent","workItemId":"100","workItemState":{"state":"Proposed"}},{"itemType":"child","nodeId":"activity","workItemId":"101","workItemState":{"state":"Completed","isCompletedCategory":true}}]}`),
		generated:     targetSession,
	}
	plan := &completionPlan{
		TargetGroup: &assessmentGroup{Name: "Current"},
		Items:       []completionPlanItem{{Target: target, Source: source, SourceGroup: &assessmentGroup{Name: "Previous"}, ExistingSession: targetSession}},
	}

	results, err := runCompletionPlan(context.Background(), api, &scope{Name: "Product"}, plan, completionOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if api.writeCalls != 0 || api.generateCalls != 0 {
		t.Fatalf("reconciliation regenerated assessment: writes=%d generations=%d", api.writeCalls, api.generateCalls)
	}
	if got := api.stateUpdates["201"]; got != "Completed" {
		t.Fatalf("child state update = %q, want Completed", got)
	}
	if _, exists := api.stateUpdates["200"]; exists {
		t.Fatal("parent work item was updated")
	}
	if results[0].Status != "complete" || results[0].WorkItemCount != 1 {
		t.Fatalf("result = %+v", results[0])
	}
}

type fakeComplianceAPI struct {
	work              *assessmentWork
	sourceSession     *session
	generated         *session
	workAnswers       []json.RawMessage
	generationPayload map[string]json.RawMessage
	stateUpdates      map[string]string
	writeCalls        int
	generateCalls     int
}

func (f *fakeComplianceAPI) getAssessmentWork(_ context.Context, _ *assessment, answers []json.RawMessage) (*assessmentWork, error) {
	f.workAnswers = answers
	return f.work, nil
}

func (f *fakeComplianceAPI) getPolicyQuestions(_ context.Context, _ string, _ int64) ([]question, error) {
	return nil, nil
}

func (f *fakeComplianceAPI) writeAssessment(_ context.Context, target *assessment, _ []json.RawMessage) (*assessment, error) {
	f.writeCalls++
	return target, nil
}

func (f *fakeComplianceAPI) getSession(_ context.Context, sessionID string) (*session, error) {
	if f.sourceSession != nil && sessionID == f.sourceSession.ID {
		return f.sourceSession, nil
	}
	if f.generated != nil && sessionID == f.generated.ID {
		return f.generated, nil
	}
	return nil, nil
}

func (f *fakeComplianceAPI) getLatestSession(_ context.Context, _ string) (*session, error) {
	return f.generated, nil
}

func (f *fakeComplianceAPI) generateSession(_ context.Context, _ string, payload map[string]json.RawMessage) error {
	f.generateCalls++
	f.generationPayload = payload
	return nil
}

func (f *fakeComplianceAPI) setWorkItemState(_ context.Context, _ string, workItemID, state string) error {
	if f.stateUpdates == nil {
		f.stateUpdates = make(map[string]string)
	}
	f.stateUpdates[workItemID] = state
	if f.generated != nil {
		for index := range f.generated.WorkItems {
			if f.generated.WorkItems[index].WorkItemID == workItemID {
				f.generated.WorkItems[index].WorkItemState.State = state
				f.generated.WorkItems[index].WorkItemState.IsCompletedCategory = true
			}
		}
	}
	return nil
}

func decodeAssessment(t *testing.T, data string) *assessment {
	t.Helper()
	var result assessment
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func decodeSession(t *testing.T, data string) *session {
	t.Helper()
	var result session
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func decodeQuestion(t *testing.T, data string) question {
	t.Helper()
	var result question
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
