// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package appinsights

import (
	"context"
	"errors"
	"log"
	"net/http"
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

	throttled   atomic.Bool
	closed      atomic.Bool
	sendQueue   []*[]batchItem
	sendQueueMu sync.Mutex
	sendMu      sync.Mutex
	inflight    atomic.Int64 // Number of items currently being sent.

	itemsBuf sync.Pool
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
		itemsBuf: sync.Pool{
			New: func() any {
				buf := make([]batchItem, 0, batchSize)
				return &buf
			},
		},
	}
	channel.cancelCtx, channel.cancelCauseFunc = context.WithCancelCause(context.Background())
	return channel
}

func (channel *inMemoryChannel) logf(format string, args ...any) {
	channel.errorLog.Printf(format, args...)
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
	channel.checkInflight()
}

// close flushes and tears down the submission goroutine and closes internal
// channels. Returns when all pending telemetry items have been submitted
// (it is then safe to shut down without losing telemetry) or when
// the context is canceled.
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
	channel.checkInflight()
}

func (channel *inMemoryChannel) checkInflight() {
	if inflight := channel.inflight.Load(); inflight > 0 {
		channel.logf("failed to transmit %d telemetry items: %v", inflight, context.Cause(channel.cancelCtx))
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
					channel.inflight.Add(1)
					items = append(items, batchItem{item, 0})
				} else {
					dropped++
				}
				continue
			}
			channel.inflight.Add(1)
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
					channel.logf("failed to transmit %d telemetry items", dropped)
					dropped = 0
				}
			}
			channel.sendBatch(items)
			items = items[:0]

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

// sendBatch schedules a batch of items for transmission.
func (channel *inMemoryChannel) sendBatch(items []batchItem) {
	channel.sendQueueMu.Lock()
	defer channel.sendQueueMu.Unlock()

	if len(items) == 0 {
		// Cancel the accept loop if we are closed and the queue is empty.
		if len(channel.sendQueue) == 0 && channel.closed.Load() {
			channel.cancelCauseFunc(errClosed)
		}
		return
	}

	// Copy the items to a temporary buffer to let the caller
	// reuse the item slice. The size of items is capped to the
	// maximum batch size, so the length of the polled buffer
	// can't grow unbounded.
	buf := channel.itemsBuf.Get().(*[]batchItem)
	*buf = (*buf)[:0]
	*buf = append(*buf, items...)
	channel.sendQueue = append(channel.sendQueue, buf)

	// Start a goroutine to transmit the items without blocking
	// the accept loop.
	go func() {
		retry := channel.transmitRetry()
		channel.sendQueueMu.Lock()
		defer channel.sendQueueMu.Unlock()
		if !retry && len(channel.sendQueue) == 0 && channel.closed.Load() {
			// Cancel the accept loop if we are closed and the queue is empty.
			channel.cancelCauseFunc(errClosed)
		}
	}()
}

// transmitRetry pops the first item from the queue and transmits it.
// If the transmission fails, it retries the items that can be retried.
// Returns true if some items were retried.
func (channel *inMemoryChannel) transmitRetry() bool {
	// Allow only one goroutine to transmit at a time.
	channel.sendMu.Lock()
	defer channel.sendMu.Unlock()

	// Pop the first item from the queue.
	channel.sendQueueMu.Lock()
	itemsPtr := channel.sendQueue[0]
	channel.sendQueue = channel.sendQueue[1:]
	channel.sendQueueMu.Unlock()

	var succed, failed int
	defer func() {
		if failed > 0 {
			channel.logf("failed to transmit %d telemetry items", failed)
		}
		channel.inflight.Add(-int64(failed + succed))
		channel.itemsBuf.Put(itemsPtr)
	}()
	result, err := channel.transmitter.transmit(channel.cancelCtx, *itemsPtr)
	if err != nil {
		failed = len(*itemsPtr)
		if result == nil {
			return false
		}
	}
	if result.isSuccess() {
		succed = len(*itemsPtr)
		return false
	}
	canRetry := result.canRetry()
	throttled := result.isThrottled()

	if !canRetry {
		failed = len(*itemsPtr)
		return false
	}

	// Filter down to failed items.
	var retryItems []batchItem
	succed, failed, retryItems = result.getRetryItems(*itemsPtr)
	if len(retryItems) == 0 {
		return false
	}

	channel.retry(throttled, result.retryAfter, retryItems)
	return true
}
