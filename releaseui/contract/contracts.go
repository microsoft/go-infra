// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package contract defines the boundary between the release UI and a release implementation.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/go-infra/releaseui/coordinator"
)

// ErrInvalidInput marks an error caused by operator-controlled process input.
var ErrInvalidInput = errors.New("invalid release input")

type invalidInputError struct {
	err error
}

func (e *invalidInputError) Error() string {
	return e.err.Error()
}

func (e *invalidInputError) Unwrap() []error {
	return []error{ErrInvalidInput, e.err}
}

// InvalidInput marks err as an operator input error. The shared server returns these errors as bad
// requests. Other preparation errors are conflicts or service failures.
func InvalidInput(err error) error {
	return &invalidInputError{err: err}
}

// Process describes one kind of release, such as a Go-images test release.
//
// A Process validates browser input, prepares new runs, and restores persisted runs. Its methods
// must not mutate an external service. External mutations belong in the steps returned by Run.Steps.
type Process interface {
	// Definition returns the process metadata and input form shown by the release UI.
	Definition() Definition

	// Preflight performs non-mutating readiness checks.
	Preflight(context.Context) (Readiness, error)

	// Prepare validates one browser selection and returns a new run that has not started.
	Prepare(context.Context, Selection) (Run, error)

	// Restore validates persisted state and reconstructs its run.
	Restore(*State) (Run, error)
}

// Run represents one prepared or restored release.
type Run interface {
	// Snapshot returns an independent copy of the run's durable state.
	Snapshot() *State

	// Steps builds the executable graph for the run. A nil checkpoint callback builds a graph for
	// review or simulation. A non-nil callback lets real steps persist resumable state.
	Steps(context.Context, CheckpointFunc) ([]*coordinator.Step, error)
}

// CheckpointFunc persists process-specific state before execution continues.
type CheckpointFunc func(context.Context, Checkpoint) error

// Readiness reports whether a process can plan and execute releases.
type Readiness struct {
	// PlanningEnabled reports whether Prepare can resolve and validate a plan.
	PlanningEnabled bool

	// ExecutionEnabled reports whether a confirmed run can mutate its external service.
	ExecutionEnabled bool

	// Details explains the current readiness result to an operator.
	Details string
}

// Definition contains the process metadata and input form shown by the release UI.
type Definition struct {
	// ID is the stable machine-readable process identifier stored in work items and used in URLs.
	ID string

	// Name is the process name shown to an operator.
	Name string

	// Mark is the short process label shown on dashboard cards.
	Mark string

	// Description summarizes what the process releases.
	Description string

	// DocumentationURL links to the canonical HTTPS release instructions.
	DocumentationURL string

	// Workflow describes the process form and review action.
	Workflow Workflow
}

// Workflow describes the form and capabilities rendered by the shared process page.
type Workflow struct {
	// Heading labels the process input form.
	Heading string `json:"heading"`

	// Description adds context below Heading.
	Description string `json:"description,omitempty"`

	// SubmitLabel labels the command that prepares the release plan.
	SubmitLabel string `json:"submitLabel,omitempty"`

	// Variants lists the processes displayed within this group. The first variant is selected by
	// default.
	Variants []Variant `json:"variants"`

	// CanSimulate reports whether the UI offers a simulation command for this process.
	CanSimulate bool `json:"canSimulate"`
}

// Variant describes one selectable process within a display group.
type Variant struct {
	// ID is the stable value sent to Process.Prepare.
	ID string `json:"id"`

	// Name is the variant name shown in the process selector.
	Name string `json:"name"`

	// Description explains the variant.
	Description string `json:"description"`

	// Inputs lists only the fields used by this variant.
	Inputs []Input `json:"inputs,omitempty"`

	// NoticeTitle labels an optional notice shown for this variant.
	NoticeTitle string `json:"noticeTitle,omitempty"`

	// Notice explains constraints that apply to this variant.
	Notice string `json:"notice,omitempty"`
}

// Selection is one process variant and its browser input object.
type Selection struct {
	// VariantID identifies one variant from Definition.Workflow.Variants.
	VariantID string `json:"variantId"`

	// Input contains the selected variant's browser fields.
	Input json.RawMessage `json:"input"`
}

