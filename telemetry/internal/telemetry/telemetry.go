package telemetry

import "github.com/microsoft/go-infra/telemetry/internal/appinsights"

// Client is the global telemetry client used to send telemetry data.
//
// It is kept in an internal package to prevent direct access
// from outside the telemetry package.
var Client *appinsights.Client
