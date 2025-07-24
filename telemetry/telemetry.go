// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights"
	"github.com/microsoft/go-infra/telemetry/internal/config"
	"github.com/microsoft/go-infra/telemetry/internal/telemetry"
)

// Config holds the configuration for the telemetry client.
type Config struct {
	// The instrumentation key used to identify the application.
	// This key is required and must be set before sending any telemetry.
	InstrumentationKey string

	// UploadConfig is the json-encoded telemetry upload configuration.
	// This parameter is required.
	UploadConfig []byte

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

	// Allow uploading telemetry for Go development versions even if the
	// upload configuration does not explicitly include them.
	AllowGoDevel bool

	// ErrorLog specifies an optional logger for errors
	// that occur when attempting to upload telemetry.
	// If nil, errors are not logged.
	ErrorLog *log.Logger
}

var countersToUpload map[string]struct{}

// Start initializes telemetry using the specified configuration.
func Start(cfg Config) {
	if cfg.UploadConfig == nil {
		panic("UploadConfigPath must be set in telemetry.Config")
	}
	uploadConfig, err := config.UnmarshalConfig(cfg.UploadConfig)
	if err != nil {
		panic(fmt.Errorf("failed to unmarshal telemetry config: %v", err))
	}
	if !slices.Contains(uploadConfig.GOOS, runtime.GOOS) ||
		!slices.Contains(uploadConfig.GOARCH, runtime.GOARCH) {
		// Only start telemetry if the current GOOS and GOARCH
		// are supported by the telemetry configuration.
		return
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		panic("failed to read build info for telemetry")
	}
	ver, prog := telemetry.ProgramInfo(bi)
	if ver == "devel" {
		if !cfg.AllowGoDevel {
			// If the Go version is a development version and we are not allowing
			// development versions, do not start telemetry.
			return
		}
	}

	progIdx := slices.IndexFunc(uploadConfig.Programs, func(p *config.ProgramConfig) bool {
		return p.Name == prog
	})
	if progIdx == -1 {
		return // Program not configured for telemetry
	}
	countersToUpload = make(map[string]struct{})
	for _, c := range uploadConfig.Programs[progIdx].Counters {
		if c.Name == "" {
			continue // Skip empty counter names
		}
		for _, e := range config.Expand(c.Name) {
			countersToUpload[e] = struct{}{}
		}
	}

	telemetry.Init(&appinsights.Client{
		InstrumentationKey: cfg.InstrumentationKey,
		Endpoint:           cfg.Endpoint,
		MaxBatchSize:       cfg.MaxBatchSize,
		MaxBatchInterval:   cfg.MaxBatchInterval,
		Tags: map[string]string{
			"ai.application.ver": ver,
			"ai.cloud.role":      prog,
		},
		UploadFilter: uploadFilter,
		ErrorLog:     cfg.ErrorLog,
	})
}

// Close closes the telemetry client and flushes any remaining telemetry data.
// It should be called when the application is shutting down to ensure all
// telemetry data is sent before the program exits.
func Close(ctx context.Context) {
	if telemetry.Client != nil {
		telemetry.Client.Close(ctx)
	}
}

func uploadFilter(name string) bool {
	_, ok := countersToUpload[name]
	return ok
}
