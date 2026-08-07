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
	AzureReadOnlyEnabled     bool             `json:"azureReadOnlyEnabled"`
	GoImagesExecutionEnabled bool             `json:"goImagesExecutionEnabled"`
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
		report.AzureReadOnlyEnabled = true
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
	} else if !report.AzureReadOnlyEnabled {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Go-images pipeline execution",
			Status:  CheckStatusWarning,
			Details: "Unavailable until pipeline 1023 source validation passes preflight.",
		})
	} else if details, err := s.execution.Preflight(ctx); err != nil {
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Go-images pipeline execution",
			Status:  CheckStatusWarning,
			Details: err.Error(),
		})
	} else {
		report.ExternalExecutionEnabled = true
		report.GoImagesExecutionEnabled = true
		report.Checks = append(report.Checks, PreflightCheck{
			ID:      "external-execution",
			Name:    "Go-images pipeline 1023 execution",
			Status:  CheckStatusPassed,
			Details: details,
		})
	}
	return report
}

func defaultExecutableLookup(name string) (string, error) {
	return exec.LookPath(name)
}
