package rpc

import (
	"github.com/iamxvbaba/td/clock"
	"time"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func (c fixedClock) Timer(d time.Duration) clock.Timer {
	return clock.System.Timer(d)
}

func (c fixedClock) Ticker(d time.Duration) clock.Ticker {
	return clock.System.Ticker(d)
}