// Input describes one browser control.
type Input struct {
	// ID is the JSON object key accepted by Process.Prepare.
	ID string `json:"id"`

	// Type selects the browser control. The release UI currently accepts "number".
	Type string `json:"type"`

	// Label names the control.
	Label string `json:"label"`

	// Description explains the value expected from the operator.
	Description string `json:"description,omitempty"`

	// Placeholder is example text shown by an empty number input.
	Placeholder string `json:"placeholder,omitempty"`
}

// FieldOptions contains the display text for one bound input field.
type FieldOptions struct {
	// Label names the field.
	Label string

	// Description explains the value expected from the operator.
	Description string

	// Placeholder is example text shown by an empty field.
	Placeholder string
}

type inputBinding struct {
	definition Input
	set        func(json.RawMessage) error
}

// InputSet binds browser field IDs to Go fields and produces their UI definitions.
//
// Use one function to declare a variant's fields. Call Inputs while building Definition and Parse
// while preparing the variant. This keeps each browser ID beside the Go field it sets.
type InputSet struct {
	bindings []inputBinding
	err      error
}

// NewInputSet creates an empty input set.
func NewInputSet() *InputSet {
	return &InputSet{}
}

// PositiveIntVar binds id to target and renders it as a positive integer field.
func (s *InputSet) PositiveIntVar(target *int, id string, options FieldOptions) {
	if s.err != nil {
		return
	}
	if target == nil {
		s.err = fmt.Errorf("input %q has a nil target", id)
		return
	}
	for _, binding := range s.bindings {
		if binding.definition.ID == id {
			s.err = fmt.Errorf("input %q is bound more than once", id)
			return
		}
	}
	s.bindings = append(s.bindings, inputBinding{
		definition: Input{
			ID: id, Type: "number", Label: options.Label,
			Description: options.Description, Placeholder: options.Placeholder,
		},
		set: func(raw json.RawMessage) error {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("input %q must be a string", id)
			}
			number, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil || number == 0 || uint64(int(number)) != number {
				return fmt.Errorf("input %q must be a positive integer", id)
			}
			*target = int(number)
			return nil
		},
	})
}

// Inputs returns the UI definitions for the bound fields.
func (s *InputSet) Inputs() []Input {
	inputs := make([]Input, len(s.bindings))
	for index, binding := range s.bindings {
		inputs[index] = binding.definition
	}
	return inputs
}

