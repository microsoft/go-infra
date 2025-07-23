// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package appinsights provides a client for sending arbitrary telemetry event
// data to Application Insights.
package appinsights

import (
	"context"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
	"github.com/microsoft/go-infra/telemetry/internal/telemetry"
)

// Config holds the configuration for the telemetry client.
type Config struct {
	// The instrumentation key used to identify the application.
	// This key is required and must be set before sending any telemetry.
	InstrumentationKey string

	// The endpoint URL to which telemetry will be sent.
	// If empty, it defaults to https://dc.services.visualstudio.com/v2/track.
	Endpoint string

	// Maximum number of telemetry items that can be submitted in each
	// request. If this many items are buffered, the buffer will be
	// flushed before MaxBatchInterval expires.
	// If zero, it defaults to 1024.
	MaxBatchSize int

	// Maximum time to wait before sending a batch of telemetry.
	// If zero, it defaults to 10 seconds.
	MaxBatchInterval time.Duration
}

// Start initializes telemetry using the specified configuration.
func Start(cfg Config) {
	telemetry.Init(&appinsights.Client{
		InstrumentationKey: cfg.InstrumentationKey,
		Endpoint:           cfg.Endpoint,
		MaxBatchSize:       cfg.MaxBatchSize,
		MaxBatchInterval:   cfg.MaxBatchInterval,
	})
}

// Close closes the telemetry client and flushes any remaining telemetry data.
// It should be called when the application is shutting down to ensure all
// telemetry data is sent before the program exits.
// It returns any errors that occurred during the upload process.
func Close(ctx context.Context) error {
	if telemetry.Client == nil {
		return nil
	}
	return telemetry.Client.Close(ctx)
}

// TrackEvent sends a telemetry event with the specified name and properties.
func TrackEvent(name string, properties map[string]string) {
	telemetry.Client.TrackEvent(name, properties)
}
