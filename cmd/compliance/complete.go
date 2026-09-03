// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

type complianceAPI interface {
	getAssessmentWork(context.Context, *assessment, []json.RawMessage) (*assessmentWork, error)
	getPolicyQuestions(context.Context, string, int64) ([]question, error)
	writeAssessment(context.Context, *assessment, []json.RawMessage) (*assessment, error)
	getSession(context.Context, string) (*session, error)
	getLatestSession(context.Context, string) (*session, error)
	generateSession(context.Context, string, map[string]json.RawMessage) error
	setWorkItemState(context.Context, string, string, string) error
}

type completionOptions struct {
	Apply              bool
	AnswersOnly        bool
	AllowPartial       bool
	AnswerOverrides    map[string][]json.RawMessage
	CompleteActivities map[string]map[string]struct{}
	PollInterval       time.Duration
}

type completionResult struct {
	AssessmentName string
	SourceGroup    string
	CopiedAnswers  int
	DroppedAnswers int
	QuestionCount  int
	WorkCount      int
	WorkItemCount  int
	SessionID      string
	Status         string
	Err            error
}

func runCompletionPlan(ctx context.Context, api complianceAPI, s *scope, plan *completionPlan, options completionOptions) ([]completionResult, error) {
	results := make([]completionResult, 0, len(plan.Items))
	var resultErrors []error

	for _, item := range plan.Items {
		result := completionResult{AssessmentName: item.Target.Name}
		if item.SourceGroup != nil {
			result.SourceGroup = item.SourceGroup.Name
		}
		if item.SkipReason != "" {
			result.Status = "skipped: " + item.SkipReason
			results = append(results, result)
			continue
		}
		if item.ExistingSession != nil {
			result.WorkItemCount = childWorkItemCount(item.ExistingSession)
			sourceSession, err := api.getSession(ctx, item.Source.LastSession.SessionID)
			if err != nil {
				result.Status = "failed"
				result.Err = fmt.Errorf("read source completion session: %w", err)
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
			closures, err := planWorkItemClosures(sourceSession, item.ExistingSession, options.CompleteActivities[item.Target.Name])
			if err != nil {
				result.Status = "needs review"
				result.Err = err
				if options.Apply {
					resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
				}
				results = append(results, result)
				continue
			}
			if options.AnswersOnly {
				result.Status = "skipped: completion session already generated"
				results = append(results, result)
				continue
			}
			if !options.Apply {
				result.Status = fmt.Sprintf("ready to complete %d work items", len(closures))
				results = append(results, result)
				continue
			}
			completed, err := applyWorkItemClosures(ctx, api, item.ExistingSession, closures, options.PollInterval)
			if err != nil {
				result.Status = "work-item completion failed"
				result.Err = err
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
				results = append(results, result)
				continue
			}
			result.SessionID = completed.ID
			result.WorkItemCount = childWorkItemCount(completed)
			result.Status = "complete"
			results = append(results, result)
			continue
		}

		candidateAnswers := item.Source.Answers
		explicitAnswers := false
		if override, ok := options.AnswerOverrides[item.Target.Name]; ok {
			candidateAnswers = override
			explicitAnswers = true
		}
		work, err := api.getAssessmentWork(ctx, item.Target, candidateAnswers)
		if err != nil {
			result.Status = "failed"
			result.Err = fmt.Errorf("retrieve current assessment work: %w", err)
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
			results = append(results, result)
			continue
		}

		var sourceQuestions []question
		if explicitAnswers {
			if err := validateExplicitAnswers(candidateAnswers, work.Questions); err != nil {
				result.Status = "failed"
				result.Err = fmt.Errorf("validate answer override: %w", err)
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
		}
		compareQuestionDetails := !explicitAnswers && !sameGraphVersion(item.Source.GraphVersion, item.Target.GraphVersion)
		if compareQuestionDetails && len(item.Source.Answers) > 0 {
			if item.Source.GraphVersion == nil {
				result.Status = "failed"
				result.Err = errors.New("source assessment has answers but no graph version")
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
			sourceQuestions, err = api.getPolicyQuestions(ctx, policyIdentifier(item.Source), *item.Source.GraphVersion)
			if err != nil {
				result.Status = "failed"
				result.Err = fmt.Errorf("read source questionnaire: %w", err)
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
		}

		answers, dropped, err := filterAnswersForQuestions(candidateAnswers, sourceQuestions, work.Questions, compareQuestionDetails)
		if err != nil {
			result.Status = "failed"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		if dropped > 0 {
			work, err = api.getAssessmentWork(ctx, item.Target, answers)
			if err != nil {
				result.Status = "failed"
				result.Err = fmt.Errorf("recompute assessment work with compatible answers: %w", err)
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
		}
		result.CopiedAnswers = len(answers)
		result.DroppedAnswers = dropped
		result.QuestionCount = len(work.Questions)
		result.WorkCount = len(work.Work)
		if err := validateCompleteActivities(options.CompleteActivities[item.Target.Name], work.Work); err != nil {
			result.Status = "failed"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		if dropped > 0 && !options.AllowPartial {
			result.Status = "needs review"
			result.Err = fmt.Errorf("%d previous answers are incompatible with the current questionnaire", dropped)
			if options.Apply {
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
			}
			results = append(results, result)
			continue
		}

		var sourceSession *session
		var generationPayload map[string]json.RawMessage
		if !options.AnswersOnly {
			sourceSession, err = api.getSession(ctx, item.Source.LastSession.SessionID)
			if err != nil {
				result.Status = "failed"
				result.Err = fmt.Errorf("read source completion session: %w", err)
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
				results = append(results, result)
				continue
			}
			generationPayload, err = makeSessionGenerationPayload(sourceSession, s, plan.TargetGroup, item.Target)
			if err != nil {
				result.Status = "failed"
				result.Err = err
				resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
				results = append(results, result)
				continue
			}
		}

		if !options.Apply {
			result.Status = "ready"
			results = append(results, result)
			continue
		}

		saved, err := api.writeAssessment(ctx, item.Target, answers)
		if err != nil {
			result.Status = "failed"
			result.Err = fmt.Errorf("save assessment: %w", err)
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
			results = append(results, result)
			continue
		}
		if options.AnswersOnly {
			result.Status = "answers saved"
			results = append(results, result)
			continue
		}

		previousSessionID := ""
		if item.Target.LastSession != nil {
			previousSessionID = item.Target.LastSession.SessionID
		}
		if err := api.generateSession(ctx, saved.ID, generationPayload); err != nil {
			result.Status = "answers saved; session failed"
			result.Err = fmt.Errorf("generate completion session: %w", err)
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, result.Err))
			results = append(results, result)
			continue
		}
		generated, err := waitForNewSession(ctx, api, saved.ID, previousSessionID, options.PollInterval)
		if err != nil {
			result.Status = "answers saved; session failed"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		completed, err := waitForSession(ctx, api, generated, options.PollInterval)
		if err != nil {
			result.Status = "answers saved; session failed"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		result.SessionID = completed.ID
		result.WorkItemCount = childWorkItemCount(completed)
		closures, err := planWorkItemClosures(sourceSession, completed, options.CompleteActivities[item.Target.Name])
		if err != nil {
			result.Status = "session generated; work-item completion needs review"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		completed, err = applyWorkItemClosures(ctx, api, completed, closures, options.PollInterval)
		if err != nil {
			result.Status = "session generated; work-item completion failed"
			result.Err = err
			resultErrors = append(resultErrors, fmt.Errorf("%s: %w", item.Target.Name, err))
			results = append(results, result)
			continue
		}
		result.WorkItemCount = childWorkItemCount(completed)
		result.Status = "complete"
		results = append(results, result)
	}

	return results, errors.Join(resultErrors...)
}

func validateExplicitAnswers(answers []json.RawMessage, questions []question) error {
	questionByID := make(map[string]question, len(questions))
	for _, question := range questions {
		if question.NodeID == "" {
			return errors.New("questionnaire contains a question with no node ID")
		}
		questionByID[question.NodeID] = question
	}

	answered := make(map[string]struct{}, len(answers))
	for _, raw := range answers {
		var answer struct {
			QuestionID string   `json:"questionId"`
			Answers    []string `json:"answers"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			return fmt.Errorf("decode answer override: %w", err)
		}
		if answer.QuestionID == "" {
			return errors.New("answer override has no question ID")
		}
		if _, duplicate := answered[answer.QuestionID]; duplicate {
			return fmt.Errorf("answer override contains duplicate question ID %q", answer.QuestionID)
		}
		question, ok := questionByID[answer.QuestionID]
		if !ok {
			return fmt.Errorf("answer override contains unknown question ID %q", answer.QuestionID)
		}
		if len(answer.Answers) == 0 {
			return fmt.Errorf("answer override for question %q is empty", answer.QuestionID)
		}

		var fields struct {
			AnswerTemplate struct {
				Options []struct {
					Value string `json:"value"`
				} `json:"options"`
			} `json:"answerTemplate"`
		}
		if err := json.Unmarshal(question.Raw, &fields); err != nil {
			return fmt.Errorf("decode question %q answer options: %w", answer.QuestionID, err)
		}
		if len(fields.AnswerTemplate.Options) > 0 {
			allowed := make(map[string]struct{}, len(fields.AnswerTemplate.Options))
			for _, option := range fields.AnswerTemplate.Options {
				allowed[option.Value] = struct{}{}
			}
			for _, value := range answer.Answers {
				if _, ok := allowed[value]; !ok {
					return fmt.Errorf("answer %q is not allowed for question %q", value, answer.QuestionID)
				}
			}
		}
		answered[answer.QuestionID] = struct{}{}
	}

	var missing []string
	for questionID := range questionByID {
		if _, ok := answered[questionID]; !ok {
			missing = append(missing, questionID)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("answer override is missing question IDs: %s", strings.Join(missing, ", "))
	}
	return nil
}

type workItemClosure struct {
	WorkItemID string
	State      string
}

func planWorkItemClosures(source, target *session, approvedCurrentNodes map[string]struct{}) ([]workItemClosure, error) {
	if source == nil || !strings.EqualFold(source.State, "complete") {
		return nil, errors.New("source completion session is not complete")
	}
	if target == nil || !strings.EqualFold(target.State, "complete") {
		return nil, errors.New("target generation session is not complete")
	}

	sourceByNode := make(map[string]sessionWorkItem)
	normalizedApprovals := make(map[string]string, len(approvedCurrentNodes))
	for nodeID := range approvedCurrentNodes {
		normalizedNodeID := normalizeActivityNodeID(nodeID)
		if previous, exists := normalizedApprovals[normalizedNodeID]; exists {
			return nil, fmt.Errorf("approved current nodes %q and %q are duplicates", previous, nodeID)
		}
		normalizedApprovals[normalizedNodeID] = nodeID
	}
	completedState := ""
	for _, item := range source.WorkItems {
		if item.ItemType != "child" {
			continue
		}
		if item.NodeID == "" {
			return nil, errors.New("source completion session contains a child with no node ID")
		}
		nodeID := normalizeActivityNodeID(item.NodeID)
		if _, exists := sourceByNode[nodeID]; exists {
			return nil, fmt.Errorf("source completion session contains duplicate child node %q", item.NodeID)
		}
		sourceByNode[nodeID] = item
		if item.WorkItemState.IsCompletedCategory && item.WorkItemState.State != "" {
			if completedState == "" {
				completedState = item.WorkItemState.State
			} else if !strings.EqualFold(completedState, item.WorkItemState.State) {
				completedState = ""
			}
		}
	}

	var closures []workItemClosure
	usedApprovals := make(map[string]struct{})
	for _, item := range target.WorkItems {
		if item.ItemType != "child" || item.WorkItemState.IsCompletedCategory {
			continue
		}
		sourceItem, ok := sourceByNode[normalizeActivityNodeID(item.NodeID)]
		state := ""
		if ok {
			if !sourceItem.WorkItemState.IsCompletedCategory {
				return nil, fmt.Errorf("source child node %q was not completed", item.NodeID)
			}
			state = sourceItem.WorkItemState.State
		} else if _, approved := normalizedApprovals[normalizeActivityNodeID(item.NodeID)]; approved {
			if completedState == "" {
				return nil, fmt.Errorf("cannot determine a single completed state for approved current node %q", item.NodeID)
			}
			state = completedState
			usedApprovals[normalizeActivityNodeID(item.NodeID)] = struct{}{}
		} else {
			return nil, fmt.Errorf("generated child node %q has no matching source work item", item.NodeID)
		}
		if item.WorkItemID == "" {
			return nil, fmt.Errorf("generated child node %q has no work item ID", item.NodeID)
		}
		if state == "" {
			return nil, fmt.Errorf("source child node %q has no work item state", item.NodeID)
		}
		closures = append(closures, workItemClosure{WorkItemID: item.WorkItemID, State: state})
	}
	for normalizedNodeID, originalNodeID := range normalizedApprovals {
		if _, used := usedApprovals[normalizedNodeID]; !used {
			return nil, fmt.Errorf("approved current node %q is not an open generated child", originalNodeID)
		}
	}
	if len(closures) > 0 && target.ServerURL == "" {
		return nil, errors.New("target generation session has no work item server URL")
	}
	return closures, nil
}

func validateCompleteActivities(approved map[string]struct{}, work []json.RawMessage) error {
	if len(approved) == 0 {
		return nil
	}
	available := make(map[string]struct{}, len(work))
	for _, raw := range work {
		var activity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &activity); err != nil {
			return fmt.Errorf("decode assessment activity: %w", err)
		}
		if activity.ID != "" {
			available[activity.ID] = struct{}{}
		}
	}
	for nodeID := range approved {
		if _, ok := available[nodeID]; !ok {
			return fmt.Errorf("approved current node %q is not in the assessment work", nodeID)
		}
	}
	return nil
}

func normalizeActivityNodeID(nodeID string) string {
	return strings.TrimPrefix(strings.TrimSpace(nodeID), "Activity ")
}

func applyWorkItemClosures(ctx context.Context, api complianceAPI, target *session, closures []workItemClosure, pollInterval time.Duration) (*session, error) {
	for _, closure := range closures {
		if err := api.setWorkItemState(ctx, target.ServerURL, closure.WorkItemID, closure.State); err != nil {
			return nil, err
		}
	}
	if len(closures) == 0 {
		return target, nil
	}
	return waitForChildWorkItemsComplete(ctx, api, target.ID, pollInterval)
}

func waitForChildWorkItemsComplete(ctx context.Context, api complianceAPI, sessionID string, pollInterval time.Duration) (*session, error) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	for {
		current, err := api.getSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("refresh work items for completion session %s: %w", sessionID, err)
		}
		if current == nil {
			return nil, fmt.Errorf("refresh work items for completion session %s: empty response", sessionID)
		}
		if !hasIncompleteChildWorkItems(current) {
			return current, nil
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("wait for child work items in session %s: %w", sessionID, ctx.Err())
		case <-timer.C:
		}
	}
}

func childWorkItemCount(s *session) int {
	count := 0
	for _, item := range s.WorkItems {
		if item.ItemType == "child" {
			count++
		}
	}
	return count
}

func filterAnswersForQuestions(answers []json.RawMessage, sourceQuestions, currentQuestions []question, compareDetails bool) ([]json.RawMessage, int, error) {
	currentQuestionByID := make(map[string]question, len(currentQuestions))
	for _, question := range currentQuestions {
		if question.NodeID == "" {
			return nil, 0, errors.New("current questionnaire contains a question with no node ID")
		}
		currentQuestionByID[question.NodeID] = question
	}
	sourceQuestionByID := make(map[string]question, len(sourceQuestions))
	for _, question := range sourceQuestions {
		if question.NodeID != "" {
			sourceQuestionByID[question.NodeID] = question
		}
	}

	filtered := make([]json.RawMessage, 0, len(answers))
	dropped := 0
	for _, answer := range answers {
		var fields struct {
			QuestionID string `json:"questionId"`
		}
		if err := json.Unmarshal(answer, &fields); err != nil {
			return nil, 0, fmt.Errorf("decode previous answer: %w", err)
		}
		if fields.QuestionID == "" {
			return nil, 0, errors.New("previous answer has no question ID")
		}
		currentQuestion, ok := currentQuestionByID[fields.QuestionID]
		if !ok {
			dropped++
			continue
		}
		if compareDetails {
			sourceQuestion, ok := sourceQuestionByID[fields.QuestionID]
			if !ok {
				dropped++
				continue
			}
			equal, err := equalQuestionDetails(sourceQuestion, currentQuestion)
			if err != nil {
				return nil, 0, err
			}
			if !equal {
				dropped++
				continue
			}
		}
		filtered = append(filtered, answer)
	}
	return filtered, dropped, nil
}

type questionDetails struct {
	AnswerTemplate        any `json:"answerTemplate"`
	AutoApplicableAnswers any `json:"autoApplicableAnswers"`
	Question              any `json:"question"`
	QuestionPlainText     any `json:"questionPlainText"`
	QuestionRaw           any `json:"questionRaw"`
}

func equalQuestionDetails(a, b question) (bool, error) {
	decode := func(value question) (questionDetails, error) {
		var details questionDetails
		if err := json.Unmarshal(value.Raw, &details); err != nil {
			return questionDetails{}, fmt.Errorf("decode question %q details: %w", value.NodeID, err)
		}
		return details, nil
	}
	aDetails, err := decode(a)
	if err != nil {
		return false, err
	}
	bDetails, err := decode(b)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(aDetails, bDetails), nil
}

func sameGraphVersion(a, b *int64) bool {
	return a != nil && b != nil && *a == *b
}

func policyIdentifier(a *assessment) string {
	if a.GraphID == nil || *a.GraphID == "" {
		return a.PolicyNodeID
	}
	return *a.GraphID + "/" + a.PolicyNodeID
}

func makeSessionGenerationPayload(source *session, s *scope, targetGroup *assessmentGroup, target *assessment) (map[string]json.RawMessage, error) {
	if !strings.EqualFold(source.State, "complete") {
		return nil, fmt.Errorf("source session %q is not complete", source.ID)
	}
	if source.ProjectID == "" || source.WITConfigID == "" || source.AreaPath == "" || source.IterationPath == "" {
		return nil, fmt.Errorf("source session %q has incomplete work-item configuration", source.ID)
	}
	areaRoot := classificationRoot(source.AreaPath)
	iterationRoot := classificationRoot(source.IterationPath)
	if areaRoot == "" || iterationRoot == "" || !strings.EqualFold(areaRoot, iterationRoot) {
		return nil, fmt.Errorf("source session %q has inconsistent area and iteration projects", source.ID)
	}

	values := map[string]any{
		"areaPath":         areaRoot,
		"assignedTo":       nil,
		"iterationPath":    iterationRoot,
		"linkedWorkItem":   nil,
		"projectId":        source.ProjectID,
		"relatedWorkItems": []any{},
		"serverUrl":        nil,
		"witConfigId":      source.WITConfigID,
		"workItemTags":     []any{},
		"name":             fmt.Sprintf("%s for %s of %s", target.Name, targetGroup.Name, s.Name),
	}
	payload := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode completion session field %q: %w", key, err)
		}
		payload[key] = encoded
	}
	return payload, nil
}

func classificationRoot(path string) string {
	path = strings.Trim(path, "\\")
	root, _, _ := strings.Cut(path, "\\")
	return root
}

func waitForNewSession(ctx context.Context, api complianceAPI, assessmentID, previousSessionID string, pollInterval time.Duration) (*session, error) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	for {
		current, err := api.getLatestSession(ctx, assessmentID)
		if err != nil {
			return nil, fmt.Errorf("find generated completion session for assessment %s: %w", assessmentID, err)
		}
		if current != nil && current.ID != "" {
			state := strings.ToLower(current.State)
			if current.ID != previousSessionID || state != "failed" && state != "archived" {
				return current, nil
			}
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("find generated completion session for assessment %s: %w", assessmentID, ctx.Err())
		case <-timer.C:
		}
	}
}

func waitForSession(ctx context.Context, api complianceAPI, current *session, pollInterval time.Duration) (*session, error) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	for {
		if current == nil {
			return nil, errors.New("completion session response is empty")
		}
		if current.ID == "" {
			return nil, errors.New("completion session response has no ID")
		}
		switch strings.ToLower(current.State) {
		case "complete":
			return current, nil
		case "failed":
			if current.FailureMessage != "" {
				return nil, fmt.Errorf("completion session %s failed: %s", current.ID, current.FailureMessage)
			}
			return nil, fmt.Errorf("completion session %s failed", current.ID)
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("wait for completion session %s: %w", current.ID, ctx.Err())
		case <-timer.C:
		}
		sessionID := current.ID
		refreshed, err := api.getSession(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("refresh completion session %s: %w", sessionID, err)
		}
		current = refreshed
	}
}
