// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/microsoft/go-infra/telemetry"
	"github.com/microsoft/go-infra/telemetry/counter"
	"github.com/microsoft/go-infra/telemetry/internal/config"
	itelemetry "github.com/microsoft/go-infra/telemetry/internal/telemetry"
)

func TestTelemetry(t *testing.T) {
	uploadConfig := baseUploadConfig(t)

	testTelemetry(t, uploadConfig, 4)
}

func TestTelemetryWrongGOOS(t *testing.T) {
	uploadConfig := baseUploadConfig(t)
	uploadConfig.GOOS = []string{"not-a-real-os"} // intentionally wrong

	testTelemetry(t, uploadConfig, 0)
}

func TestTelemetryWrongGOARCH(t *testing.T) {
	uploadConfig := baseUploadConfig(t)
	uploadConfig.GOARCH = []string{"not-a-real-arch"} // intentionally wrong

	testTelemetry(t, uploadConfig, 0)
}

func TestTelemetryWrongGoVersion(t *testing.T) {
	uploadConfig := baseUploadConfig(t)
	uploadConfig.GoVersion = []string{"not-a-real-go-version"} // intentionally wrong

	testTelemetry(t, uploadConfig, 0)
}

func TestTelemetryWrongGoProgram(t *testing.T) {
	uploadConfig := baseUploadConfig(t)
	uploadConfig.Programs[0].Name = "not-a-real-program" // intentionally wrong

	testTelemetry(t, uploadConfig, 0)
}

func testTelemetry(t *testing.T, config config.UploadConfig, uploads int) {
	startTelemetry(t, config, uploads)

	counter.Inc("test_counter")
	counter.Inc("go:0") // skipped
	counter.Inc("go:1")
	counter.Inc("go:2")
	counter.Inc("go:3")
	counter.Inc("go:4") // skipped

	telemetry.Close(t.Context())
}

func baseUploadConfig(t *testing.T) config.UploadConfig {
	t.Helper()
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("failed to read build info")
	}
	ver, prog := itelemetry.ProgramInfo(bi)
	return config.UploadConfig{
		GOOS:      []string{runtime.GOOS},
		GOARCH:    []string{runtime.GOARCH},
		GoVersion: []string{ver},
		Programs: []*config.ProgramConfig{
			{
				Name: prog,
				Counters: []config.CounterConfig{
					{Name: "test_counter"},
					{Name: "go:{1,2,3}"},
				},
			},
		},
	}
}

func startTelemetry(t *testing.T, cfg config.UploadConfig, uploads int) {
	t.Helper()
	var got int
	httptestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got++
		if got > uploads {
			t.Errorf("too many uploads: got %d, want %d", got, uploads)
		}
		fmt.Fprintf(w, `{"itemsReceived": %d, "itemsAccepted": %d}`, uploads, uploads)
	}))
	t.Cleanup(func() {
		if got < uploads {
			t.Errorf("expected %d uploads, got %d", uploads, got)
		}
		httptestServer.Close()
	})
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cfgFilePath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgFilePath, cfgData, 0o644); err != nil {
		t.Fatal(err)
	}
	telemetry.Start(telemetry.Config{
		InstrumentationKey: "fake-key",
		Endpoint:           httptestServer.URL,
		MaxBatchSize:       1,
		UploadConfigPath:   cfgFilePath,
	})
}
