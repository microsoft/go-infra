package appinsights

import (
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microsoft/go-infra/telemetry/internal/appinsights/internal/contracts"
)

var (
	submit_retries = []time.Duration{time.Duration(10 * time.Second), time.Duration(30 * time.Second), time.Duration(60 * time.Second)}
)

// inMemoryChannel stores events exclusively in memory.
// Presently the only telemetry channel implementation available.
type inMemoryChannel struct {
	endpointAddr  string
	collectChan   chan *contracts.Envelope
	controlChan   chan *inMemoryChannelControl
	batchSize     int
	batchInterval time.Duration
	waitgroup     sync.WaitGroup
	throttle      *throttleManager
	transmitter   transmitter

	closed atomic.Bool
}

type inMemoryChannelControl struct {
	// If true, flush the buffer.
	flush bool

	// If true, stop listening on the channel.  (Flush is required if any events are to be sent)
	stop bool

	// If stopping and flushing, this specifies whether to retry submissions on error.
	retry bool

	// If retrying, what is the max time to wait before finishing up?
	timeout time.Duration

	// If specified, a message will be sent on this channel when all pending telemetry items have been submitted
	callback chan struct{}
}

// newInMemoryChannel creates an inMemoryChannel instance and starts a background submission goroutine.
func newInMemoryChannel(endpointUrl string, batchSize int, batchInterval time.Duration, httpClient *http.Client) *inMemoryChannel {
	channel := &inMemoryChannel{
		endpointAddr:  endpointUrl,
		collectChan:   make(chan *contracts.Envelope),
		controlChan:   make(chan *inMemoryChannelControl),
		batchSize:     batchSize,
		batchInterval: batchInterval,
		throttle:      newThrottleManager(),
		transmitter:   newTransmitter(endpointUrl, httpClient),
	}

	go channel.acceptLoop()

	return channel
}

// EndpointAddress is the address of the endpoint to which telemetry is sent.
func (channel *inMemoryChannel) EndpointAddress() string {
	return channel.endpointAddr
}

// Queues a single telemetry item
func (channel *inMemoryChannel) Send(item *contracts.Envelope) {
	if item != nil && !channel.closed.Load() {
		channel.collectChan <- item
	}
}

// Forces the current queue to be sent
func (channel *inMemoryChannel) Flush() {
	if channel.closed.Load() {
		return
	}
	channel.controlChan <- &inMemoryChannelControl{
		flush: true,
	}
}

// Tears down the submission goroutines, closes internal channels.  Any
// telemetry waiting to be sent is discarded.  Further calls to Send() have
// undefined behavior.  This is a more abrupt version of Close().
func (channel *inMemoryChannel) Stop() {
	if channel.closed.Load() {
		return
	}
	channel.controlChan <- &inMemoryChannelControl{
		stop: true,
	}
}

// Returns true if this channel has been throttled by the data collector.
func (channel *inMemoryChannel) IsThrottled() bool {
	return channel.throttle != nil && channel.throttle.IsThrottled()
}

// Flushes and tears down the submission goroutine and closes internal
// channels.  Returns a channel that is closed when all pending telemetry
// items have been submitted and it is safe to shut down without losing
// telemetry.
//
// If retryTimeout is specified and non-zero, then failed submissions will
// be retried until one succeeds or the timeout expires, whichever occurs
// first.  A retryTimeout of zero indicates that failed submissions will be
// retried as usual.  An omitted retryTimeout indicates that submissions
// should not be retried if they fail.
//
// Note that the returned channel may not be closed before retryTimeout even
// if it is specified.  This is because retryTimeout only applies to the
// latest telemetry buffer.  This may be typical for applications that
// submit a large amount of telemetry or are prone to being throttled.  When
// exiting, you should select on the result channel and your own timer to
// avoid long delays.
func (channel *inMemoryChannel) Close(timeout time.Duration) <-chan struct{} {
	if channel.closed.Load() {
		return nil
	}
	callback := make(chan struct{})

	ctl := &inMemoryChannelControl{
		stop:     true,
		flush:    true,
		retry:    false,
		callback: callback,
	}

	if timeout != 0 {
		ctl.retry = true
		ctl.timeout = timeout
	}

	channel.controlChan <- ctl

	return callback
}