// Parse binds one JSON object to the registered Go fields.
func (s *InputSet) Parse(data json.RawMessage) error {
	if s.err != nil {
		return s.err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var encoded map[string]json.RawMessage
	if err := decoder.Decode(&encoded); err != nil {
		return fmt.Errorf("decode process inputs: %w", err)
	}
	if encoded == nil {
		return errors.New("process inputs must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("process inputs must contain exactly one JSON value")
	}
	for _, binding := range s.bindings {
		raw, ok := encoded[binding.definition.ID]
		if !ok {
			return fmt.Errorf("input %q is required", binding.definition.ID)
		}
		if err := binding.set(raw); err != nil {
			return err
		}
		delete(encoded, binding.definition.ID)
	}
	if len(encoded) > 0 {
		unknown := make([]string, 0, len(encoded))
		for id := range encoded {
			unknown = append(unknown, id)
		}
		sort.Strings(unknown)
		return fmt.Errorf("unknown process input %q", unknown[0])
	}
	return nil
}

// Plan contains the immutable data produced while preparing a run.
type Plan struct {
	// VariantID identifies the selected process variant.
	VariantID string

	// Test classifies the run as a test or dry run.
	Test bool

	// Input is the normalized browser input.
	Input json.RawMessage

	// Payload is process-specific immutable state needed to restore the run.
	Payload json.RawMessage

	// SessionID is the process-specific correlation identifier. The UI uses the intent digest when
	// the process leaves SessionID empty.
	SessionID string

	// View contains the resolved plan shown before confirmation.
	View PlanView

	// Target identifies the fixed external target reviewed by the operator.
	Target Reference
}

// State is the durable, process-neutral state of one release run.
type State struct {
	// ProcessID identifies the Process that owns Input, Payload, and Checkpoint.
	ProcessID string `json:"processId"`

	// VariantID identifies the selected process variant.
	VariantID string `json:"variantId"`

	// Test classifies the run as a test or dry run.
	Test bool `json:"test,omitempty"`

	// Input is the normalized browser input used to prepare the run.
	Input json.RawMessage `json:"input"`

	// Payload is process-specific immutable state needed to validate and restore the run.
	Payload json.RawMessage `json:"payload"`

	// Digest identifies the immutable release intent.
	Digest string `json:"digest"`

	// SessionID is the process-specific correlation identifier.
	SessionID string `json:"sessionId"`

	// View contains the resolved plan shown to the operator.
	View PlanView `json:"view"`

	// Target identifies the fixed external target reviewed before confirmation.
	Target Reference `json:"target"`

	// External identifies the external run discovered after mutation.
	External *Reference `json:"external,omitempty"`

	// Checkpoint is process-specific resumable state.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`

	// Started reports whether the operator confirmed the run and the UI created its work item.
	Started bool `json:"started"`

	// Complete reports whether the run reached a terminal result.
	Complete bool `json:"complete"`

	// Result is empty while the run is incomplete. Terminal values are "succeeded", "failed",
	// "canceled", and "uncertain".
	Result string `json:"result,omitempty"`

	// UpdatedAt is the work-item update time used for display. It is not stored in the snapshot.
	UpdatedAt time.Time `json:"-"`
}

// Reference links to an external target or run.
type Reference struct {
	// ID is the target or run identifier shown by the UI.
	ID string `json:"id"`

	// URL is the HTTPS browser URL for the target or run.
	URL string `json:"url"`

	// LinkLabel labels the URL.
	LinkLabel string `json:"linkLabel"`

	// Status is the external service status, when known.
	Status string `json:"status,omitempty"`

	// Terminal reports whether the external service reached a terminal state.
	Terminal bool `json:"terminal,omitempty"`

	// Succeeded reports whether a terminal external run succeeded.
	Succeeded bool `json:"succeeded,omitempty"`
}

// Checkpoint contains process-specific resumable state and process-neutral display data.
type Checkpoint struct {
	// State is process-specific resumable JSON.
	State json.RawMessage

	// External identifies the external run, when one has been discovered.
	External *Reference

	// Progress describes the current external action for the live UI.
	Progress Progress
}

// Progress describes process-neutral progress for one active step.
type Progress struct {
	// Summary is the short status shown by the UI.
	Summary string

	// Detail adds process-specific status information.
	Detail string

	// Completed is the number of completed units, when the process reports bounded progress.
	Completed int

	// Total is the total number of units, when known.
	Total int
}

// PlanView contains process-neutral display data for the review page.
type PlanView struct {
	// Subtitle summarizes the process mode, target, or graph size.
	Subtitle string `json:"subtitle"`

	// IntentTitle names the exact release intent.
	IntentTitle string `json:"intentTitle"`

	// IntentBadge is a short intent classification shown beside IntentTitle.
	IntentBadge string `json:"intentBadge,omitempty"`

	// Facts lists resolved values used to review the intent.
	Facts []PlanFact `json:"facts,omitempty"`

	// Request previews the locked external request.
	Request *RequestPreview `json:"request,omitempty"`

	// ExecutionTitle labels the confirmation section.
	ExecutionTitle string `json:"executionTitle,omitempty"`

	// ExecutionWarning explains the effect of confirming the run.
	ExecutionWarning string `json:"executionWarning,omitempty"`

	// ExecutionConfirmation is the exact confirmation statement shown to the operator.
	ExecutionConfirmation string `json:"executionConfirmation,omitempty"`

	// ExecutionButtonLabel labels the final command that starts the run.
	ExecutionButtonLabel string `json:"executionButtonLabel,omitempty"`
}

// PlanFact is one resolved value shown while reviewing a plan.
type PlanFact struct {
	// Label names the fact.
	Label string `json:"label"`

	// Value is the primary resolved value.
	Value string `json:"value"`

	// Detail adds supporting information.
	Detail string `json:"detail,omitempty"`

	// Href links to the source of the fact.
	Href string `json:"href,omitempty"`
}

// RequestPreview describes the locked external request without sending it.
type RequestPreview struct {
	// Eyebrow identifies the external service or request state.
	Eyebrow string `json:"eyebrow"`

	// Title names the external operation.
	Title string `json:"title"`

	// Target names the fixed external repository, project, or pipeline.
	Target string `json:"target,omitempty"`

	// Fields lists the locked request values.
	Fields []RequestField `json:"fields,omitempty"`
}

// RequestField is one name and value in an external request preview.
type RequestField struct {
	// Name identifies the external request field.
	Name string `json:"name"`

	// Value is the locked field value.
	Value string `json:"value"`
}
