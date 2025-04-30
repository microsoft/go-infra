package appinsights

import (
	"time"

	"code.cloudfoundry.org/clock/fakeclock"
)

var fakeClock *fakeclock.FakeClock

func mockClockAt(timestamp time.Time) {
	if timestamp.IsZero() {
		timestamp = time.Now().Round(time.Minute)
	}
	fakeClock = fakeclock.NewFakeClock(timestamp)
	now = func() time.Time {
		return fakeClock.Now()
	}
	sleep = fakeClock.Sleep
	newTimer = func(d time.Duration) timer {
		return fakeClock.NewTimer(d)
	}
}

func mockClock() {
	mockClockAt(time.Time{})
}

func resetClock() {
	fakeClock = nil
	now = time.Now
	sleep = time.Sleep
	newTimer = newStdTimer
}

func slowTick(seconds int) {
	const delay = time.Millisecond * time.Duration(5)

	// Sleeps in tests are evil, but with all the async nonsense going
	// on, no callbacks, and minimal control of the clock, I'm not
	// really sure I have another choice.

	time.Sleep(delay)
	for range seconds {
		fakeClock.Increment(time.Second)
		time.Sleep(delay)
	}
}
