// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

// CheckStatus is the outcome of a local readiness check.
type CheckStatus string

const (
	CheckStatusPassed      CheckStatus = "passed"
	CheckStatusWarning     CheckStatus = "warning"
	CheckStatusUnavailable CheckStatus = "unavailable"
)

// PreflightCheck describes one non-mutating local readiness check.
type PreflightCheck struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Details string      `json:"details"`
}

// PreflightReport describes local readiness without authenticating or contacting any service.
type PreflightReport struct {
	ExternalExecutionEnabled bool             `json:"externalExecutionEnabled"`
	PlanningEnabled          bool             `json:"planningEnabled"`
	Checks                   []PreflightCheck `json:"checks"`
}
