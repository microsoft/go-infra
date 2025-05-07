// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

// batchItem is a telemetry item that is sent in a batch.
type batchItem struct {
	item    *contracts.Envelope
	retries int
}

// inMemoryChannel stores events exclusively in memory.
// Presently the only telemetry channel implementation available.
type inMemoryChannel struct {
	endpointAddr  string
	batchSize     int
	batchInterval time.Duration
	errorLog      *log.Logger

	collectChan chan *contracts.Envelope
	flushChan   chan struct{}
	retryChan   chan retryMessage

	transmitter transmitter

	// Use a context instead of a channel to
	// allow propagating the cancellation to the
	// transmitter and to the underlying HTTP client.
	cancelCtx       context.Context
	cancelCauseFunc context.CancelCauseFunc

	throttled atomic.Bool
	closed    atomic.Bool
	inflight  atomic.Int64
}

type retryMessage struct {
	throttled  bool
	retryAfter time.Time
	items      []batchItem
}

// newInMemoryChannel creates an inMemoryChannel instance and starts a background submission goroutine.
func newInMemoryChannel(endpointUrl string, batchSize int, batchInterval time.Duration, httpClient *http.Client, errorLog *log.Logger) *inMemoryChannel {
	// Set up the channel
	channel := &inMemoryChannel{
		endpointAddr:  endpointUrl,
		batchSize:     batchSize,
		batchInterval: batchInterval,
		errorLog:      errorLog,
		collectChan:   make(chan *contracts.Envelope),
		flushChan:     make(chan struct{}),
		retryChan:     make(chan retryMessage),
		transmitter:   newTransmitter(endpointUrl, httpClient),
	}
	channel.cancelCtx, channel.cancelCauseFunc = context.WithCancelCause(context.Background())
	return channel
}

func (channel *inMemoryChannel) log(v ...any) {
	if channel.errorLog != nil {
		channel.errorLog.Print(v...)
	} else {
		log.Print(v...)
	}
}

