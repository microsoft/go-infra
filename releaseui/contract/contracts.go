// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package contract defines the boundary between the release UI and a release implementation.
package contract

import (
	"context"
	"encoding/json"
	"errors"
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

	// Prepare validates normalized browser input and returns a new run that has not started.
	Prepare(context.Context, json.RawMessage) (Run, error)

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

	// Inputs lists the browser-editable process inputs in display order.
	Inputs []Input `json:"inputs,omitempty"`

	// CanSimulate reports whether the UI offers a simulation command for this process.
	CanSimulate bool `json:"canSimulate"`
}

// Input describes one browser control.
type Input struct {
	// ID is the JSON object key accepted by Process.Prepare.
	ID string `json:"id"`

	// Type selects the browser control. The release UI currently accepts "choice" and "number".
	Type string `json:"type"`

	// Label names the control.
	Label string `json:"label"`

	// Description explains the value expected from the operator.
	Description string `json:"description,omitempty"`

	// Default is the value used when the browser omits the input.
	Default string `json:"default,omitempty"`

	// Placeholder is example text shown by an empty number input.
	Placeholder string `json:"placeholder,omitempty"`

	// Options lists the allowed values for a choice input.
	Options []InputOption `json:"options,omitempty"`

	// VisibleWhen shows this input only when another choice has the specified value.
	VisibleWhen *Condition `json:"visibleWhen,omitempty"`
}

// InputOption describes one allowed value for a choice input.
type InputOption struct {
	// Value is the JSON value sent to Process.Prepare.
	Value string `json:"value"`

	// Name is the option name shown to the operator.
	Name string `json:"name"`

	// Mark is a short label shown beside the option.
	Mark string `json:"mark,omitempty"`

	// Description explains the option.
	Description string `json:"description"`

	// NoticeTitle labels an optional notice shown when the option is selected.
	NoticeTitle string `json:"noticeTitle,omitempty"`

	// Notice explains constraints that apply to the selected option.
	Notice string `json:"notice,omitempty"`
}

// Condition controls whether an input is visible.
type Condition struct {
	// InputID identifies the choice input that controls visibility.
	InputID string `json:"inputId"`

	// Equals is the controlling value that makes the input visible.
	Equals string `json:"equals"`
}

// Plan contains the immutable data produced while preparing a run.
type Plan struct {
	// Test classifies the run as a test or dry run.
	Test bool

	// Input is the normalized browser input.
	Input json.RawMessage

	// Payload is process-specific immutable state needed to restore the run.
	Payload json.RawMessage

	// SessionID is the process-specific correlation identifier. The UI uses the intent digest when
	// the process leaves SessionID empty.
	SessionID string

	// Steps records the reviewed graph structure.
	Steps []Step

	// View contains the resolved plan shown before confirmation.
	View PlanView

	// Target identifies the fixed external target reviewed by the operator.
	Target Reference
}

// State is the durable, process-neutral state of one release run.
type State struct {
	// ProcessID identifies the Process that owns Input, Payload, and Checkpoint.
	ProcessID string `json:"processId"`

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

	// Steps records the reviewed graph structure.
	Steps []Step `json:"steps"`

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

// Step records one node in the reviewed execution graph.
type Step struct {
	// Name is the stable step identifier shown by the UI and referenced by DependsOn.
	Name string `json:"name"`

	// DependsOn lists prerequisite step names.
	DependsOn []string `json:"dependsOn,omitempty"`

	// Timeout limits one execution attempt.
	Timeout time.Duration `json:"timeout"`
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
