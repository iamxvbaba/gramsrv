package mtprotoedge

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
)

const (
	defaultOutboundWriteMaxBytes = int64(512 << 20)
	defaultOutboundScratchPool   = 256
)

// outboundScratchPool bounds and reuses the encrypted wire buffer across connections. A lease
// reserves wire + codec/obfuscation copies plus their bounded transport overhead, then shrinks to
// the actual retained capacity while idle in the bounded pool. Large one-off frames are dropped on
// return. This removes attacker-warmable per-Conn MiB buffers without returning to an unbounded
// allocation-per-message design.
type outboundScratchPool struct {
	budget *outboundTrackedBudget
	idle   chan *outboundScratch
}

type outboundScratch struct {
	wire     bin.Buffer
	codec    []byte
	reserved int
}

func newOutboundScratchPool(maxBytes int64) *outboundScratchPool {
	if maxBytes <= 0 {
		maxBytes = defaultOutboundWriteMaxBytes
	}
	return &outboundScratchPool{
		budget: newOutboundTrackedBudget(maxBytes),
		idle:   make(chan *outboundScratch, defaultOutboundScratchPool),
	}
}

func (p *outboundScratchPool) acquire(ctx context.Context, stop <-chan struct{}, wireBytes int) (*outboundScratch, error) {
	return p.acquireUntil(ctx, stop, wireBytes, time.Time{})
}

func (p *outboundScratchPool) acquireUntil(ctx context.Context, stop <-chan struct{}, wireBytes int, deadline time.Time) (*outboundScratch, error) {
	if p == nil || wireBytes <= 0 {
		return nil, ErrOutboundMessageTooLarge
	}
	peak := wireBytes*3 + 2*maxCompatPacketOverhead
	if peak < wireBytes { // int overflow
		return nil, ErrOutboundMessageTooLarge
	}

	var scratch *outboundScratch
	select {
	case scratch = <-p.idle:
	default:
	}
	if scratch == nil {
		if err := p.budget.waitReserveUntil(ctx, stop, peak, deadline); err != nil {
			return nil, err
		}
		return &outboundScratch{wire: bin.Buffer{Buf: make([]byte, wireBytes)}, reserved: peak}, nil
	}

	if cap(scratch.wire.Buf) >= wireBytes {
		if extra := peak - scratch.reserved; extra > 0 {
			if err := p.budget.waitReserveUntil(ctx, stop, extra, deadline); err != nil {
				p.putIdle(scratch)
				return nil, err
			}
			scratch.reserved += extra
		}
		scratch.wire.Buf = scratch.wire.Buf[:wireBytes]
		return scratch, nil
	}

	// The old slice is no longer reachable after clearing it; return that retained charge before
	// waiting for a larger lease, otherwise old+peak may exceed the budget and deadlock a resize
	// that would fit after replacement.
	old := scratch.reserved
	scratch.wire.Buf = nil
	scratch.codec = nil
	scratch.reserved = 0
	p.budget.release(old)
	if err := p.budget.waitReserveUntil(ctx, stop, peak, deadline); err != nil {
		return nil, err
	}
	scratch.wire.Buf = make([]byte, wireBytes)
	scratch.reserved = peak
	return scratch, nil
}

func (p *outboundScratchPool) release(scratch *outboundScratch) {
	if p == nil || scratch == nil {
		return
	}
	retained := cap(scratch.wire.Buf) + cap(scratch.codec)
	if retained > maxRetainedConnBuffer {
		p.budget.release(scratch.reserved)
		scratch.wire.Buf = nil
		scratch.codec = nil
		scratch.reserved = 0
		return
	}
	if scratch.reserved > retained {
		p.budget.release(scratch.reserved - retained)
		scratch.reserved = retained
	}
	scratch.wire.Buf = scratch.wire.Buf[:0]
	scratch.codec = scratch.codec[:0]
	p.putIdle(scratch)
}

func (p *outboundScratchPool) putIdle(scratch *outboundScratch) {
	select {
	case p.idle <- scratch:
	default:
		p.budget.release(scratch.reserved)
		scratch.wire.Buf = nil
		scratch.codec = nil
		scratch.reserved = 0
	}
}

func (p *outboundScratchPool) snapshot() int64 {
	if p == nil {
		return 0
	}
	return p.budget.snapshot()
}
