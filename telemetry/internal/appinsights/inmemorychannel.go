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

// inMemoryChannel stores events exclusively in memory.
// Presently the only telemetry channel implementation available.
type inMemoryChannel struct {
	endpointAddr    string
	collectChan     chan *contracts.Envelope
	flushChan       chan *flushMessage
	retryChan       chan *retryMessage
	batchSize       int
	batchInterval   time.Duration
	waitgroup       sync.WaitGroup
	throttled       atomic.Bool
	transmitter     transmitter
	errorLog        *log.Logger
	cancelCtx       context.Context
	cancelCauseFunc context.CancelCauseFunc
	closed          atomic.Bool
}

type flushMessage struct {
	// If specified, a message will be sent on this channel when all pending telemetry items have been submitted
	callback chan struct{}
	close    bool
}

type retryMessage struct {
	throttled  bool
	retryAfter time.Time
	items      []*contracts.Envelope
}

// newInMemoryChannel creates an inMemoryChannel instance and starts a background submission goroutine.
func newInMemoryChannel(endpointUrl string, batchSize int, batchInterval time.Duration, httpClient *http.Client, errorLog *log.Logger) *inMemoryChannel {
	// Set up the channel
	channel := &inMemoryChannel{
		endpointAddr:  endpointUrl,
		collectChan:   make(chan *contracts.Envelope),
		flushChan:     make(chan *flushMessage),
		retryChan:     make(chan *retryMessage),
		batchSize:     batchSize,
		batchInterval: batchInterval,
		transmitter:   newTransmitter(endpointUrl, httpClient),
		errorLog:      errorLog,
	}
	channel.cancelCtx, channel.cancelCauseFunc = context.WithCancelCause(context.Background())

	go channel.acceptLoop()

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
	channel.flushChan <- &flushMessage{}
}

func (channel *inMemoryChannel) retry(throttled bool, retryAfter time.Time, items []*contracts.Envelope) {
	if channel.closed.Load() {
		return
	}
	channel.retryChan <- &retryMessage{throttled, retryAfter, items}
}

var errStopped = errors.New("client stopped")

func (channel *inMemoryChannel) stop() {
	if channel.closed.Load() {
		return
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errStopped)
	channel.cancelCauseFunc(context.Cause(ctx))
}

// Flushes and tears down the submission goroutine and closes internal
// channels.  Returns a channel that is closed when all pending telemetry
// items have been submitted and it is safe to shut down without losing
// telemetry.
func (channel *inMemoryChannel) close(ctx context.Context) {
	if channel.closed.Load() {
		return
	}

	callback := make(chan struct{})
	channel.flushChan <- &flushMessage{
		callback: callback,
		close:    true,
	}
	select {
	case <-ctx.Done():
		channel.cancelCauseFunc(context.Cause(ctx))
	case <-callback:
		// Successfully flushed
	}
}

func (channel *inMemoryChannel) acceptLoop() {
	channel.start()
	channel.closed.Store(true)
}

// Part of channel accept loop: Initialize buffer and accept first message, handle controls.
func (channel *inMemoryChannel) start() {
	items := make([]*contracts.Envelope, 0, channel.batchSize)
	timer := time.NewTimer(time.Hour)
	timer.Stop() // Stop timer until we need it.
	var dropped int
	for {
		select {
		case event := <-channel.collectChan:
			if event == nil {
				panic("received nil event")
			}
			if channel.throttled.Load() {
				// Check if there is space to add the item to the batch.
				// If not, then drop the event.
				if len(items) < channel.batchSize {
					items = append(items, event)
				} else {
					dropped++
				}
				continue
			}
			items = append(items, event)
			if len(items) >= channel.batchSize {
				timer.Stop()
				channel.sendBatch(items)
				items = items[:0]
			} else if len(items) == 1 {
				// Start the timer if this is the first item in the batch.
				timer.Reset(channel.batchInterval)
			}

		case flush := <-channel.flushChan:
			if !flush.close && channel.throttled.Load() {
				// Ignore the flush request if we are throttled and
				// the flush is not closing.
				// If closing, try sending the items one last time.
				continue
			}
			timer.Stop()
			channel.signalWhenDone(flush.callback)
			channel.sendBatch(items)
			if flush.close {
				return
			}
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
			// Delete items that have been retried more than 2 times.
			msg.items = slices.DeleteFunc(msg.items, func(item *contracts.Envelope) bool {
				return item.Retries > 2
			})
			for _, item := range msg.items {
				item.Retries++
			}
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
			timer.Stop()
			return
		}
	}
}

var itemsBuf = sync.Pool{
	New: func() any {
		buf := make([]*contracts.Envelope, 0, 1024)
		return &buf
	},
}

// Part of channel accept loop: Check and wait on throttle, submit pending telemetry
func (channel *inMemoryChannel) sendBatch(items []*contracts.Envelope) {
	if len(items) == 0 {
		return
	}
	channel.waitgroup.Add(1)

	// Copy the items to a temporary buffer to let the caller
	// reuse the item slice.
	buf := itemsBuf.Get().(*[]*contracts.Envelope)
	*buf = (*buf)[:0]
	*buf = append(*buf, items...)

	// Start a goroutine to transmit the items without blocking
	// the accept loop.
	go func() {
		defer channel.waitgroup.Done()
		defer itemsBuf.Put(buf)
		channel.transmitRetry(*buf)
	}()
}

func (channel *inMemoryChannel) transmitRetry(items []*contracts.Envelope) {
	payload, err := serialize(items)
	if err != nil {
		channel.log(err)
		if payload == nil {
			return
		}
	}

	result, err := channel.transmitter.Transmit(channel.cancelCtx, payload, items)
	if err != nil {
		channel.logf("Error transmitting payload: %v", err)
		return
	}
	if result.isSuccess() {
		return
	}
	canRetry := result.canRetry()
	throttled := result.isThrottled()
	channel.logf("Failed to transmit payload: code=%v, received=%d, accepted=%d, canRetry=%t, throttled=%t",
		result.statusCode, result.response.ItemsReceived, result.response.ItemsAccepted, canRetry, throttled)

	// Determine if we need to retry anything.
	if canRetry {
		// Filter down to failed items.
		payload, items = result.getRetryItems(payload, items)
		if len(payload) == 0 || len(items) == 0 {
			return
		}
	} else {
		return
	}

	channel.retry(throttled, result.retryAfter, items)
}

func (channel *inMemoryChannel) signalWhenDone(callback chan struct{}) {
	if callback == nil {
		return
	}
	go func() {
		channel.waitgroup.Wait()
		close(callback)
	}()
}
