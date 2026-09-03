// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type scope struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	AssessmentGroups []*assessmentGroup `json:"assessmentGroups"`
}

type assessmentGroup struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	DueDate     *time.Time    `json:"dueDate"`
	Assessments []*assessment `json:"assessments"`
}

type assessment struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ScopeID           string            `json:"scopeId"`
	AssessmentGroupID string            `json:"assessmentGroupId"`
	IsActive          bool              `json:"isActive"`
	LastModifiedDate  time.Time         `json:"lastModifiedDate"`
	CreatedDateTime   time.Time         `json:"createdDateTime"`
	PolicyNodeID      string            `json:"policyNodeId"`
	PolicyName        string            `json:"policyName"`
	GraphVersion      *int64            `json:"graphVersion"`
	GraphID           *string           `json:"graphId"`
	Answers           []json.RawMessage `json:"answers"`
	LastSession       *sessionReference `json:"lastSession"`
	Raw               json.RawMessage   `json:"-"`
}

func (a *assessment) UnmarshalJSON(data []byte) error {
	type assessmentFields assessment

	var fields assessmentFields
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*a = assessment(fields)
	a.Raw = append(a.Raw[:0], data...)
	return nil
}

type sessionReference struct {
	SessionID string   `json:"sessionId"`
	Session   *session `json:"-"`
}

type completionPlan struct {
	TargetGroup *assessmentGroup
	Items       []completionPlanItem
}

type completionPlanItem struct {
	Target          *assessment
	Source          *assessment
	SourceGroup     *assessmentGroup
	ExistingSession *session
	SkipReason      string
}

func buildCompletionPlan(s *scope, targetGroupName, sourceGroupName string, overwrite bool) (*completionPlan, error) {
	if s == nil {
		return nil, errors.New("scope is nil")
	}

	targetGroup, err := selectTargetGroup(s.AssessmentGroups, targetGroupName)
	if err != nil {
		return nil, err
	}

	plan := &completionPlan{TargetGroup: targetGroup}
	for _, target := range targetGroup.Assessments {
		item := completionPlanItem{Target: target}
		var targetSession *session
		if target.LastSession != nil {
			targetSession = target.LastSession.Session
		}
		sessionState := ""
		if targetSession != nil {
			sessionState = strings.ToLower(targetSession.State)
		}
		failedSession := sessionState == "failed" || sessionState == "archived"
		switch {
		case target.LastSession != nil && targetSession == nil:
			item.SkipReason = "already complete"
		case sessionState == "complete" && !hasIncompleteChildWorkItems(targetSession):
			item.SkipReason = "already complete"
		case sessionState == "complete":
			item.ExistingSession = targetSession
			item.SourceGroup, item.Source = findSourceAssessment(s.AssessmentGroups, targetGroup, target, sourceGroupName)
			if item.Source == nil {
				item.SkipReason = "no earlier completed assessment for this policy"
			}
		case targetSession != nil && !failedSession:
			item.SkipReason = "completion session is " + sessionState
		case !failedSession && !overwrite && (target.IsActive || len(target.Answers) > 0):
			item.SkipReason = "already in progress"
		default:
			item.SourceGroup, item.Source = findSourceAssessment(s.AssessmentGroups, targetGroup, target, sourceGroupName)
			if item.Source == nil {
				item.SkipReason = "no earlier completed assessment for this policy"
			}
		}
		plan.Items = append(plan.Items, item)
	}

	slices.SortFunc(plan.Items, func(a, b completionPlanItem) int {
		return compareStrings(a.Target.Name, b.Target.Name)
	})
	return plan, nil
}

func hasIncompleteChildWorkItems(s *session) bool {
	for _, item := range s.WorkItems {
		if item.ItemType == "child" && !item.WorkItemState.IsCompletedCategory {
			return true
		}
	}
	return false
}

func selectTargetGroup(groups []*assessmentGroup, name string) (*assessmentGroup, error) {
	if name != "" {
		var match *assessmentGroup
		for _, group := range groups {
			if group.Name != name {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("multiple assessment groups named %q", name)
			}
			match = group
		}
		if match == nil {
			return nil, fmt.Errorf("assessment group %q not found", name)
		}
		return match, nil
	}

	var latest *assessmentGroup
	var latestCreated time.Time
	for _, group := range groups {
		for _, candidate := range group.Assessments {
			if latest == nil || candidate.CreatedDateTime.After(latestCreated) {
				latest = group
				latestCreated = candidate.CreatedDateTime
			}
		}
	}
	if latest == nil {
		return nil, errors.New("scope has no assessment groups")
	}
	return latest, nil
}

func findSourceAssessment(groups []*assessmentGroup, targetGroup *assessmentGroup, target *assessment, sourceGroupName string) (*assessmentGroup, *assessment) {
	var sourceGroup *assessmentGroup
	var source *assessment
	for _, group := range groups {
		if group == targetGroup || sourceGroupName != "" && group.Name != sourceGroupName {
			continue
		}
		for _, candidate := range group.Assessments {
			if candidate.PolicyNodeID != target.PolicyNodeID || candidate.LastSession == nil {
				continue
			}
			if !target.CreatedDateTime.IsZero() && !candidate.CreatedDateTime.Before(target.CreatedDateTime) {
				continue
			}
			if source == nil || candidate.LastModifiedDate.After(source.LastModifiedDate) {
				sourceGroup = group
				source = candidate
			}
		}
	}
	return sourceGroup, source
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
