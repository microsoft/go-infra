// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"cmp"
	"context"
	"log"
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

	// ErrorLog specifies an optional logger for errors
	// that occur when attempting to upload telemetry.
	// If nil, logging is done via the log package's standard logger.
	ErrorLog *log.Logger

	channel  *inMemoryChannel
	context  *telemetryContext
	disabled bool

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
		c.channel = newInMemoryChannel(endpoint, batchSize, batchInterval, httpClient, c.ErrorLog)
		c.context = setupContext(c.InstrumentationKey, c.Tags)
		if err := contracts.SanitizeTags(c.context.Tags); err != nil {
			c.channel.logf("Warning sanitizing tags: %v", err)
		}

		go c.channel.acceptLoop()
		c.initialized.Store(true)
	})
}

func setupContext(instrumentationKey string, tags map[string]string) *telemetryContext {
	context := newTelemetryContext(instrumentationKey)
	context.Tags["ai.internal.sdkVersion"] = sdkName + ":" + version
	maps.Copy(context.Tags, tags)
	return context
}

// Enabled returns true if this client is enabled and will accept telemetry.
func (c *Client) Enabled() bool {
	return !c.disabled
}

// SetEnabled enables or disables the client. When disabled, telemetry is
// silently swallowed by the client. Defaults to enabled.
func (c *Client) SetEnabled(enabled bool) {
	c.disabled = !enabled
}

// NewEvent creates a new event with the specified name.
func (c *Client) NewEvent(name string) *Event {
	return &Event{
		name:   name,
		client: c,
	}
}

// TrackEvent logs a user action with the specified name.
func (c *Client) TrackEvent(name string) {
	c.NewEvent(name).Inc()
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
func (c *Client) track(data contracts.EventData, n uint64) {
	if c.disabled || n == 0 {
		return
	}
	c.init()
	ev := c.context.envelop(data)
	if err := ev.Sanitize(); err != nil {
		c.channel.logf("Warning sanitizing telemetry item: %v", err)
	}
	for range n {
		c.channel.send(ev)
	}
}

// Event represents an event to be tracked.
type Event struct {
	name   string
	client *Client
}

// Inc adds 1 to the counter.
func (e *Event) Inc() {
	e.Add(1)
}

// Add adds n to the counter. n cannot be negative, as counts cannot decrease.
func (e *Event) Add(n uint64) {
	e.client.track(contracts.EventData{
		Name: e.name,
		Ver:  2,
	}, n)
}
