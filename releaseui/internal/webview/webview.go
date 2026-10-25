// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package webview defines the JSON models exchanged with the release UI browser.
package webview

import (
	"encoding/json"
	"time"
)

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

// PlanStep describes one step in a planned release.
type PlanStep struct {
	// Name identifies the step to the operator and in dependency lists.
	Name string `json:"name"`

	// DependsOn lists the names of steps that must finish before this step can run.
	DependsOn []string `json:"dependsOn,omitempty"`

	// Status is the step's current execution state.
	Status string `json:"status,omitempty"`
}

// PipelineRun summarizes the execution state of a release.
type PipelineRun struct {
	// Complete reports whether the release has reached a terminal state.
	Complete bool `json:"complete"`

	// Result distinguishes success, failure, cancellation, and an uncertain saved outcome.
	Result string `json:"result,omitempty"`
}

// ReleaseRecordReference identifies the durable record for a release.
type ReleaseRecordReference struct {
	// ID is the record's numeric identifier.
	ID int `json:"id"`

	// URL links to the record in its backing service.
	URL string `json:"url,omitempty"`

	// Revision identifies the record state reviewed before resuming.
	Revision int `json:"revision"`
}

// ExecutionResponse contains the state needed to confirm or monitor a release.
type ExecutionResponse struct {
	// PlanDigest identifies the exact plan that the operator reviewed.
	PlanDigest string `json:"planDigest,omitempty"`

	// Run summarizes the release execution state.
	Run PipelineRun `json:"run"`

	// RunURL links to the external run owned by the release process.
	RunURL string `json:"runUrl,omitempty"`

	// Record identifies the durable release record after the release is confirmed.
	Record *ReleaseRecordReference `json:"record,omitempty"`

	// CanResume permits an explicit confirmed resume of the selected saved checkpoint.
	CanResume bool `json:"canResume"`

	// Saved reports a non-executing saved run. Its external steps may have read-only observations.
	Saved bool `json:"saved"`
}

// DashboardResponse contains the release and process cards shown on the dashboard.
type DashboardResponse struct {
	// Ongoing contains releases that have not reached a terminal state.
	Ongoing []ReleaseSummary `json:"ongoing"`

	// NeedsAttention contains releases whose result requires operator review.
	NeedsAttention []ReleaseSummary `json:"needsAttention"`

	// Recent contains completed or closed releases kept for quick access.
	Recent []ReleaseSummary `json:"recent"`

	// Processes contains the configured release process choices.
	Processes []ProcessSummary `json:"processes"`

	// TrackingError reports a failure to load one or more durable release records.
	TrackingError string `json:"trackingError,omitempty"`
}

// ReleaseSummary describes one release card on the dashboard.
type ReleaseSummary struct {
	// Mark is the short process label shown on the card.
	Mark string `json:"mark"`

	// Name is the release process name.
	Name string `json:"name"`

	// Mode provides process-specific detail about the release.
	Mode string `json:"mode,omitempty"`

	// Status describes the release's current or final state.
	Status string `json:"status"`

	// SavedStatus is the last persisted status, separate from an external observation.
	SavedStatus string `json:"savedStatus,omitempty"`

	// ExternalStatus summarizes graph observations of known external operations, not release completion.
	ExternalStatus string `json:"externalStatus,omitempty"`

	// RunURL links to the process-owned pipeline or workflow.
	RunURL string `json:"runUrl,omitempty"`

	// Closed reports that the tracking record is history rather than an open action item.
	Closed bool `json:"closed,omitempty"`

	// UpdatedAt is the last known change to the release.
	UpdatedAt time.Time `json:"updatedAt,omitzero"`

	// Href links to the release process page.
	Href string `json:"href"`

	// RecordID is the durable record's numeric identifier.
	RecordID int `json:"recordId,omitempty"`

	// RecordURL links to the record in its backing service.
	RecordURL string `json:"recordUrl,omitempty"`

	// Issue makes a record read-only without hiding its metadata or external run link.
	Issue *ReleaseIssue `json:"issue,omitempty"`
}

// ReleaseIssue explains why a record cannot be opened for execution.
type ReleaseIssue struct {
	// RecordState is the state reported by the backing service, not the external run.
	RecordState string `json:"recordState,omitempty"`

	// Message explains the problem without requiring knowledge of the storage format.
	Message string `json:"message"`

	// Guidance describes safe next steps for the operator.
	Guidance string `json:"guidance"`

	// Detail preserves the underlying error for troubleshooting.
	Detail string `json:"detail"`
}

// ProcessSummary describes one release process card on the dashboard.
type ProcessSummary struct {
	// ID is the stable identifier of the process opened by the card.
	ID string `json:"id"`

	// Name is the process name shown to the operator.
	Name string `json:"name"`

	// Mark is the short process label shown on the card.
	Mark string `json:"mark"`

	// Description summarizes what the process releases.
	Description string `json:"description"`

	// Href links to the process page.
	Href string `json:"href"`
}

