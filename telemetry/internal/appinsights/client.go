// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

// Client is the main entry point for sending telemetry to Application Insights.
// Changing its properties after a telemetry item is created will have no effect.
type Client struct {
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

	// Customized http client.
	// If nil, it defaults to http.DefaultClient.
	HTTPClient *http.Client

	// Tags to be sent with every telemetry item.
	// If nil, no additional tags will be sent.
	Tags map[string]string

	// Logger specifies a structured logger.
	// If nil nothing is logged.
	Logger *slog.Logger

	// Function to filter out telemetry items by name before they are sent.
	// If nil, all telemetry items are sent.
	UploadFilter func(name string) bool

	channel *inMemoryChannel
	context *telemetryContext

	initialized atomic.Bool
	initOnce    sync.Once
}

// init initializes the client.
// It is safe to call this method multiple times concurrently.
func (c *Client) init() {
	c.initOnce.Do(func() {
		if c.InstrumentationKey == "" {
			panic("instrumentation key is required")
		}
		endpoint := cmp.Or(c.Endpoint, "https://dc.services.visualstudio.com/v2/track")
		batchSize := cmp.Or(c.MaxBatchSize, 1024)
		batchInterval := cmp.Or(c.MaxBatchInterval, 10*time.Second)
		httpClient := cmp.Or(c.HTTPClient, http.DefaultClient)
		c.channel = newInMemoryChannel(endpoint, batchSize, batchInterval, httpClient, c.Logger)
		c.context = setupContext(c.InstrumentationKey, c.Tags)
		if err := contracts.SanitizeTags(c.context.Tags); err != nil {
			c.channel.warn("tags have been sanitized", "error", err)
		}

		go c.channel.acceptLoop()
		c.initialized.Store(true)
	})
}

func setupContext(instrumentationKey string, tags map[string]string) *telemetryContext {
	context := newTelemetryContext(instrumentationKey)
	context.Tags["ai.internal.sdkVersion"] = internalVersion
	maps.Copy(context.Tags, tags)
	return context
}

// NewEvent creates a new event with the specified name.
// If c is nil, returns a usable Event that does not send any telemetry.
func (c *Client) NewEvent(name string, properties map[string]string) *Event {
	return &Event{
		name:       name,
		client:     c,
		properties: properties,
	}
}

// TrackEvent logs a user action with the specified name.
// If c is nil, nothing is logged.
func (c *Client) TrackEvent(name string, properties map[string]string) {
	c.NewEvent(name, properties).Inc()
}

// Forces the current queue to be sent.
func (c *Client) Flush() {
	if !c.initialized.Load() {
		return
	}
	c.channel.flush()
}

// Close flushes and tears down the submission goroutine and closes internal channels.
// Waits until all pending telemetry items have been submitted.
func (c *Client) Close(ctx context.Context) {
	if !c.initialized.Load() {
		return
	}
	c.channel.close(ctx)
}

// Stop tears down the submission goroutines, closes internal channels.
// Any telemetry waiting to be sent is discarded.
// This is a more abrupt version of [Client.Close].
func (c *Client) Stop() {
	c.channel.stop()
}

// Submits the specified telemetry item.
func (c *Client) track(data contracts.EventData, n int64) {
	if n == 0 || (c.UploadFilter != nil && !c.UploadFilter(data.Name)) {
		return
	}
	c.init()
	ev := c.context.envelop(data)
	if err := ev.Sanitize(); err != nil {
		c.channel.warn("tags have been sanitized", "error", err)
	}
	for range n {
		c.channel.send(ev)
	}
}

// Event represents an event to be tracked.
type Event struct {
	name       string
	client     *Client
	properties map[string]string
}

// Inc adds 1 to the counter.
func (e *Event) Inc() {
	e.Add(1)
}

// Add adds n to the counter. n cannot be negative, as counts cannot decrease.
func (e *Event) Add(n int64) {
	if e == nil || e.client == nil {
		return
	}
	e.client.track(contracts.EventData{
		Name:       e.name,
		Ver:        2,
		Properties: e.properties,
	}, n)
}
