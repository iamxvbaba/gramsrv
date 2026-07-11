package rpc

import (
	"context"
	"time"
)

const defaultIdleDispatchMaxInterval = 5 * time.Second

type idleBackoff struct {
	base time.Duration
	max  time.Duration
	next time.Duration
}

func newIdleBackoff(base, max time.Duration) idleBackoff {
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}
	return idleBackoff{
		base: base,
		max:  max,
		next: base,
	}
}

func (b *idleBackoff) ActiveDelay() time.Duration {
	b.next = b.base
	return b.base
}

func (b *idleBackoff) IdleDelay() time.Duration {
	delay := b.next
	if b.next < b.max {
		b.next *= 2
		if b.next > b.max {
			b.next = b.max
		}
	}
	return delay
}

func runIdleBackoffLoop(ctx context.Context, interval, maxIdleInterval time.Duration, dispatch func(context.Context) bool) {
	if dispatch == nil {
		return
	}
	backoff := newIdleBackoff(interval, maxIdleInterval)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if dispatch(ctx) {
			// 有积压时立即继续 drain；interval 只用于空闲轮询。旧逻辑每个非空
			// batch 也固定等待 base，形成 batch/base 的人工吞吐上限。
			_ = backoff.ActiveDelay()
			timer.Reset(0)
			continue
		}
		timer.Reset(backoff.IdleDelay())
	}
}
