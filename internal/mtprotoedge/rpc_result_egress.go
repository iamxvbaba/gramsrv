package mtprotoedge

import (
	"context"
	"errors"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
)

const (
	rpcResultGZIPMinBytes        = 4 << 10
	rpcResultGZIPMaxInputBytes   = (10 << 20) - 1 // gotd client decompression hard limit.
	rpcResultGZIPMinSavedBytes   = 1 << 10
	rpcResultGZIPMinSavedDivisor = 12 // Require roughly 8.3% reduction.
	rpcResultGZIPConcurrency     = 8
	rpcDeliveryHookConcurrency   = 8
	rpcDeliveryHookQueueSize     = 1024
)

var rpcResultGZIPSlots = make(chan struct{}, rpcResultGZIPConcurrency)

var defaultRPCDeliveryHookExecutor = newRPCDeliveryHookExecutor(rpcDeliveryHookConcurrency, rpcDeliveryHookQueueSize)

// ErrRPCDeliveryHookCapacity means an RPC result with a delivery-dependent
// transition cannot reserve reliable executor capacity. The result must not be
// written: its connection is fenced and the immutable result stays replayable.
var ErrRPCDeliveryHookCapacity = errors.New("mtproto rpc delivery hook capacity exhausted")

type rpcDeliveryHookTicketState uint32

const (
	rpcDeliveryHookTicketReserved rpcDeliveryHookTicketState = iota + 1
	rpcDeliveryHookTicketQueued
	rpcDeliveryHookTicketReleased
	rpcDeliveryHookTicketDone
)

type rpcDeliveryHookTicket struct {
	executor *rpcDeliveryHookExecutor
	state    atomic.Uint32
	job      rpcDeliveryHookJob
}

type rpcDeliveryHookJob struct {
	next   *rpcDeliveryHookJob
	ticket *rpcDeliveryHookTicket
	fn     func()
}

// rpcDeliveryHookExecutor has process lifetime. Capacity bounds queued plus
// running hooks; every physical write reserves a ticket before admission. A
// successful writer therefore performs only one short O(1) queue append and
// never waits for capacity or hook work. Failed writes release their ticket,
// while the shared logical coordinator remains eligible for a later replay.
type rpcDeliveryHookExecutor struct {
	slots chan struct{}

	mu   sync.Mutex
	cond *sync.Cond
	head *rpcDeliveryHookJob
	tail *rpcDeliveryHookJob

	panics atomic.Uint64
}

func newRPCDeliveryHookExecutor(workers, capacity int) *rpcDeliveryHookExecutor {
	if workers <= 0 {
		workers = 1
	}
	if capacity < workers {
		capacity = workers
	}
	e := &rpcDeliveryHookExecutor{slots: make(chan struct{}, capacity)}
	e.cond = sync.NewCond(&e.mu)
	for range workers {
		go e.run()
	}
	return e
}

func (e *rpcDeliveryHookExecutor) reserve() (*rpcDeliveryHookTicket, bool) {
	if e == nil {
		return nil, false
	}
	select {
	case e.slots <- struct{}{}:
		ticket := &rpcDeliveryHookTicket{executor: e}
		ticket.state.Store(uint32(rpcDeliveryHookTicketReserved))
		return ticket, true
	default:
		return nil, false
	}
}

func (t *rpcDeliveryHookTicket) release() {
	if t == nil || t.executor == nil || !t.state.CompareAndSwap(
		uint32(rpcDeliveryHookTicketReserved), uint32(rpcDeliveryHookTicketReleased),
	) {
		return
	}
	<-t.executor.slots
}

func (t *rpcDeliveryHookTicket) submit(fn func()) bool {
	if t == nil || t.executor == nil || fn == nil || !t.state.CompareAndSwap(
		uint32(rpcDeliveryHookTicketReserved), uint32(rpcDeliveryHookTicketQueued),
	) {
		return false
	}
	t.job.ticket = t
	t.job.fn = fn
	t.executor.enqueue(&t.job)
	return true
}

func (e *rpcDeliveryHookExecutor) enqueue(job *rpcDeliveryHookJob) {
	e.mu.Lock()
	if e.tail == nil {
		e.head = job
	} else {
		e.tail.next = job
	}
	e.tail = job
	e.cond.Signal()
	e.mu.Unlock()
}

func (e *rpcDeliveryHookExecutor) run() {
	for {
		e.mu.Lock()
		for e.head == nil {
			e.cond.Wait()
		}
		job := e.head
		e.head = job.next
		if e.head == nil {
			e.tail = nil
		}
		job.next = nil
		e.mu.Unlock()
		e.runOne(job)
	}
}

func (e *rpcDeliveryHookExecutor) runOne(job *rpcDeliveryHookJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			e.panics.Add(1)
			log.Printf("mtprotoedge: rpc delivery hook panic: %v\n%s", recovered, debug.Stack())
		}
		if job != nil && job.ticket != nil {
			job.ticket.state.Store(uint32(rpcDeliveryHookTicketDone))
			<-e.slots
		}
	}()
	if job != nil && job.fn != nil {
		job.fn()
	}
}

// encodeAdaptiveRPCResultInner returns either the original layer-specific TL
// object or one complete gzip_packed object. Compression is CPU bounded and is
// retained only when it materially reduces the non-preemptible transport frame.
func encodeAdaptiveRPCResultInner(ctx context.Context, stop <-chan struct{}, inner []byte) ([]byte, bool, error) {
	if len(inner) < rpcResultGZIPMinBytes || len(inner) > rpcResultGZIPMaxInputBytes {
		return inner, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case rpcResultGZIPSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-stop:
		return nil, false, ErrConnClosed
	}
	defer func() { <-rpcResultGZIPSlots }()

	var packed bin.Buffer
	if err := (proto.GZIP{Data: inner}).Encode(&packed); err != nil {
		return nil, false, err
	}
	saved := len(inner) - packed.Len()
	required := max(rpcResultGZIPMinSavedBytes, len(inner)/rpcResultGZIPMinSavedDivisor)
	if saved < required {
		return inner, false, nil
	}
	return packed.Raw(), true, nil
}

// rpcResultPriority is protocol scheduling metadata, not handler business
// behavior. Difference/state responses converge the update state, while the
// dialogs+pinned pair converges the initial chat list in both TDesktop and
// Android. These bootstrap barriers must pass background prefetch regardless of
// platform or their own encoded size.
func rpcResultPriority(method string, encoded *encodedOutboundMessage) outboundPriority {
	base := method
	if i := strings.IndexByte(base, '#'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "updates.getDifference", "updates.getChannelDifference", "updates.getState",
		"messages.getDialogs", "messages.getPinnedDialogs":
		return outboundPriorityCritical
	}
	return classifyOutboundPriority(encoded, false)
}

func (p outboundPriority) String() string {
	switch p {
	case outboundPriorityCritical:
		return "convergence"
	case outboundPriorityBulk:
		return "bulk"
	case outboundPriorityControl:
		return "control"
	default:
		return "normal"
	}
}
