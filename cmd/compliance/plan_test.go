// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildCompletionPlan(t *testing.T) {
	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	answer := json.RawMessage(`{"questionId":"question-1","answers":["yes"]}`)

	s := &scope{AssessmentGroups: []*assessmentGroup{
		{
			Name: "Old",
			Assessments: []*assessment{
				{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: old, LastModifiedDate: old, Answers: []json.RawMessage{answer}, LastSession: &sessionReference{SessionID: "old-a"}},
			},
		},
		{
			Name: "Previous",
			Assessments: []*assessment{
				{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: recent, LastModifiedDate: recent, Answers: []json.RawMessage{answer}, LastSession: &sessionReference{SessionID: "recent-a"}},
				{Name: "Policy B", PolicyNodeID: "b", CreatedDateTime: recent, LastModifiedDate: recent, LastSession: &sessionReference{SessionID: "recent-b"}},
			},
		},
		{
			Name: "Current",
			Assessments: []*assessment{
				{Name: "Policy D", PolicyNodeID: "d", CreatedDateTime: targetDate, LastSession: &sessionReference{SessionID: "current-d"}},
				{Name: "Policy C", PolicyNodeID: "c", CreatedDateTime: targetDate, IsActive: true},
				{Name: "Policy B", PolicyNodeID: "b", CreatedDateTime: targetDate},
				{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: targetDate},
				{Name: "Policy E", PolicyNodeID: "e", CreatedDateTime: targetDate},
			},
		},
	}}

	plan, err := buildCompletionPlan(s, "Current", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetGroup.Name != "Current" {
		t.Fatalf("target group = %q, want Current", plan.TargetGroup.Name)
	}

	items := planItemsByName(plan.Items)
	if got := items["Policy A"].Source.LastSession.SessionID; got != "recent-a" {
		t.Errorf("Policy A source session = %q, want recent-a", got)
	}
	if got := len(items["Policy B"].Source.Answers); got != 0 {
		t.Errorf("Policy B copied answer count = %d, want 0", got)
	}
	if got := items["Policy C"].SkipReason; got != "already in progress" {
		t.Errorf("Policy C skip reason = %q", got)
	}
	if got := items["Policy D"].SkipReason; got != "already complete" {
		t.Errorf("Policy D skip reason = %q", got)
	}
	if got := items["Policy E"].SkipReason; got != "no earlier completed assessment for this policy" {
		t.Errorf("Policy E skip reason = %q", got)
	}
}

func TestBuildCompletionPlanDefaultsToNewestGroup(t *testing.T) {
	s := &scope{AssessmentGroups: []*assessmentGroup{
		{Name: "Earlier", Assessments: []*assessment{{CreatedDateTime: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}},
		{Name: "Newest", Assessments: []*assessment{{CreatedDateTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}},
	}}

	plan, err := buildCompletionPlan(s, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.TargetGroup.Name; got != "Newest" {
		t.Fatalf("target group = %q, want Newest", got)
	}
}

func TestBuildCompletionPlanRestrictsSourceGroup(t *testing.T) {
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := created.Add(time.Hour)
	s := &scope{AssessmentGroups: []*assessmentGroup{
		{Name: "Requested", Assessments: []*assessment{{PolicyNodeID: "a", CreatedDateTime: created, LastModifiedDate: created, LastSession: &sessionReference{SessionID: "requested"}}}},
		{Name: "Newer", Assessments: []*assessment{{PolicyNodeID: "a", CreatedDateTime: created, LastModifiedDate: created.Add(time.Minute), LastSession: &sessionReference{SessionID: "newer"}}}},
		{Name: "Current", Assessments: []*assessment{{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: targetDate}}},
	}}

	plan, err := buildCompletionPlan(s, "Current", "Requested", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Items[0].Source.LastSession.SessionID; got != "requested" {
		t.Fatalf("source session = %q, want requested", got)
	}
}

func TestBuildCompletionPlanRetriesFailedSession(t *testing.T) {
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := created.Add(time.Hour)
	s := &scope{AssessmentGroups: []*assessmentGroup{
		{Name: "Previous", Assessments: []*assessment{{PolicyNodeID: "a", CreatedDateTime: created, LastModifiedDate: created, LastSession: &sessionReference{SessionID: "source"}}}},
		{Name: "Current", Assessments: []*assessment{{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: targetDate, IsActive: true, LastSession: &sessionReference{SessionID: "failed", Session: &session{ID: "failed", State: "failed"}}}}},
	}}

	plan, err := buildCompletionPlan(s, "Current", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].SkipReason != "" {
		t.Fatalf("skip reason = %q, want retry", plan.Items[0].SkipReason)
	}
	if got := plan.Items[0].Source.LastSession.SessionID; got != "source" {
		t.Fatalf("source session = %q, want source", got)
	}
}

func TestBuildCompletionPlanReconcilesOpenExistingSession(t *testing.T) {
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	targetDate := created.Add(time.Hour)
	existing := &session{ID: "current-session", State: "complete", WorkItems: []sessionWorkItem{{ItemType: "child", NodeID: "activity", WorkItemID: "123"}}}
	s := &scope{AssessmentGroups: []*assessmentGroup{
		{Name: "Previous", Assessments: []*assessment{{PolicyNodeID: "a", CreatedDateTime: created, LastModifiedDate: created, LastSession: &sessionReference{SessionID: "source"}}}},
		{Name: "Current", Assessments: []*assessment{{Name: "Policy A", PolicyNodeID: "a", CreatedDateTime: targetDate, LastSession: &sessionReference{SessionID: existing.ID, Session: existing}}}},
	}}

	plan, err := buildCompletionPlan(s, "Current", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].SkipReason != "" {
		t.Fatalf("skip reason = %q, want reconciliation", plan.Items[0].SkipReason)
	}
	if plan.Items[0].ExistingSession != existing {
		t.Fatal("existing session was not retained")
	}
}

func planItemsByName(items []completionPlanItem) map[string]completionPlanItem {
	result := make(map[string]completionPlanItem, len(items))
	for _, item := range items {
		result[item.Target.Name] = item
	}
	return result
}
