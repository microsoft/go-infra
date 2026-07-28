// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package session defines the durable, non-secret state of a release UI session.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/go-infra/cmd/releaseagent/internal/coordinator"
	"github.com/microsoft/go-infra/cmd/releaseagent/internal/releasesteps"
)

const (
	// CurrentSchemaVersion is the only session document schema understood by this version.
	CurrentSchemaVersion = 3
	// CurrentWorkflowRevision changes when step behavior becomes incompatible with saved state,
	// even if step IDs and dependencies have not changed.
	CurrentWorkflowRevision = 4
)

// Document is the durable, non-secret state needed to reconstruct a release plan.
//
// Credentials are deliberately excluded. They must be reacquired when the application starts.
type Document struct {
	SchemaVersion   int                `json:"schemaVersion"`
	ID              string             `json:"id"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
	Input           releasesteps.Input `json:"input"`
	State           releasesteps.State `json:"state"`
	Plan            Plan               `json:"plan"`
	ExecutionDigest string             `json:"executionDigest"`
}

// Plan is the persisted structural identity of a release DAG.
type Plan struct {
	WorkflowRevision int        `json:"workflowRevision"`
	Digest           string     `json:"digest"`
	Steps            []PlanStep `json:"steps"`
}

// PlanStep contains only properties that affect execution structure. Human-readable names are not
// included so copy changes do not invalidate an otherwise compatible session.
type PlanStep struct {
	ID           string   `json:"id"`
	DependsOn    []string `json:"dependsOn,omitempty"`
	TimeoutNanos int64    `json:"timeoutNanos"`
}

// NewDocument creates and validates a new durable session document.
func NewDocument(input *releasesteps.Input, state *releasesteps.State, steps []*coordinator.Step, now time.Time) (*Document, error) {
	if input == nil {
		return nil, errors.New("session input is nil")
	}
	if state == nil {
		return nil, errors.New("session state is nil")
	}
	if now.IsZero() {
		return nil, errors.New("session creation time is zero")
	}

	idBytes := make([]byte, 18)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate session ID: %w", err)
	}
	plan, err := NewPlan(steps)
	if err != nil {
		return nil, err
	}

	inputCopy, err := cloneJSON(*input)
	if err != nil {
		return nil, fmt.Errorf("copy session input: %w", err)
	}
	stateCopy, err := cloneJSON(*state)
	if err != nil {
		return nil, fmt.Errorf("copy session state: %w", err)
	}
	now = now.UTC()
	document := &Document{
		SchemaVersion: CurrentSchemaVersion,
		ID:            base64.RawURLEncoding.EncodeToString(idBytes),
		CreatedAt:     now,
		UpdatedAt:     now,
		Input:         inputCopy,
		State:         stateCopy,
		Plan:          plan,
	}
	document.ExecutionDigest, err = executionDigest(document.Input, document.Plan)
	if err != nil {
		return nil, err
	}
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return document, nil
}

// NewPlan creates a deterministic structural fingerprint of steps.
func NewPlan(steps []*coordinator.Step) (Plan, error) {
	if err := coordinator.ValidateSteps(steps); err != nil {
		return Plan{}, fmt.Errorf("validate session plan: %w", err)
	}
	plan := Plan{
		WorkflowRevision: CurrentWorkflowRevision,
		Steps:            make([]PlanStep, 0, len(steps)),
	}
	for _, step := range steps {
		planStep := PlanStep{
			ID:           step.ID,
			DependsOn:    make([]string, len(step.DependsOn)),
			TimeoutNanos: int64(step.Timeout),
		}
		for i, dependency := range step.DependsOn {
			planStep.DependsOn[i] = dependency.ID
		}
		plan.Steps = append(plan.Steps, planStep)
	}
	digest, err := planDigest(plan.Steps)
	if err != nil {
		return Plan{}, err
	}
	plan.Digest = digest
	return plan, nil
}

// Validate checks the document schema and internal structural fingerprint.
func (d *Document) Validate() error {
	if d == nil {
		return errors.New("session document is nil")
	}
	if d.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported session schema version %d, expected %d", d.SchemaVersion, CurrentSchemaVersion)
	}
	if d.ID == "" {
		return errors.New("session ID is empty")
	}
	if d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		return errors.New("session timestamps must be set")
	}
	if d.UpdatedAt.Before(d.CreatedAt) {
		return errors.New("session update time precedes creation time")
	}
	if len(d.Input.Versions) == 0 {
		return errors.New("session has no release versions")
	}
	if len(d.Plan.Steps) == 0 {
		return errors.New("session plan has no steps")
	}
	if d.Plan.WorkflowRevision != CurrentWorkflowRevision {
		return fmt.Errorf("unsupported workflow revision %d, expected %d", d.Plan.WorkflowRevision, CurrentWorkflowRevision)
	}
	if err := validatePlanSteps(d.Plan.Steps); err != nil {
		return err
	}
	digest, err := planDigest(d.Plan.Steps)
	if err != nil {
		return err
	}
	if d.Plan.Digest != digest {
		return fmt.Errorf("session plan digest mismatch: stored %q, calculated %q", d.Plan.Digest, digest)
	}
	executionDigest, err := executionDigest(d.Input, d.Plan)
	if err != nil {
		return err
	}
	if d.ExecutionDigest != executionDigest {
		return fmt.Errorf("session execution digest mismatch: stored %q, calculated %q", d.ExecutionDigest, executionDigest)
	}
	return nil
}

func validatePlanSteps(steps []PlanStep) error {
	byID := make(map[string]PlanStep, len(steps))
	for _, step := range steps {
		if step.ID == "" {
			return errors.New("session plan contains an empty step ID")
		}
		if _, exists := byID[step.ID]; exists {
			return fmt.Errorf("session plan contains duplicate step ID %q", step.ID)
		}
		byID[step.ID] = step
	}

	for _, step := range steps {
		dependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, exists := byID[dependency]; !exists {
				return fmt.Errorf("session step %q depends on unknown step %q", step.ID, dependency)
			}
			if _, exists := dependencies[dependency]; exists {
				return fmt.Errorf("session step %q repeats dependency %q", step.ID, dependency)
			}
			dependencies[dependency] = struct{}{}
		}
	}

	type visitState uint8
	const (
		unvisited visitState = iota
		visiting
		visited
	)
	visits := make(map[string]visitState, len(steps))
	var visit func(string) error
	visit = func(id string) error {
		switch visits[id] {
		case visiting:
			return fmt.Errorf("session plan contains a dependency cycle at %q", id)
		case visited:
			return nil
		}

		visits[id] = visiting
		for _, dependency := range byID[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visits[id] = visited
		return nil
	}
	for id := range byID {
		if visits[id] != unvisited {
			continue
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// MatchesPlan reports an error if current is structurally incompatible with the persisted plan.
func (d *Document) MatchesPlan(current Plan) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if err := validatePlanSteps(current.Steps); err != nil {
		return err
	}
	if current.WorkflowRevision != CurrentWorkflowRevision {
		return fmt.Errorf("current workflow revision %d is unsupported, expected %d", current.WorkflowRevision, CurrentWorkflowRevision)
	}
	digest, err := planDigest(current.Steps)
	if err != nil {
		return err
	}
	if current.Digest != digest {
		return fmt.Errorf("current release plan digest mismatch: stored %q, calculated %q", current.Digest, digest)
	}
	if current.Digest != d.Plan.Digest {
		return fmt.Errorf("release graph changed since session creation: stored digest %q, current digest %q", d.Plan.Digest, current.Digest)
	}
	return nil
}

// WithState returns a detached document containing the latest release domain state. The original
// document is unchanged, allowing callers to replace it only after durable storage succeeds.
func (d *Document) WithState(state *releasesteps.State, now time.Time) (*Document, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("updated release state is nil")
	}
	if now.IsZero() {
		return nil, errors.New("session update time is zero")
	}
	document, err := cloneJSON(*d)
	if err != nil {
		return nil, fmt.Errorf("copy session document: %w", err)
	}
	stateCopy, err := cloneJSON(*state)
	if err != nil {
		return nil, fmt.Errorf("copy updated release state: %w", err)
	}
	document.State = stateCopy
	document.UpdatedAt = now.UTC()
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return &document, nil
}

func planDigest(steps []PlanStep) (string, error) {
	data, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("marshal session plan: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func executionDigest(input releasesteps.Input, plan Plan) (string, error) {
	data, err := json.Marshal(struct {
		Input            releasesteps.Input `json:"input"`
		PlanDigest       string             `json:"planDigest"`
		WorkflowRevision int                `json:"workflowRevision"`
	}{
		Input:            input,
		PlanDigest:       plan.Digest,
		WorkflowRevision: plan.WorkflowRevision,
	})
	if err != nil {
		return "", fmt.Errorf("marshal session execution identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func cloneJSON[T any](value T) (T, error) {
	var clone T
	data, err := json.Marshal(value)
	if err != nil {
		return clone, err
	}
	if err := json.Unmarshal(data, &clone); err != nil {
		return clone, err
	}
	return clone, nil
}
