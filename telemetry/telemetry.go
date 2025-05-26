// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"context"
	_ "embed"
	"fmt"
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

	// UploadConfigPath is the path to the telemetry upload configuration file.
	// If empty, the default configuration embedded in the binary will be used.
	UploadConfigPath string
}

//go:embed config.json
var uploadConfigData []byte

var countersToUpload map[string]struct{}

// Start initializes telemetry using the specified configuration.
func Start(cfg Config) {
	var uploadConfig *config.UploadConfig
	var err error
	if cfg.UploadConfigPath != "" {
		uploadConfig, err = config.ReadConfig(cfg.UploadConfigPath)
	} else {
		uploadConfig, err = config.UnmarshalConfig(uploadConfigData)
	}
	if err != nil {
		panic(fmt.Errorf("failed to unmarshal telemetry config: %v", err))
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		panic("failed to read build info for telemetry")
	}
	ver, prog := telemetry.ProgramInfo(bi)
	if !slices.Contains(uploadConfig.GOOS, runtime.GOOS) ||
		!slices.Contains(uploadConfig.GOARCH, runtime.GOARCH) ||
		!slices.Contains(uploadConfig.GoVersion, ver) {
		// Only start telemetry if the current GOOS, GOARCH, and Go version
		// are supported by the telemetry configuration.
		return
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

	telemetry.Client = &appinsights.Client{
		InstrumentationKey: cfg.InstrumentationKey,
		Endpoint:           cfg.Endpoint,
		MaxBatchSize:       cfg.MaxBatchSize,
		MaxBatchInterval:   cfg.MaxBatchInterval,
		Tags: map[string]string{
			"msgo.go.goos":    runtime.GOOS,
			"msgo.go.goarch":  runtime.GOARCH,
			"msgo.go.version": ver,
		},
		UploadFilter: uploadFilter,
	}
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
