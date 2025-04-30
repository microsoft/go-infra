package appinsights

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"runtime"
	"time"
)

// Configuration data used to initialize a new TelemetryClient.
type TelemetryConfiguration struct {
	// Maximum number of telemetry items that can be submitted in each
	// request. If this many items are buffered, the buffer will be
	// flushed before MaxBatchInterval expires.
	// If zero, it defaults to 1024.
	MaxBatchSize int

	// Maximum time to wait before sending a batch of telemetry.
	// If zero, it defaults to 10 seconds.
	MaxBatchInterval time.Duration

	// Customized http client.
	// If nil, it defaults to http.DefaultClient.
	Client *http.Client
}

type TelemetryClient struct {
	channel *inMemoryChannel
	context *telemetryContext
	enabled bool
}

// Creates a new telemetry client instance configured by the specified TelemetryConfiguration object.
// The instrumentation key and endpoint URL are required.
// The configuration is optional and can be nil.
func NewTelemetryClient(insturmentationKey, endpointUrl string, config *TelemetryConfiguration) *TelemetryClient {
	if insturmentationKey == "" {
		panic("instrumentation key is required")
	}
	if endpointUrl == "" {
		panic("endpoint URL is required")
	}
	var batchSize int
	var batchInterval time.Duration
	var httpClient *http.Client
	if config != nil {
		batchSize = config.MaxBatchSize
		batchInterval = config.MaxBatchInterval
		httpClient = config.Client
	}
	batchSize = cmp.Or(batchSize, 1024)
	batchInterval = cmp.Or(batchInterval, 10*time.Second)
	httpClient = cmp.Or(httpClient, http.DefaultClient)

	return &TelemetryClient{
		channel: newInMemoryChannel(endpointUrl, batchSize, batchInterval, httpClient),
		context: setupContext(insturmentationKey),
		enabled: true,
	}
}

func setupContext(instrumentationKey string) *telemetryContext {
	context := newTelemetryContext(instrumentationKey)
	context.Tags.Internal().SetSdkVersion(sdkName + ":" + Version)
	context.Tags.Device().SetOsVersion(runtime.GOOS)

	if hostname, err := os.Hostname(); err == nil {
		context.Tags.Device().SetId(hostname)
		context.Tags.Cloud().SetRoleInstance(hostname)
	}

	return context
}

// Gets whether this client is enabled and will accept telemetry.
func (tc *TelemetryClient) Enabled() bool {
	return tc.enabled
}

// Enables or disables the telemetry client.  When disabled, telemetry is
// silently swallowed by the client.  Defaults to enabled.
func (tc *TelemetryClient) SetEnabled(isEnabled bool) {
	tc.enabled = isEnabled
}

// TrackNewEvent logs a user action with the specified name.
func (tc *TelemetryClient) TrackNewEvent(name string) {
	tc.TrackEvent(NewEventTelemetry(name))
}

// TrackEvent los a user action with the specified event.
func (tc *TelemetryClient) TrackEvent(ev *EventTelemetry) {
	tc.track(ev)
}

// Forces the current queue to be sent.
func (tc *TelemetryClient) Flush() {
	tc.channel.Flush()
}

// Close flushes and tears down the submission goroutine and closes internal channels.
// Waits until all pending telemetry items have been submitted.
func (tc *TelemetryClient) Close(ctx context.Context) error {
	var d time.Duration
	if timeout, ok := ctx.Deadline(); ok {
		d = time.Until(timeout)
		if d == 0 {
			// Don't confuse the user with a zero duration.
			d = -1
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tc.channel.Close(d):
		// TODO: check for errors.
		return nil
	}
}

// Tears down the submission goroutines, closes internal channels.
// Any telemetry waiting to be sent is discarded.
// This is a more abrupt version of [TelemetryClient.Close].
func (tc *TelemetryClient) Stop() {
	tc.channel.Stop()
}

// Submits the specified telemetry item.
func (tc *TelemetryClient) track(item telemetry) {
	if tc.enabled && item != nil {
		tc.channel.Send(tc.context.envelop(item))
	}
}