// ProcessDetail contains the process information and workflow shown on a process page.
type ProcessDetail struct {
	// ID is the stable identifier of the default workflow variant.
	ID string `json:"id"`

	// Name is the process name shown to the operator.
	Name string `json:"name"`

	// Mark is the short process label.
	Mark string `json:"mark"`

	// Description summarizes what the process releases.
	Description string `json:"description"`

	// DocumentationURL links to the release instructions.
	DocumentationURL string `json:"documentationUrl"`

	// Workflow describes the available release variants and their input forms.
	Workflow WorkflowDetail `json:"workflow"`
}

// WorkflowDetail describes the form used to prepare a release.
type WorkflowDetail struct {
	// Heading introduces the workflow to the operator.
	Heading string `json:"heading"`

	// SubmitLabel labels the button that prepares the default variant.
	SubmitLabel string `json:"submitLabel"`

	// Variants contains the release processes available within the workflow.
	Variants []WorkflowProcess `json:"variants"`
}

// WorkflowProcess describes one selectable release process variant.
type WorkflowProcess struct {
	// ID is the stable identifier submitted with the selected variant.
	ID string `json:"id"`

	// Name labels the variant in the workflow.
	Name string `json:"name"`

	// Description summarizes when to choose the variant.
	Description string `json:"description,omitempty"`

	// Preamble provides instructions that apply to the variant's input form.
	Preamble string `json:"preamble,omitempty"`

	// SubmitLabel labels the button that prepares this variant.
	SubmitLabel string `json:"submitLabel,omitempty"`

	// Inputs contains the browser controls for this variant.
	Inputs []Input `json:"inputs,omitempty"`

	// NoticeTitle labels an important variant-specific notice.
	NoticeTitle string `json:"noticeTitle,omitempty"`

	// Notice contains the notice text.
	Notice string `json:"notice,omitempty"`
}

// CheckStatus is the result of one preflight check.
type CheckStatus string

const (
	// CheckStatusPassed means the check found no readiness problem.
	CheckStatusPassed CheckStatus = "passed"

	// CheckStatusWarning means planning may continue but the operator should review the details.
	CheckStatusWarning CheckStatus = "warning"

	// CheckStatusUnavailable means the release cannot proceed.
	CheckStatusUnavailable CheckStatus = "unavailable"
)

// PreflightCheck describes one release-readiness check.
type PreflightCheck struct {
	// ID is the stable identifier of the check.
	ID string `json:"id"`

	// Name labels the check for the operator.
	Name string `json:"name"`

	// Status is the result of the check.
	Status CheckStatus `json:"status"`

	// Details explains the result.
	Details string `json:"details"`
}

// PreflightReport describes whether the selected process is ready to plan and execute.
type PreflightReport struct {
	// ExternalExecutionEnabled reports whether the prepared release may call external services.
	ExternalExecutionEnabled bool `json:"externalExecutionEnabled"`

	// PlanningEnabled reports whether the operator may prepare a release plan.
	PlanningEnabled bool `json:"planningEnabled"`

	// Checks contains the individual readiness results.
	Checks []PreflightCheck `json:"checks"`
}

// ProcessRunResponse contains the selected inputs, plan, and execution state for one release.
type ProcessRunResponse[P any] struct {
	// VariantID identifies the process variant that owns the release.
	VariantID string `json:"variantId"`

	// Input is the JSON object used to prepare the release.
	Input json.RawMessage `json:"input"`

	// Steps describes the planned execution graph.
	Steps []PlanStep `json:"steps"`

	// Execution contains confirmation and run state shared by all process types.
	Execution ExecutionResponse `json:"execution"`

	// Plan contains the process-specific plan shown for review.
	Plan P `json:"plan"`
}

// ReleaseStartRequest confirms that the operator reviewed a particular release plan.
type ReleaseStartRequest struct {
	// PlanDigest identifies the exact plan that the operator reviewed.
	PlanDigest string `json:"planDigest"`

	// Confirmed records the operator's approval of external actions.
	Confirmed bool `json:"confirmed"`

	// Revision is required when resuming an existing record.
	Revision int `json:"revision,omitempty"`
}

// ReleaseRecordExport is the portable representation of a durable release record.
type ReleaseRecordExport[R any] struct {
	// ID is the record's numeric identifier.
	ID int `json:"id"`

	// Revision is the optimistic concurrency revision of the record.
	Revision int `json:"revision"`

	// URL links to the record in its backing service.
	URL string `json:"url"`

	// Run contains the persisted release state.
	Run R `json:"run"`
}
