package mtprotoedge

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/transport"
)

const (
	physicalTransportClosedBit     = uint64(1) << 63
	physicalTransportGenerationMax = physicalTransportClosedBit - 1
)

// physicalTransportOwner separates the lifetime of one physical socket from
// the logical Conn generations that successively use it. state is either an
// open generation in [1, physicalTransportGenerationMax], or that generation
// with physicalTransportClosedBit set.
//
// Transfer and owner-close race through one atomic state transition. This is
// the critical property: when transfer wins, a stale generation can no longer
// close the socket; when close wins, no later generation can be published.
type physicalTransportOwner struct {
	raw transport.Conn

	// writeMu orders a completed send before generation transfer. CloseAny and
	// owner-close deliberately do not wait for it: raw.Close must be able to
	// interrupt a transport implementation blocked inside Send.
	writeMu sync.Mutex
	state   atomic.Uint64

	// binding identifies the logical Conn currently attached to the open
	// generation. Physical close fences that exact Conn before touching raw;
	// generation matching prevents a stale close from retiring a replacement.
	bindingMu        sync.Mutex
	boundGeneration  uint64
	boundLogicalConn *Conn

	closeDone chan struct{}
	closeErr  error
}

// physicalTransportLease is the capability owned by one logical Conn
// generation. It implements transport.Conn so exchange/protocol-error paths
// can use the initial lease from the moment serveConn starts.
type physicalTransportLease struct {
	owner      *physicalTransportOwner
	generation uint64
}

var _ transport.Conn = (*physicalTransportLease)(nil)

func newPhysicalTransportOwner(raw transport.Conn) (*physicalTransportOwner, *physicalTransportLease) {
	owner := &physicalTransportOwner{
		raw:       raw,
		closeDone: make(chan struct{}),
	}
	owner.state.Store(1)
	return owner, &physicalTransportLease{owner: owner, generation: 1}
}

// Transfer atomically hands the physical transport to the next logical Conn
// generation. Holding writeMu ensures every send admitted by the old lease has
// returned before the generation changes. CloseAny may still interrupt such a
// send and make the CAS fail, which is the required shutdown ordering.
func (l *physicalTransportLease) Transfer() (*physicalTransportLease, bool) {
	if l == nil || l.owner == nil || l.generation == 0 {
		return nil, false
	}
	owner := l.owner
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()

	if l.generation >= physicalTransportGenerationMax {
		return nil, false
	}
	if !owner.state.CompareAndSwap(l.generation, l.generation+1) {
		return nil, false
	}
	owner.bindingMu.Lock()
	if owner.boundGeneration == l.generation {
		owner.boundGeneration = l.generation + 1
		owner.boundLogicalConn = nil
	}
	owner.bindingMu.Unlock()
	return &physicalTransportLease{owner: owner, generation: l.generation + 1}, true
}

// IsCurrentOpen reports whether this lease is still the unique open owner.
// Callers use it at protocol publication barriers after a required write.
func (l *physicalTransportLease) IsCurrentOpen() bool {
	return l != nil && l.owner != nil && l.generation != 0 &&
		l.owner.state.Load() == l.generation
}

// bindLogicalConn publishes the logical Conn for this exact generation. If
// physical close already won, the new Conn is terminally fenced immediately
// and can never pass SessionManager publication checks.
func (l *physicalTransportLease) bindLogicalConn(c *Conn) bool {
	if l == nil || l.owner == nil || c == nil {
		if c != nil {
			c.beginTerminalShutdown()
		}
		return false
	}
	owner := l.owner
	owner.bindingMu.Lock()
	open := owner.state.Load() == l.generation
	if open {
		owner.boundGeneration = l.generation
		owner.boundLogicalConn = c
	}
	owner.bindingMu.Unlock()
	if !open {
		c.beginTerminalShutdown()
	}
	return open
}

// Send admits a write only while this lease is the current open generation.
// The lock also serializes quick ACK/protocol writes with the outbound actor.
func (l *physicalTransportLease) Send(ctx context.Context, b *bin.Buffer) error {
	return l.withCurrentWriter(func(raw transport.Conn) error {
		return raw.Send(ctx, b)
	})
}

