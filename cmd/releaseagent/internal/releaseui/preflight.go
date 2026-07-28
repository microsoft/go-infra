// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package releaseui

import (
	"context"
	"os/exec"
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
	Checks                   []PreflightCheck `json:"checks"`
}

type executableLookup func(string) (string, error)

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
			Details: "Disabled. Start with -session-file to persist and restore the release plan.",
		})
	} else {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "durable-session",
			Name:    "Durable session storage",
			Status:  CheckStatusPassed,
			Details: "Enabled. The non-secret release plan is persisted atomically.",
		})
	}
	for _, command := range []struct {
		id         string
		name       string
		executable string
	}{
		{id: "github-cli", name: "GitHub CLI (gh)", executable: "gh"},
		{id: "azure-cli", name: "Azure CLI (az)", executable: "az"},
	} {
		path, err := s.lookPath(command.executable)
		if err != nil {
			report.Checks = append(report.Checks, PreflightCheck{
				ID:      command.id,
				Name:    command.name,
				Status:  CheckStatusWarning,
				Details: "Executable not found in PATH. Authentication was not attempted.",
			})
			continue
		}
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      command.id,
			Name:    command.name,
			Status:  CheckStatusPassed,
			Details: "Found at " + path + ". Authentication was not attempted.",
		})
	}
	if s.smoke == nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "External release execution",
			Status:  CheckStatusUnavailable,
			Details: "Disabled. No GitHub, Azure DevOps, or publishing operation can be started.",
		})
		return report
	}
	details, err := s.smoke.Preflight(ctx)
	if err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Pipeline 1151 smoke execution",
			Status:  CheckStatusWarning,
			Details: err.Error(),
		})
		return report
	}
	report.ExternalExecutionEnabled = true
	report.Checks = append(report.Checks, PreflightCheck{
		ID:      "external-execution",
		Name:    "Pipeline 1151 smoke execution",
		Status:  CheckStatusPassed,
		Details: details,
	})
	return report
}

func defaultExecutableLookup(name string) (string, error) {
	return exec.LookPath(name)
}
