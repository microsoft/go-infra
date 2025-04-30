package appinsights

import "time"

// We need to mock out the clock for tests; we'll use this to do it.
var now = time.Now
var sleep = time.Sleep
var newTimer = newStdTimer

type timer interface {
	Stop() bool
	Reset(d time.Duration) bool
	C() <-chan time.Time
}

type stdTimer struct {
	timer *time.Timer
}

func newStdTimer(d time.Duration) timer {
	return stdTimer{
		timer: time.NewTimer(d),
	}
}

func (t stdTimer) Stop() bool {
	return t.timer.Stop()
}

func (t stdTimer) Reset(d time.Duration) bool {
	return t.timer.Reset(d)
}

func (t stdTimer) C() <-chan time.Time {
	return t.timer.C
}
