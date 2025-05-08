// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package appinsights provides an interface to submit telemetry to Application Insights,
// a component of Azure Monitor. This package calls the Classic API.
// See https://learn.microsoft.com/en-us/azure/azure-monitor/app/api-custom-events-metrics
package appinsights

const (
	sdkName         = "go-infra/telemetry"
	Version         = "v0.0.1"
	internalVersion = sdkName + ":" + Version
)