func (channel *inMemoryChannel) acceptLoop() {
	channelState := newInMemoryChannelState(channel)

	for !channelState.stopping {
		channelState.start()
	}

	channelState.stop()
}

// Data shared between parts of a channel
type inMemoryChannelState struct {
	channel      *inMemoryChannel
	stopping     bool
	buffer       telemetryBufferItems
	retry        bool
	retryTimeout time.Duration
	callback     chan struct{}
	timer        *time.Timer
}

func newInMemoryChannelState(channel *inMemoryChannel) *inMemoryChannelState {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &inMemoryChannelState{
		channel:  channel,
		buffer:   make(telemetryBufferItems, 0, 16),
		stopping: false,
		timer:    timer,
	}
}

// Part of channel accept loop: Initialize buffer and accept first message, handle controls.
func (state *inMemoryChannelState) start() bool {
	if len(state.buffer) > 16 {
		// Start out with the size of the previous buffer
		state.buffer = make(telemetryBufferItems, 0, cap(state.buffer))
	} else if len(state.buffer) > 0 {
		// Start out with at least 16 slots
		state.buffer = make(telemetryBufferItems, 0, 16)
	}

	// Wait for an event
	select {
	case event := <-state.channel.collectChan:
		if event == nil {
			// Channel closed?  Not intercepted by Send()?
			panic("Received nil event")
		}

		state.buffer = append(state.buffer, event)

	case ctl := <-state.channel.controlChan:
		// The buffer is empty, so there would be no point in flushing
		state.channel.signalWhenDone(ctl.callback)

		if ctl.stop {
			state.stopping = true
			return false
		}
	}

	if len(state.buffer) == 0 {
		return true
	}

	return state.waitToSend()
}

// Part of channel accept loop: Wait for buffer to fill, timeout to expire, or flush
func (state *inMemoryChannelState) waitToSend() bool {
	// Things that are used by the sender if we receive a control message
	state.retryTimeout = 0
	state.retry = true
	state.callback = nil

	// Delay until timeout passes or buffer fills up
	state.timer.Reset(state.channel.batchInterval)
	for {
		if len(state.buffer) >= state.channel.batchSize {
			state.timer.Stop()
			return state.send()
		}

		select {
		case event := <-state.channel.collectChan:
			if event == nil {
				// Channel closed?  Not intercepted by Send()?
				panic("Received nil event")
			}

			state.buffer = append(state.buffer, event)

		case ctl := <-state.channel.controlChan:
			if ctl.stop {
				state.stopping = true
				state.retry = ctl.retry
				if !ctl.flush {
					// No flush? Just exit.
					state.channel.signalWhenDone(ctl.callback)
					return false
				}
			}

			if ctl.flush {
				state.timer.Stop()
				state.retryTimeout = ctl.timeout
				state.callback = ctl.callback
				return state.send()
			}

		case <-state.timer.C:
			// Timeout expired
			return state.send()
		}
	}
}

// Part of channel accept loop: Check and wait on throttle, submit pending telemetry
func (state *inMemoryChannelState) send() bool {
	// Hold up transmission if we're being throttled
	if !state.stopping && state.channel.throttle.IsThrottled() {
		if !state.waitThrottle() {
			// Stopped
			return false
		}
	}

	// Send
	if len(state.buffer) > 0 {
		state.channel.waitgroup.Add(1)

		// If we have a callback, wait on the waitgroup now that it's
		// incremented.
		state.channel.signalWhenDone(state.callback)

		go func(buffer telemetryBufferItems, retry bool, retryTimeout time.Duration) {
			defer state.channel.waitgroup.Done()
			state.channel.transmitRetry(buffer, retry, retryTimeout)
		}(state.buffer, state.retry, state.retryTimeout)
	} else if state.callback != nil {
		state.channel.signalWhenDone(state.callback)
	}

	return true
}