// SendDeadline preserves the allocation-free fast path of compatTransportConn.
func (l *physicalTransportLease) SendDeadline(deadline time.Time, b *bin.Buffer) error {
	return l.withCurrentWriter(func(raw transport.Conn) error {
		if writer, ok := raw.(deadlineOutboundWriter); ok {
			return writer.SendDeadline(deadline, b)
		}
		ctx := context.Background()
		cancel := func() {}
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
		}
		defer cancel()
		return raw.Send(ctx, b)
	})
}

// SendDeadlineWithScratch forwards the globally budgeted codec scratch through the generation
// lease while preserving the same write-ownership barrier as SendDeadline.
func (l *physicalTransportLease) SendDeadlineWithScratch(deadline time.Time, b *bin.Buffer, scratch *[]byte) error {
	return l.withCurrentWriter(func(raw transport.Conn) error {
		if writer, ok := raw.(deadlineOutboundScratchWriter); ok {
			return writer.SendDeadlineWithScratch(deadline, b, scratch)
		}
		if writer, ok := raw.(deadlineOutboundWriter); ok {
			return writer.SendDeadline(deadline, b)
		}
		ctx := context.Background()
		cancel := func() {}
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
		}
		defer cancel()
		return raw.Send(ctx, b)
	})
}

// SendDeadlineWithScratchGuarded evaluates guard while holding the physical
// write-ownership lock, immediately before entering the raw writer. Conn-level
// checks performed before this call are insufficient: a quick ACK or protocol
// write may hold writeMu across a temporary-key expiry boundary. The guard must
// not close/fence the connection itself because that would re-enter transport
// shutdown while writeMu is held; callers handle its error after the lock drops.
func (l *physicalTransportLease) SendDeadlineWithScratchGuarded(deadline time.Time, b *bin.Buffer, scratch *[]byte, guard func() error) error {
	return l.withCurrentWriterGuarded(guard, func(raw transport.Conn) error {
		if writer, ok := raw.(deadlineOutboundScratchWriter); ok {
			return writer.SendDeadlineWithScratch(deadline, b, scratch)
		}
		if writer, ok := raw.(deadlineOutboundWriter); ok {
			return writer.SendDeadline(deadline, b)
		}
		ctx := context.Background()
		cancel := func() {}
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
		}
		defer cancel()
		return raw.Send(ctx, b)
	})
}

func (l *physicalTransportLease) withCurrentWriter(send func(transport.Conn) error) error {
	return l.withCurrentWriterGuarded(nil, send)
}

func (l *physicalTransportLease) withCurrentWriterGuarded(guard func() error, send func(transport.Conn) error) error {
	if l == nil || l.owner == nil || l.owner.raw == nil {
		return ErrConnClosed
	}
	owner := l.owner
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	if owner.state.Load() != l.generation {
		return ErrConnClosed
	}
	if guard != nil {
		if err := guard(); err != nil {
			return err
		}
	}
	return send(owner.raw)
}

// Recv is owned by serveConn rather than a logical Conn generation. It is a
// direct proxy so the read loop remains valid after ownership transfers.
func (l *physicalTransportLease) Recv(ctx context.Context, b *bin.Buffer) error {
	if l == nil || l.owner == nil || l.owner.raw == nil {
		return ErrConnClosed
	}
	return l.owner.raw.Recv(ctx, b)
}

// RecvDeadline preserves serveConn's direct-deadline fast path.
func (l *physicalTransportLease) RecvDeadline(deadline time.Time, b *bin.Buffer) error {
	if l == nil || l.owner == nil || l.owner.raw == nil {
		return ErrConnClosed
	}
	if receiver, ok := l.owner.raw.(deadlineReceiver); ok {
		return receiver.RecvDeadline(deadline, b)
	}
	ctx := context.Background()
	cancel := func() {}
	if !deadline.IsZero() {
		ctx, cancel = context.WithDeadline(ctx, deadline)
	}
	defer cancel()
	return l.owner.raw.Recv(ctx, b)
}

// Close closes the physical transport only if this lease still owns the
// current generation. A stale logical Conn therefore cannot close a socket
// already transferred to its replacement.
func (l *physicalTransportLease) Close() error {
	if l == nil || l.owner == nil || l.generation == 0 {
		return nil
	}
	owner := l.owner
	for {
		state := owner.state.Load()
		if state&physicalTransportClosedBit != 0 {
			return owner.waitClosed()
		}
		if state != l.generation {
			return nil
		}
		if owner.state.CompareAndSwap(state, state|physicalTransportClosedBit) {
			owner.fenceBoundGeneration(l.generation)
			return owner.closeRaw()
		}
	}
}

