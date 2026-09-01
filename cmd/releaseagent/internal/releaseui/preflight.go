// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
)

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

func (s *Server) preflightReport(ctx context.Context) PreflightReport {
	report := PreflightReport{
		ExternalExecutionEnabled: false,
		Checks: []PreflightCheck{
			{
				ID:      "loopback-server",
				Name:    "Loopback-only HTTP server",
				Status:  CheckStatusPassed,
				Details: "The release UI accepts requests only through a local loopback address.",
			},
		},
	}
	if s.sessionStore == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "durable-session",
			Name:    "Durable session storage",
			Status:  CheckStatusWarning,
			Details: "Not configured. Release plans cannot be persisted or restored.",
		})
	} else {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "durable-session",
			Name:    "Durable session storage",
			Status:  CheckStatusPassed,
			Details: "Enabled. The non-secret release plan is persisted atomically.",
		})
	}
	if s.readOnly == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "azure-read-only",
			Name:    "Pipeline 1023 read-only access",
			Status:  CheckStatusUnavailable,
			Details: "Disabled. No Azure DevOps request will be made.",
		})
	} else if details, err := s.readOnly.Preflight(ctx); err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "azure-read-only",
			Name:    "Pipeline 1023 read-only access",
			Status:  CheckStatusWarning,
			Details: err.Error(),
		})
	} else {
		report.PlanningEnabled = true
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "azure-read-only",
			Name:    "Pipeline 1023 read-only access",
			Status:  CheckStatusPassed,
			Details: details,
		})
	}
	if s.execution == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Go-images pipeline execution",
			Status:  CheckStatusUnavailable,
			Details: "Disabled. No Azure pipeline can be queued.",
		})
	} else if !report.PlanningEnabled {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Go-images pipeline execution",
			Status:  CheckStatusWarning,
			Details: "Unavailable until pipeline 1023 source validation passes preflight.",
		})
	} else {
		report.ExternalExecutionEnabled = true
	}
	return report
}
