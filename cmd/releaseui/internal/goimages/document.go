// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package goimages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

const (
	// CurrentSchemaVersion identifies documents that reconstruct their graph from domain state.
	CurrentSchemaVersion   = 9
	legacySchemaVersion    = 8
	legacyWorkflowRevision = 8
)

// Document is the durable, non-secret domain state of one Go-images release.
//
// Credentials and derived graph metadata are excluded. A schema-8 document may contain LegacyPlan
// so that the release UI can validate work items created before graph reconstruction.
type Document struct {
	SchemaVersion   int         `json:"schemaVersion"`
	ID              string      `json:"id"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
	Input           Input       `json:"input"`
	State           State       `json:"state"`
	LegacyPlan      *legacyPlan `json:"plan,omitempty"`
	ExecutionDigest string      `json:"executionDigest"`
}

type legacyPlan struct {
	WorkflowRevision int              `json:"workflowRevision"`
	Digest           string           `json:"digest"`
	Steps            []legacyPlanStep `json:"steps"`
}

type legacyPlanStep struct {
	Name         string   `json:"name"`
	DependsOn    []string `json:"dependsOn,omitempty"`
	TimeoutNanos int64    `json:"timeoutNanos"`
}

// NewDocument creates and validates a new document without storing derived graph metadata.
func NewDocument(input *Input, state *State, now time.Time) (*Document, error) {
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
	}
	document.ExecutionDigest, err = executionDigest(document.Input)
	if err != nil {
		return nil, err
	}
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return document, nil
}

// Validate checks the document schema and immutable execution identity.
func (d *Document) Validate() error {
	if d == nil {
		return errors.New("session document is nil")
	}
	if d.SchemaVersion != CurrentSchemaVersion && d.SchemaVersion != legacySchemaVersion {
		return fmt.Errorf("unsupported session schema version %d", d.SchemaVersion)
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
	if err := ValidateState(&d.Input, &d.State); err != nil {
		return fmt.Errorf("validate session state: %w", err)
	}

	var digest string
	var err error
	if d.SchemaVersion == legacySchemaVersion {
		if err := validateLegacyPlan(d.LegacyPlan); err != nil {
			return err
		}
		digest, err = legacyExecutionDigest(d.Input, *d.LegacyPlan)
	} else {
		if d.LegacyPlan != nil {
			return errors.New("current session unexpectedly contains a legacy plan")
		}
		digest, err = executionDigest(d.Input)
	}
	if err != nil {
		return err
	}
	if d.ExecutionDigest != digest {
		return fmt.Errorf("session execution digest mismatch: stored %q, calculated %q", d.ExecutionDigest, digest)
	}
	return nil
}

// ValidateGraph checks a reconstructed graph against schema-8 metadata when present.
func (d *Document) ValidateGraph(steps []*coordinator.Step) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.LegacyPlan == nil {
		return nil
	}
	current, err := newLegacyPlan(steps)
	if err != nil {
		return err
	}
	if current.Digest != d.LegacyPlan.Digest {
		return fmt.Errorf("release graph changed since session creation: stored digest %q, current digest %q", d.LegacyPlan.Digest, current.Digest)
	}
	return nil
}

func newLegacyPlan(steps []*coordinator.Step) (legacyPlan, error) {
	plan := legacyPlan{
		WorkflowRevision: legacyWorkflowRevision,
		Steps:            make([]legacyPlanStep, 0, len(steps)),
	}
	for _, step := range steps {
		if step == nil {
			return legacyPlan{}, errors.New("session plan contains a nil step")
		}
		entry := legacyPlanStep{
			Name: step.Name, DependsOn: make([]string, len(step.DependsOn)), TimeoutNanos: int64(step.Timeout),
		}
		for index, dependency := range step.DependsOn {
			if dependency == nil {
				return legacyPlan{}, fmt.Errorf("session step %q has a nil dependency", step.Name)
			}
			entry.DependsOn[index] = dependency.Name
		}
		plan.Steps = append(plan.Steps, entry)
	}
	if err := validateLegacyPlanSteps(plan.Steps); err != nil {
		return legacyPlan{}, err
	}
	digest, err := legacyPlanDigest(plan.Steps)
	if err != nil {
		return legacyPlan{}, err
	}
	plan.Digest = digest
	return plan, nil
}

func validateLegacyPlan(plan *legacyPlan) error {
	if plan == nil || len(plan.Steps) == 0 {
		return errors.New("legacy session plan has no steps")
	}
	if plan.WorkflowRevision != legacyWorkflowRevision {
		return fmt.Errorf("unsupported legacy workflow revision %d", plan.WorkflowRevision)
	}
	if err := validateLegacyPlanSteps(plan.Steps); err != nil {
		return err
	}
	digest, err := legacyPlanDigest(plan.Steps)
	if err != nil {
		return err
	}
	if plan.Digest != digest {
		return fmt.Errorf("legacy session plan digest mismatch: stored %q, calculated %q", plan.Digest, digest)
	}
	return nil
}

func validateLegacyPlanSteps(steps []legacyPlanStep) error {
	byName := make(map[string]legacyPlanStep, len(steps))
	for _, step := range steps {
		if step.Name == "" {
			return errors.New("session plan contains an empty step name")
		}
		if _, exists := byName[step.Name]; exists {
			return fmt.Errorf("session plan contains duplicate step name %q", step.Name)
		}
		byName[step.Name] = step
	}
	for _, step := range steps {
		dependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, exists := byName[dependency]; !exists {
				return fmt.Errorf("session step %q depends on unknown step %q", step.Name, dependency)
			}
			if _, exists := dependencies[dependency]; exists {
				return fmt.Errorf("session step %q repeats dependency %q", step.Name, dependency)
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
	visit = func(name string) error {
		switch visits[name] {
		case visiting:
			return fmt.Errorf("session plan contains a dependency cycle at %q", name)
		case visited:
			return nil
		}
		visits[name] = visiting
		for _, dependency := range byName[name].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visits[name] = visited
		return nil
	}
	for name := range byName {
		if visits[name] == unvisited {
			if err := visit(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithState returns a detached document containing the latest release domain state.
func (d *Document) WithState(state *State, now time.Time) (*Document, error) {
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

func legacyPlanDigest(steps []legacyPlanStep) (string, error) {
	data, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("marshal session plan: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func executionDigest(input Input) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal session execution identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func legacyExecutionDigest(input Input, plan legacyPlan) (string, error) {
	data, err := json.Marshal(struct {
		Input            Input  `json:"input"`
		PlanDigest       string `json:"planDigest"`
		WorkflowRevision int    `json:"workflowRevision"`
	}{
		Input: input, PlanDigest: plan.Digest, WorkflowRevision: plan.WorkflowRevision,
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