// startCloseAlreadyFenced is the non-reentrant close capability for the logical
// Conn that has already published terminal/lifecycle gates itself. It
// synchronously publishes the physical closed bit (so Transfer cannot win), then
// runs the potentially pathological raw.Close outside the RPC worker/flight
// handoff. It neither calls fenceBoundGeneration nor waits for a CloseAny that
// already won, avoiding both lifecycle reentry and close cycles.
func (l *physicalTransportLease) startCloseAlreadyFenced() {
	if l == nil || l.owner == nil || l.generation == 0 {
		return
	}
	owner := l.owner
	for {
		state := owner.state.Load()
		if state&physicalTransportClosedBit != 0 || state != l.generation {
			return
		}
		if owner.state.CompareAndSwap(state, state|physicalTransportClosedBit) {
			owner.bindingMu.Lock()
			if owner.boundGeneration == l.generation {
				owner.boundLogicalConn = nil
			}
			owner.bindingMu.Unlock()
			go func() { _ = owner.closeRaw() }()
			return
		}
	}
}

// CloseAny is the unconditional physical-socket capability retained by
// serveConn. It marks the owner closed before calling raw.Close and never waits
// for writeMu, so it can break a blocked Send or Recv.
func (o *physicalTransportOwner) CloseAny() error {
	if o == nil {
		return nil
	}
	for {
		state := o.state.Load()
		if state&physicalTransportClosedBit != 0 {
			return o.waitClosed()
		}
		if o.state.CompareAndSwap(state, state|physicalTransportClosedBit) {
			o.fenceBoundGeneration(state)
			return o.closeRaw()
		}
	}
}

func (o *physicalTransportOwner) fenceBoundGeneration(generation uint64) {
	o.bindingMu.Lock()
	var c *Conn
	if o.boundGeneration == generation {
		c = o.boundLogicalConn
		o.boundLogicalConn = nil
	}
	o.bindingMu.Unlock()
	if c != nil {
		c.beginTerminalShutdown()
	}
}

func (o *physicalTransportOwner) closeRaw() error {
	if o.raw != nil {
		o.closeErr = o.raw.Close()
	}
	close(o.closeDone)
	return o.closeErr
}

func (o *physicalTransportOwner) waitClosed() error {
	<-o.closeDone
	return o.closeErr
}

// Forward the optional compat-transport capabilities hidden by the lease.
// These keep frame-budget ownership and quick-ack semantics unchanged while
// serveConn operates on the initial lease instead of the raw transport.
func (l *physicalTransportLease) releaseInboundFrame() {
	if l == nil || l.owner == nil {
		return
	}
	if releaser, ok := l.owner.raw.(inboundFrameOwnershipReleaser); ok {
		releaser.releaseInboundFrame()
	}
}

func (l *physicalTransportLease) retainInboundFrameBytes(n int64) bool {
	if l == nil || l.owner == nil {
		return true
	}
	if retainer, ok := l.owner.raw.(inboundFrameBackingRetainer); ok {
		return retainer.retainInboundFrameBytes(n)
	}
	return true
}

func (l *physicalTransportLease) ConsumeQuickAckRequested() bool {
	if l == nil || l.owner == nil {
		return false
	}
	if quick, ok := l.owner.raw.(quickAckTransport); ok {
		return quick.ConsumeQuickAckRequested()
	}
	return false
}

func (l *physicalTransportLease) SendQuickAck(ctx context.Context, token uint32) error {
	return l.withCurrentWriter(func(raw transport.Conn) error {
		if quick, ok := raw.(quickAckTransport); ok {
			return quick.SendQuickAck(ctx, token)
		}
		return nil
	})
}

func (l *physicalTransportLease) SendQuickAckDeadline(deadline time.Time, token uint32) error {
	return l.withCurrentWriter(func(raw transport.Conn) error {
		if quick, ok := raw.(deadlineQuickAckTransport); ok {
			return quick.SendQuickAckDeadline(deadline, token)
		}
		if quick, ok := raw.(quickAckTransport); ok {
			ctx := context.Background()
			cancel := func() {}
			if !deadline.IsZero() {
				ctx, cancel = context.WithDeadline(ctx, deadline)
			}
			defer cancel()
			return quick.SendQuickAck(ctx, token)
		}
		return nil
	})
}