// Part of channel accept loop: Wait for throttle to expire while dropping messages
func (state *inMemoryChannelState) waitThrottle() bool {
	// Channel is currently throttled.  Once the buffer fills, messages will
	// be lost...  If we're exiting, then we'll just try to submit anyway.  That
	// request may be throttled and transmitRetry will perform the backoff correctly.

	log.Println("Channel is throttled, events may be dropped.")
	throttleDone := state.channel.throttle.NotifyWhenReady()
	dropped := 0

	defer log.Printf("Channel dropped %d events while throttled", dropped)

	for {
		select {
		case <-throttleDone:
			close(throttleDone)
			return true

		case event := <-state.channel.collectChan:
			// If there's still room in the buffer, then go ahead and add it.
			if len(state.buffer) < state.channel.batchSize {
				state.buffer = append(state.buffer, event)
			} else {
				if dropped == 0 {
					log.Print("Buffer is full, dropping further events.")
				}

				dropped++
			}

		case ctl := <-state.channel.controlChan:
			if ctl.stop {
				state.stopping = true
				state.retry = ctl.retry
				if !ctl.flush {
					state.channel.signalWhenDone(ctl.callback)
					return false
				} else {
					// Make an exception when stopping
					return true
				}
			}

			// Cannot flush
			// TODO: Figure out what to do about callback?
			if ctl.flush {
				state.channel.signalWhenDone(ctl.callback)
			}
		}
	}
}

// Part of channel accept loop: Clean up and close telemetry channel
func (state *inMemoryChannelState) stop() {
	state.channel.closed.Store(true)
	close(state.channel.collectChan)
	close(state.channel.controlChan)

	// Throttle can't close until transmitters are done using it.
	state.channel.waitgroup.Wait()
	state.channel.throttle.Stop()
}

func (channel *inMemoryChannel) transmitRetry(items telemetryBufferItems, retry bool, retryTimeout time.Duration) {
	payload := items.serialize()
	retryTimeRemaining := retryTimeout

	for _, wait := range submit_retries {
		result, err := channel.transmitter.Transmit(payload, items)
		if err == nil && result != nil && result.IsSuccess() {
			return
		}

		if !retry {
			log.Print("Refusing to retry telemetry submission (retry==false)")
			return
		}

		// Check for success, determine if we need to retry anything
		if result != nil {
			if result.CanRetry() {
				// Filter down to failed items
				payload, items = result.GetRetryItems(payload, items)
				if len(payload) == 0 || len(items) == 0 {
					return
				}
			} else {
				log.Print("Cannot retry telemetry submission")
				return
			}

			// Check for throttling
			if result.IsThrottled() {
				if result.retryAfter != nil {
					log.Printf("Channel is throttled until %s", *result.retryAfter)
					channel.throttle.RetryAfter(*result.retryAfter)
				} else {
					// TODO: Pick a time
				}
			}
		}

		if retryTimeout > 0 {
			// We're on a time schedule here.  Make sure we don't try longer
			// than we have been allowed.
			if retryTimeRemaining < wait {
				// One more chance left -- we'll wait the max time we can
				// and then retry on the way out.
				time.Sleep(retryTimeRemaining)
				break
			} else {
				// Still have time left to go through the rest of the regular
				// retry schedule
				retryTimeRemaining -= wait
			}
		}

		log.Printf("Waiting %s to retry submission", wait)
		time.Sleep(wait)

		// Wait if the channel is throttled and we're not on a schedule
		if channel.IsThrottled() && retryTimeout == 0 {
			log.Printf("Channel is throttled; extending wait time.")
			ch := channel.throttle.NotifyWhenReady()
			result := <-ch
			close(ch)

			if !result {
				return
			}
		}
	}

	// One final try
	_, err := channel.transmitter.Transmit(payload, items)
	if err != nil {
		log.Print("Gave up transmitting payload; exhausted retries")
	}
}

func (channel *inMemoryChannel) signalWhenDone(callback chan struct{}) {
	if callback != nil {
		go func() {
			channel.waitgroup.Wait()
			close(callback)
		}()
	}
}