func (channel *inMemoryChannel) logf(format string, args ...any) {
	if channel.errorLog != nil {
		channel.errorLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

// Queues a single telemetry item
func (channel *inMemoryChannel) send(item *contracts.Envelope) {
	if item != nil && !channel.closed.Load() {
		channel.collectChan <- item
	}
}

// Forces the current queue to be sent
func (channel *inMemoryChannel) flush() {
	if channel.closed.Load() {
		return
	}
	channel.flushChan <- struct{}{}
}

func (channel *inMemoryChannel) retry(throttled bool, retryAfter time.Time, items []batchItem) {
	// Retry even if the channel is closed to allow for
	// retrying items that were already sent.
	channel.retryChan <- retryMessage{throttled, retryAfter, items}
}

var errStopped = errors.New("client stopped")
var errClosed = errors.New("client closed")

func (channel *inMemoryChannel) stop() {
	if channel.closed.Load() {
		return
	}

	channel.closed.Store(true)
	channel.cancelCauseFunc(errStopped)
}

// Flushes and tears down the submission goroutine and closes internal
// channels.  Returns a channel that is closed when all pending telemetry
// items have been submitted and it is safe to shut down without losing
// telemetry.
func (channel *inMemoryChannel) close(ctx context.Context) {
	if channel.closed.Load() {
		return
	}

	channel.closed.Store(true)
	channel.flushChan <- struct{}{}
	select {
	case <-ctx.Done():
		channel.cancelCauseFunc(context.Cause(ctx))
	case <-channel.cancelCtx.Done():
		// Successfully flushed
	}
}

func (channel *inMemoryChannel) acceptLoop() {
	channel.start()
}

// Part of channel accept loop: Initialize buffer and accept first message, handle controls.
func (channel *inMemoryChannel) start() {
	items := make([]batchItem, 0, channel.batchSize)
	timer := time.NewTimer(time.Hour)
	timer.Stop() // Stop timer until we need it.
	var dropped int
	for {
		select {
		case item := <-channel.collectChan:
			if item == nil {
				panic("received nil event")
			}
			if channel.throttled.Load() {
				// Check if there is space to add the item to the batch.
				// If not, then drop the event.
				if len(items) < channel.batchSize {
					items = append(items, batchItem{item, 0})
				} else {
					dropped++
				}
				continue
			}
			items = append(items, batchItem{item, 0})
			if len(items) >= channel.batchSize {
				timer.Stop()
				channel.sendBatch(items)
				items = items[:0]
			} else if len(items) == 1 {
				// Start the timer if this is the first item in the batch.
				timer.Reset(channel.batchInterval)
			}

		case <-channel.flushChan:
			if channel.throttled.Load() {
				// Ignore the flush request if we are throttled.
				continue
			}
			timer.Stop()
			channel.sendBatch(items)
			items = items[:0]

		case <-timer.C:
			if channel.throttled.Load() {
				// When throttled, the timer is reset to the retry time,
				// so if we get here, then we're no longer throttled.
				channel.throttled.Store(false)
				if dropped > 0 {
					channel.logf("Dropped %d telemetry items due to throttling", dropped)
					dropped = 0
				}
			}
			channel.sendBatch(items)
			items = items[:0]
			timer.Reset(channel.batchInterval)

		case msg := <-channel.retryChan:
			// If there is not enough space in the batch, drop the items.
			space := channel.batchSize - len(items)
			if space < len(msg.items) {
				dropped += len(msg.items) - space
				msg.items = msg.items[:space]
			}
			items = append(items, msg.items...)

			if msg.throttled {
				channel.throttled.Store(true)
			}
			if msg.retryAfter.IsZero() {
				// If the retry time is not set, use the default batch interval.
				timer.Reset(channel.batchInterval)
			} else {
				timer.Reset(time.Until(msg.retryAfter))
			}

		case <-channel.cancelCtx.Done():
			// This is the only path to exit the loop.
			timer.Stop()
			return
		}
	}
}

var itemsBuf = sync.Pool{
	New: func() any {
		buf := make([]batchItem, 0, 1024)
		return &buf
	},
}

// Part of channel accept loop: Check and wait on throttle, submit pending telemetry
func (channel *inMemoryChannel) sendBatch(items []batchItem) {
	if len(items) == 0 {
		if channel.inflight.Load() <= 0 && channel.closed.Load() {
			channel.cancelCauseFunc(errClosed)
		}
		return
	}

	channel.inflight.Add(1)

	// Copy the items to a temporary buffer to let the caller
	// reuse the item slice.
	buf := itemsBuf.Get().(*[]batchItem)
	*buf = (*buf)[:0]
	*buf = append(*buf, items...)

	// Start a goroutine to transmit the items without blocking
	// the accept loop.
	go func() {
		var retry bool
		defer func() {
			itemsBuf.Put(buf)
			if channel.inflight.Add(-1) <= 0 {
				if !retry && channel.closed.Load() {
					// Cancel the accept loop if we are closed,
					// as all inflight items have been sent.
					// Don't cancel the loop if we are retrying.
					channel.cancelCauseFunc(errClosed)
				}
			}
		}()
		retry = channel.transmitRetry(*buf)
	}()
}

// transmitRetry transmits the payload and retries if necessary.
// Returns true if the payload has been scheduled for retry.
func (channel *inMemoryChannel) transmitRetry(items []batchItem) bool {
	payload, err := serialize(items)
	if err != nil {
		channel.log(err)
		if payload == nil {
			return false
		}
	}

	result, err := channel.transmitter.transmit(channel.cancelCtx, payload, items)
	if err != nil {
		channel.logf("Error transmitting payload: %v", err)
		return false
	}
	if result.isSuccess() {
		return false
	}
	canRetry := result.canRetry()
	throttled := result.isThrottled()
	channel.logf("Failed to transmit payload: code=%v, received=%d, accepted=%d, canRetry=%t, throttled=%t",
		result.statusCode, result.response.ItemsReceived, result.response.ItemsAccepted, canRetry, throttled)

	if !canRetry {
		return false
	}

	// Filter down to failed items.
	payload, items = result.getRetryItems(payload, items)

	// Delete items that have been retried more than 2 times.
	items = slices.DeleteFunc(items, func(item batchItem) bool {
		return item.retries > 2
	})
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		item.retries++
	}

	channel.retry(throttled, result.retryAfter, items)
	return true
}
