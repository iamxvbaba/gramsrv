package mtprotoedge

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
)

var (
	// ErrConnClosed 表示连接的出站 actor 已关闭。
	ErrConnClosed = errors.New("mtproto connection closed")
	// ErrOutboundQueueFull 表示 best-effort update push 未能在预算内进入出站队列。
	ErrOutboundQueueFull = errors.New("mtproto outbound queue full")
	// ErrOutboundTrackedBudget 表示消息无法进入 Server 级 resend tracking 预算。可靠 RPC
	// 响应会终止连接让客户端重试；best-effort update 仅丢弃加速推送，由 difference 恢复。
	ErrOutboundTrackedBudget   = errors.New("mtproto outbound tracked byte budget exhausted")
	ErrOutboundMessageTooLarge = errors.New("mtproto outbound message exceeds transport frame limit")
)

const (
	// 原实现为每条 Conn eager 分配 1024 + 256 个 outboundOp 槽；在大连接数下仅空队列
	// backing 就占据大量常驻内存。默认缩至 128 + 32，仍覆盖 TDesktop 启动/推送突发，
	// 慢消费者继续由 best-effort timeout + durable difference 降级。
	defaultOutboundQueueSize         = 128
	defaultOutboundControlQueueSize  = 32
	defaultOutboundCriticalQueueSize = 16
	defaultOutboundBulkQueueSize     = 16
	defaultOutboundTrackedMaxBytes   = int64(512 << 20) // 512 MiB / Server
	defaultOutboundControlMaxBytes   = int64(64 << 20)  // ack/state/resend vectors / Server
	// requiredControlMaxWait bounds protocol barriers such as new_session_created from
	// the beginning of encoding through the completed physical write. These frames gate
	// subsequent session state transitions, so timing out must close the connection instead
	// of degrading to the best-effort control path.
	requiredControlMaxWait = 5 * time.Second

	maxTrackedServerMsgIDs = 4096
	maxTrackedAckedMsgIDs  = 1024
	// maxTrackedServerBytes 是 pending（已发送待 ack、用于 resend）总 body 字节上限。
	// 与 maxTrackedServerMsgIDs 并列：客户端从不 ack 时，大响应体按字节滚动丢弃，
	// 防 pending 被「4096 条 × 大 body」撑爆。
	maxTrackedServerBytes = 64 << 20 // 64 MiB
	// Encrypted transport adds auth-key/msg-key, plaintext headers, randomized
	// padding and codec framing. Reject before creating the two encryption buffers.
	maxOutboundBodyBytes = maxTransportMessageSize - (2 << 10)
)

type outboundOpKind byte

const (
	outboundSend outboundOpKind = iota + 1
	outboundAck
	outboundQueryState
	outboundResend
	outboundResendByRequest
)

type outboundPriority uint8

const (
	outboundPriorityNormal outboundPriority = iota
	outboundPriorityCritical
	outboundPriorityBulk
	outboundPriorityControl
)

const (
	// Large responses are scheduled separately so a startup prefetch cannot sit
	// ahead of an already-prepared session convergence result. The threshold is
	// applied after layer conversion and adaptive gzip.
	bulkOutboundThreshold = 64 << 10
	maxOrdinaryBeforeBulk = 16
)

type outboundOp struct {
	kind     outboundOpKind
	control  bool
	priority outboundPriority
	ctx      context.Context
	msgType  proto.MessageType
	msg      bin.Encoder
	encoded  *encodedOutboundMessage
	// reservedBytes accounts for the encoded body while it is queued. For a
	// content frame the reservation is transferred to resend tracking after a
	// successful write; every other terminal path releases it exactly once.
	reservedBytes     int
	reservationBudget *outboundTrackedBudget
	ids               []int64
	reqMsgID          int64
	enqueuedAt        time.Time
	done              chan outboundResult
	// terminal is owned by the outbound actor after successful queue admission.
	// It resolves detached RPC-result ownership on every physical terminal path.
	terminal func(error)
}

// outboundBodyReservation bridges the only gap between producing an immutable
// encoded body and handing it to the outbound actor. Exact RPC results acquire
// this retained-byte reservation before releasing the process-wide encode slot,
// so completed large bodies can never accumulate unaccounted in RPC workers.
// Ownership is transferred into an outboundOp or released by the producer on
// every earlier failure. A failed queue admission may roll it back exactly once;
// the synchronized release request prevents watchdog/rollback resurrection.
type outboundBodyReservation struct {
	mu               sync.Mutex
	budget           *outboundTrackedBudget
	bytes            int
	taken            bool
	releaseRequested bool
}

func (r *outboundBodyReservation) release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.bytes <= 0 {
		// The actor may already own the raw reservation while queue admission is
		// still in progress. Remember a racing watchdog/producer release so a
		// later admission rollback cannot restore bytes after that owner is gone.
		if r.taken {
			r.releaseRequested = true
		}
		r.mu.Unlock()
		return
	}
	bytes := r.bytes
	budget := r.budget
	r.bytes = 0
	r.budget = nil
	r.mu.Unlock()
	budget.release(bytes)
}

func (r *outboundBodyReservation) take(encoded *encodedOutboundMessage) (outboundOp, error) {
	if r == nil {
		return outboundOp{}, errors.New("invalid outbound body reservation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.budget == nil || r.bytes <= 0 || r.taken || r.releaseRequested || encoded == nil || len(encoded.body) != r.bytes {
		return outboundOp{}, errors.New("invalid outbound body reservation")
	}
	op := outboundOp{
		kind:              outboundSend,
		msgType:           proto.MessageServerResponse,
		encoded:           encoded,
		priority:          classifyOutboundPriority(encoded, false),
		reservedBytes:     r.bytes,
		reservationBudget: r.budget,
	}
	r.bytes = 0
	r.budget = nil
	r.taken = true
	return op, nil
}

// reclaim moves an op reservation back to the producer when queue admission
// failed before the actor could observe the op. This preserves accounting until
// the producer has retained the immutable body elsewhere (usually completed
// cache) and its deferred release runs.
func (r *outboundBodyReservation) reclaim(op *outboundOp) bool {
	if r == nil || op == nil || op.reservedBytes <= 0 || op.reservationBudget == nil {
		return false
	}
	r.mu.Lock()
	if r.bytes != 0 || r.budget != nil || !r.taken {
		r.mu.Unlock()
		return false
	}
	bytes := op.reservedBytes
	budget := op.reservationBudget
	op.reservedBytes = 0
	op.reservationBudget = nil
	r.taken = false
	if r.releaseRequested {
		r.mu.Unlock()
		budget.release(bytes)
		return true
	}
	r.bytes = bytes
	r.budget = budget
	r.mu.Unlock()
	return true
}

type encodedOutboundMessage struct {
	body     []byte
	typeID   uint32
	reqMsgID int64
	priority outboundPriority
	delivery *rpcResultDelivery
	// layerInvariant is set only from a closed Go-type proof for an mt.* leaf or
	// an rpc_result whose complete inner value is an invariant mt.* service
	// result. A constructor ID alone is insufficient proof because RPC envelopes
	// and application schemas can contain nested/profiled values.
	layerInvariant bool
	// layer binds already-prepared TL bytes to the exact generated profile that
	// produced them. It is intentionally absent for MTProto control envelopes;
	// a bound value must never be transcoded or sent to another profile.
	layer             *outboundLayerBinding
	compressed        bool
	uncompressedBytes int
}

type rpcResultDeliveryState uint32

const (
	rpcResultDeliveryPrepared rpcResultDeliveryState = iota + 1
	rpcResultDeliveryQueued
	rpcResultDeliveryWriting
	rpcResultDeliveryReplayable
	rpcResultDeliveryDelivered
)

type rpcResultDelivery struct {
	state atomic.Uint32
	mu    sync.Mutex
	// targetReqMsgID may change only before the outbound actor enters writing.
	// writtenReqMsgID is the actor's immutable snapshot for the physical frame.
	targetReqMsgID  int64
	writtenReqMsgID int64
	coordinator     *rpcResultDeliveryCoordinator
	hookTicket      *rpcDeliveryHookTicket
	// attemptHook is connection-local replay state. Unlike the coordinator's
	// logical-result hook it must run only when this particular physical attempt
	// succeeds, even if another replay of the same cached result wins first.
	attemptHook func()
}

type rpcResultDeliveryHookState uint8

const (
	rpcResultDeliveryHookUnset rpcResultDeliveryHookState = iota
	rpcResultDeliveryHookPending
	rpcResultDeliveryHookClaimed
	rpcResultDeliveryHookInProgress
	rpcResultDeliveryHookDone
)

// rpcResultDeliveryCoordinator is logical-result state. Equivalent old/new
// req_msg_id representations and every replay share it, while each physical
// attempt owns a separate rpcResultDelivery state machine and hook ticket.
type rpcResultDeliveryCoordinator struct {
	mu      sync.Mutex
	fn      func()
	state   rpcResultDeliveryHookState
	claim   *rpcResultDeliveryHookClaim
	changed chan struct{}
	// deferredToReplay is sticky while a successful initConnection retarget owns
	// the logical hook. The ordinary source terminal may mark physical delivery,
	// but only the alias restore barrier may claim the hook. On terminal alias
	// failure the flag is released so a later completed-cache replay can retry.
	deferredToReplay bool
}

// rpcResultDeliveryHookClaim is an ownership token, not merely the hook
// function. Pointer identity prevents an abandoned claimed hook from running
// after a replacement replay has acquired a newer claim. The coordinator keeps
// claimed and in-progress distinct so another physical replay can wait for the
// exact logical transition instead of mistaking ownership for completion.
type rpcResultDeliveryHookClaim struct {
	coordinator *rpcResultDeliveryCoordinator
	fn          func()
}

func newRPCResultDelivery(reqMsgID int64, coordinator ...*rpcResultDeliveryCoordinator) *rpcResultDelivery {
	c := &rpcResultDeliveryCoordinator{changed: make(chan struct{})}
	if len(coordinator) > 0 && coordinator[0] != nil {
		c = coordinator[0]
	}
	d := &rpcResultDelivery{coordinator: c}
	if reqMsgID != 0 {
		d.targetReqMsgID = reqMsgID
	}
	d.state.Store(uint32(rpcResultDeliveryPrepared))
	return d
}

func (c *rpcResultDeliveryCoordinator) setHook(fn func()) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	if c.fn == nil && c.state != rpcResultDeliveryHookDone {
		c.fn = fn
		c.state = rpcResultDeliveryHookPending
		c.signalLocked()
	}
	c.mu.Unlock()
}

func (c *rpcResultDeliveryCoordinator) pendingHook() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	pending := c.fn != nil && c.state != rpcResultDeliveryHookDone
	c.mu.Unlock()
	return pending
}

func (c *rpcResultDeliveryCoordinator) pendingAsyncHook() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	pending := c.fn != nil && c.state == rpcResultDeliveryHookPending && !c.deferredToReplay
	c.mu.Unlock()
	return pending
}

func (c *rpcResultDeliveryCoordinator) signalLocked() {
	if c.changed == nil {
		c.changed = make(chan struct{})
		return
	}
	close(c.changed)
	if c.state == rpcResultDeliveryHookDone {
		c.changed = nil
	} else {
		c.changed = make(chan struct{})
	}
}

func (c *rpcResultDeliveryCoordinator) claimLocked(allowDeferred bool) *rpcResultDeliveryHookClaim {
	if c.fn == nil || c.state != rpcResultDeliveryHookPending ||
		(c.deferredToReplay && !allowDeferred) {
		return nil
	}
	claim := &rpcResultDeliveryHookClaim{coordinator: c, fn: c.fn}
	c.claim = claim
	c.state = rpcResultDeliveryHookClaimed
	if allowDeferred {
		c.deferredToReplay = false
	}
	c.signalLocked()
	return claim
}

func (c *rpcResultDeliveryCoordinator) claimAsyncDeliveredHook() *rpcResultDeliveryHookClaim {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	claim := c.claimLocked(false)
	c.mu.Unlock()
	return claim
}

// claimReplayDeliveredHook waits for an existing claim to finish, or acquires
// the pending hook itself. allowDeferred is reserved for the initConnection
// alias that installed the sticky retarget deferral. Every state transition
// closes changed, giving waiters a concrete happens-before edge to Done.
func (c *rpcResultDeliveryCoordinator) claimReplayDeliveredHook(
	ctx context.Context,
	allowDeferred bool,
) (*rpcResultDeliveryHookClaim, error) {
	if c == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if c.fn == nil || c.state == rpcResultDeliveryHookDone {
			c.mu.Unlock()
			return nil, nil
		}
		if claim := c.claimLocked(allowDeferred); claim != nil {
			c.mu.Unlock()
			return claim, nil
		}
		changed := c.changed
		if changed == nil {
			changed = make(chan struct{})
			c.changed = changed
		}
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			c.mu.Lock()
			done := c.fn == nil || c.state == rpcResultDeliveryHookDone
			c.mu.Unlock()
			if done {
				return nil, nil
			}
			return nil, ctx.Err()
		}
	}
}

func (c *rpcResultDeliveryCoordinator) releaseDeferredHook() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.deferredToReplay {
		c.deferredToReplay = false
		c.signalLocked()
	}
	c.mu.Unlock()
}

func (c *rpcResultDeliveryCoordinator) deferHookToReplay() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.state != rpcResultDeliveryHookDone && !c.deferredToReplay {
		c.deferredToReplay = true
		c.signalLocked()
	}
	c.mu.Unlock()
}

func (c *rpcResultDeliveryCoordinator) hookState() rpcResultDeliveryHookState {
	if c == nil {
		return rpcResultDeliveryHookUnset
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	return state
}

func (claim *rpcResultDeliveryHookClaim) run() {
	if claim == nil || claim.coordinator == nil {
		return
	}
	c := claim.coordinator
	c.mu.Lock()
	if c.claim != claim || c.state != rpcResultDeliveryHookClaimed {
		c.mu.Unlock()
		return
	}
	c.state = rpcResultDeliveryHookInProgress
	c.signalLocked()
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.claim == claim && c.state == rpcResultDeliveryHookInProgress {
			c.claim = nil
			c.fn = nil
			c.deferredToReplay = false
			c.state = rpcResultDeliveryHookDone
			c.signalLocked()
		}
		c.mu.Unlock()
	}()
	if claim.fn != nil {
		claim.fn()
	}
}

// abandon makes a hook claim retryable only while its function has not begun.
// Once InProgress is visible, at-most-once semantics forbid another replay from
// guessing whether a non-cooperative callback partially applied its effects.
func (claim *rpcResultDeliveryHookClaim) abandon() bool {
	if claim == nil || claim.coordinator == nil {
		return false
	}
	c := claim.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claim != claim || c.state != rpcResultDeliveryHookClaimed {
		return false
	}
	c.claim = nil
	c.state = rpcResultDeliveryHookPending
	c.signalLocked()
	return true
}

func (m *encodedOutboundMessage) deliveryState() rpcResultDeliveryState {
	if m == nil || m.delivery == nil {
		return 0
	}
	return rpcResultDeliveryState(m.delivery.state.Load())
}

func (m *encodedOutboundMessage) markQueued() {
	if m == nil || m.delivery == nil {
		return
	}
	m.delivery.mu.Lock()
	if m.deliveryState() == rpcResultDeliveryPrepared {
		m.delivery.state.Store(uint32(rpcResultDeliveryQueued))
	}
	m.delivery.mu.Unlock()
}

// beginWriting linearizes retargeting against the sole outbound actor. The
// returned req_msg_id is immutable for this physical write.
func (m *encodedOutboundMessage) beginWriting() int64 {
	if m == nil || m.delivery == nil {
		if m == nil {
			return 0
		}
		return m.reqMsgID
	}
	m.delivery.mu.Lock()
	if m.delivery.targetReqMsgID == 0 {
		m.delivery.targetReqMsgID = m.reqMsgID
	}
	m.delivery.writtenReqMsgID = m.delivery.targetReqMsgID
	m.delivery.state.Store(uint32(rpcResultDeliveryWriting))
	target := m.delivery.writtenReqMsgID
	m.delivery.mu.Unlock()
	return target
}

func (m *encodedOutboundMessage) tryRetarget(reqMsgID int64) bool {
	if m == nil || m.delivery == nil || reqMsgID == 0 {
		return false
	}
	m.delivery.mu.Lock()
	state := m.deliveryState()
	if state != rpcResultDeliveryPrepared && state != rpcResultDeliveryQueued {
		m.delivery.mu.Unlock()
		return false
	}
	// Retarget and logical-hook deferral are one transition with beginWriting.
	// If admission already reserved a global executor ticket, detach it while the
	// actor is still unable to enter Writing; the alias barrier owns the hook now.
	coordinator := m.delivery.coordinator
	if coordinator != nil {
		coordinator.deferHookToReplay()
	}
	ticket := m.delivery.hookTicket
	m.delivery.hookTicket = nil
	m.delivery.targetReqMsgID = reqMsgID
	m.delivery.mu.Unlock()
	if ticket != nil {
		ticket.release()
	}
	return true
}

func (m *encodedOutboundMessage) writtenRequestID() int64 {
	if m == nil || m.delivery == nil {
		if m == nil {
			return 0
		}
		return m.reqMsgID
	}
	m.delivery.mu.Lock()
	id := m.delivery.writtenReqMsgID
	if id == 0 {
		id = m.delivery.targetReqMsgID
	}
	m.delivery.mu.Unlock()
	if id == 0 {
		return m.reqMsgID
	}
	return id
}

func (m *encodedOutboundMessage) markReplayable() {
	if m == nil || m.delivery == nil {
		return
	}
	m.delivery.mu.Lock()
	if m.deliveryState() != rpcResultDeliveryDelivered {
		m.delivery.state.Store(uint32(rpcResultDeliveryReplayable))
	}
	ticket := m.delivery.hookTicket
	m.delivery.hookTicket = nil
	m.delivery.attemptHook = nil
	m.delivery.mu.Unlock()
	if ticket != nil {
		ticket.release()
	}
}

func (m *encodedOutboundMessage) markDelivered() {
	if m == nil || m.delivery == nil {
		return
	}
	m.delivery.mu.Lock()
	m.delivery.state.Store(uint32(rpcResultDeliveryDelivered))
	ticket := m.delivery.hookTicket
	m.delivery.hookTicket = nil
	coordinator := m.delivery.coordinator
	attemptHook := m.delivery.attemptHook
	m.delivery.attemptHook = nil
	m.delivery.mu.Unlock()
	logicalClaim := coordinator.claimAsyncDeliveredHook()
	if logicalClaim == nil && attemptHook == nil {
		if ticket != nil {
			ticket.release()
		}
		return
	}
	if ticket == nil {
		// Production write paths reserve before admission. This fallback preserves
		// old direct-test callers without ever waiting in the socket writer.
		ticket, _ = defaultRPCDeliveryHookExecutor.reserve()
	}
	if ticket == nil {
		logicalClaim.abandon()
		log.Printf("mtprotoedge: delivered rpc_result without reserved delivery-hook capacity")
		return
	}
	if !ticket.submit(func() {
		if logicalClaim != nil {
			logicalClaim.run()
		}
		if attemptHook != nil {
			attemptHook()
		}
	}) {
		logicalClaim.abandon()
		ticket.release()
		log.Printf("mtprotoedge: reserved rpc delivery hook ticket could not be submitted")
	}
}

func (m *encodedOutboundMessage) setDeliveryHook(fn func()) {
	if m == nil || m.delivery == nil || m.delivery.coordinator == nil {
		return
	}
	m.delivery.coordinator.setHook(fn)
}

// pendingLogicalDeliveryHook reports whether this logical rpc_result still owns
// delivery-gated state from its original successful handler. Cached replay uses
// this before physical admission so it can install a per-Conn scheduler barrier
// without reserving the process-wide asynchronous hook executor.
func (m *encodedOutboundMessage) pendingLogicalDeliveryHook() bool {
	return m != nil && m.delivery != nil && m.delivery.coordinator != nil &&
		m.delivery.coordinator.pendingHook()
}

// claimLogicalDeliveryHook transfers the logical post-response hook only after
// a replay attempt has physically reached the replacement stream. Calling it
// before markDelivered makes the latter a pure attempt-state transition, so no
// global hook ticket is required on the ordered replay path.
func (m *encodedOutboundMessage) claimLogicalDeliveryHook(
	ctx context.Context,
	allowDeferred bool,
) (*rpcResultDeliveryHookClaim, error) {
	if m == nil || m.delivery == nil || m.delivery.coordinator == nil {
		return nil, nil
	}
	return m.delivery.coordinator.claimReplayDeliveredHook(ctx, allowDeferred)
}

func (m *encodedOutboundMessage) releaseDeferredLogicalDeliveryHook() {
	if m != nil && m.delivery != nil && m.delivery.coordinator != nil {
		m.delivery.coordinator.releaseDeferredHook()
	}
}

func (m *encodedOutboundMessage) setAttemptDeliveryHook(fn func()) {
	if m == nil || m.delivery == nil || fn == nil {
		return
	}
	m.delivery.mu.Lock()
	if m.delivery.attemptHook == nil {
		m.delivery.attemptHook = fn
	}
	m.delivery.mu.Unlock()
}

func (m *encodedOutboundMessage) prepareDeliveryHook(executor *rpcDeliveryHookExecutor) error {
	if m == nil || m.delivery == nil {
		return nil
	}
	m.delivery.mu.Lock()
	pending := m.delivery.attemptHook != nil || (m.delivery.coordinator != nil && m.delivery.coordinator.pendingAsyncHook())
	if m.delivery.hookTicket != nil || !pending {
		m.delivery.mu.Unlock()
		return nil
	}
	ticket, ok := executor.reserve()
	if !ok {
		m.delivery.mu.Unlock()
		return ErrRPCDeliveryHookCapacity
	}
	pending = m.delivery.attemptHook != nil || (m.delivery.coordinator != nil && m.delivery.coordinator.pendingAsyncHook())
	if !pending {
		m.delivery.mu.Unlock()
		ticket.release()
		return nil
	}
	m.delivery.hookTicket = ticket
	m.delivery.mu.Unlock()
	return nil
}

func cloneRPCResultForRequest(encoded *encodedOutboundMessage, reqMsgID int64, shareDelivery bool) (*encodedOutboundMessage, error) {
	if encoded == nil || encoded.typeID != proto.ResultTypeID || len(encoded.body) < 12 || reqMsgID == 0 {
		return nil, errors.New("invalid rpc_result retarget")
	}
	body := encoded.body
	if int64(binary.LittleEndian.Uint64(body[4:12])) != reqMsgID {
		body = append([]byte(nil), body...)
		binary.LittleEndian.PutUint64(body[4:12], uint64(reqMsgID))
	}
	var coordinator *rpcResultDeliveryCoordinator
	if encoded.delivery != nil {
		coordinator = encoded.delivery.coordinator
	}
	delivery := newRPCResultDelivery(reqMsgID, coordinator)
	if shareDelivery {
		delivery = encoded.delivery
	}
	return &encodedOutboundMessage{
		body: body, typeID: encoded.typeID, reqMsgID: reqMsgID,
		priority: encoded.priority, delivery: delivery, compressed: encoded.compressed,
		layer: encoded.layer, layerInvariant: encoded.layerInvariant,
		uncompressedBytes: encoded.uncompressedBytes,
	}, nil
}

// cloneRPCResultForRequestReserved charges the target connection's retained-body
// budget before a retarget copy can exist. Even the same-req_id zero-copy case
// needs a reservation: the replay/rewrap owner may outlive cache eviction while
// it is queued, so its immutable body must remain independently pinned.
func (c *Conn) cloneRPCResultForRequestReserved(
	encoded *encodedOutboundMessage,
	reqMsgID int64,
	shareDelivery bool,
) (*encodedOutboundMessage, *outboundBodyReservation, error) {
	if c == nil || encoded == nil || encoded.typeID != proto.ResultTypeID || len(encoded.body) < 12 || reqMsgID == 0 {
		return nil, nil, errors.New("invalid rpc_result retarget")
	}
	bytes := len(encoded.body)
	if bytes > maxOutboundBodyBytes {
		return nil, nil, fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, bytes, maxOutboundBodyBytes)
	}
	budget := c.outboundMessageBudget(encoded.typeID, false)
	if !budget.reserve(bytes) {
		return nil, nil, ErrOutboundTrackedBudget
	}
	reserved := &outboundBodyReservation{budget: budget, bytes: bytes}
	clone, err := cloneRPCResultForRequest(encoded, reqMsgID, shareDelivery)
	if err != nil {
		reserved.release()
		return nil, nil, err
	}
	return clone, reserved, nil
}

type outboundResult struct {
	info   []byte
	resent bool
	err    error
}

type outboundFrame struct {
	msgID         int64
	seqNo         int32
	typeID        uint32
	body          []byte
	reservedBytes int
	// reservationBudget follows this frame from producer queue through resend tracking.
	// Service frames such as new_session_created are content-related and therefore pending,
	// but their bytes must remain on the independent control budget for the full lifetime.
	reservationBudget *outboundTrackedBudget
	reqMsgID          int64
	// layer is retained only for proactive session-bound frames so a later
	// msg_resend_req cannot replay bytes from an obsolete profile epoch.
	layer  *outboundLayerBinding
	sentAt time.Time
	sends  int
}

type outboundState struct {
	pending     map[int64]*outboundFrame
	order       []int64
	byRequest   map[int64]int64
	acked       map[int64]struct{}
	ackOrder    []int64
	totalBytes  int
	maxMessages int
	maxBytes    int
	budget      *outboundTrackedBudget
}

// outboundTrackedBudget 是 body/control/write 三类预算共用的原子 byte-budget primitive。
// 每个实例只负责一类：普通 encoded body、encoded service frame + 控制向量，或 write
// scratch；同一 reservation 不跨实例释放。
// 预算在入队前预留，写成功后从 queue reservation 原子转交给 pending；CAS 避免所有
// outbound producer/actor 在一把全局 mutex 上串行。
type outboundTrackedBudget struct {
	maxBytes int64
	used     atomic.Int64
	wakeMu   sync.Mutex
	wake     *outboundBudgetWake
}

type outboundBudgetWake struct {
	ch      chan struct{}
	waiters int
}

const defaultOutboundEncodeConcurrency = 32

// outboundEncodeSlots bounds the otherwise unaccounted transient allocation made by TL
// encoding and layer transcoding. The retained encoded bytes are covered by
// outboundTrackedBudget after Encode returns, but without this process-wide gate many RPC
// workers could all allocate a near-limit body before any of them attempted that reservation.
// Encoding cannot be cancelled once an Encoder has started, so admission is intentionally
// acquired before calling user/domain supplied Encoder code and released immediately after the
// transient allocation has either become a tracked body or been discarded.
var outboundEncodeSlots = make(chan struct{}, defaultOutboundEncodeConcurrency)

func withOutboundEncodeSlot(ctx context.Context, stop <-chan struct{}, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case outboundEncodeSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return ErrConnClosed
	}
	defer func() { <-outboundEncodeSlots }()
	return fn()
}

func newOutboundTrackedBudget(maxBytes int64) *outboundTrackedBudget {
	if maxBytes <= 0 {
		maxBytes = defaultOutboundTrackedMaxBytes
	}
	return &outboundTrackedBudget{
		maxBytes: maxBytes,
		wake:     &outboundBudgetWake{ch: make(chan struct{})},
	}
}

func (b *outboundTrackedBudget) reserve(n int) bool {
	if n <= 0 {
		return true
	}
	bytes := int64(n)
	if b == nil || bytes > b.maxBytes {
		return false
	}
	for {
		used := b.used.Load()
		if used > b.maxBytes-bytes {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (b *outboundTrackedBudget) release(n int) {
	if b == nil || n <= 0 {
		return
	}
	if used := b.used.Add(-int64(n)); used < 0 {
		panic("mtprotoedge: outbound tracked byte budget released more than reserved")
	}
	// A release can make room for multiple independent writes. Broadcast to every waiter in the
	// current generation; a capacity-1 token can strand all but one waiter even while bytes are
	// available indefinitely. Generations are only allocated on the saturated slow path.
	b.wakeMu.Lock()
	if b.wake.waiters > 0 {
		old := b.wake
		b.wake = &outboundBudgetWake{ch: make(chan struct{})}
		close(old.ch)
	}
	b.wakeMu.Unlock()
}

func (b *outboundTrackedBudget) waitReserve(ctx context.Context, stop <-chan struct{}, n int) error {
	return b.waitReserveUntil(ctx, stop, n, time.Time{})
}

// waitReserveUntil is the saturated-path variant used by write scratch.  The absolute deadline
// is observed from the first capacity wait, not only after encryption when socket I/O begins.
// Its timer is allocated lazily after the CAS fast path fails, preserving the allocation-free
// steady state.
func (b *outboundTrackedBudget) waitReserveUntil(ctx context.Context, stop <-chan struct{}, n int, deadline time.Time) error {
	if b == nil || n < 0 || int64(n) > b.maxBytes {
		return ErrOutboundTrackedBudget
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	var (
		deadlineTimer *time.Timer
		deadlineC     <-chan time.Time
	)
	defer func() {
		if deadlineTimer != nil {
			deadlineTimer.Stop()
		}
	}()
	for {
		if b.reserve(n) {
			return nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		// Subscribe under the same lock used by release, then retry once while holding it. This
		// closes the release-between-check-and-subscribe window without putting the normal CAS
		// reservation path behind a global mutex.
		b.wakeMu.Lock()
		if b.reserve(n) {
			b.wakeMu.Unlock()
			return nil
		}
		generation := b.wake
		generation.waiters++
		b.wakeMu.Unlock()

		if deadlineC == nil && !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait < 0 {
				wait = 0
			}
			deadlineTimer = time.NewTimer(wait)
			deadlineC = deadlineTimer.C
		}
		var err error
		select {
		case <-generation.ch:
		case <-ctx.Done():
			err = ctx.Err()
		case <-deadlineC:
			err = context.DeadlineExceeded
		case <-stop:
			err = ErrConnClosed
		}
		b.wakeMu.Lock()
		generation.waiters--
		b.wakeMu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (b *outboundTrackedBudget) snapshot() int64 {
	if b == nil {
		return 0
	}
	return b.used.Load()
}

func newOutboundState(budget *outboundTrackedBudget) *outboundState {
	return newOutboundStateWithLimits(budget, maxTrackedServerMsgIDs, maxTrackedServerBytes)
}

func newOutboundStateWithLimits(budget *outboundTrackedBudget, maxMessages, maxBytes int) *outboundState {
	return &outboundState{
		maxMessages: maxMessages,
		maxBytes:    maxBytes,
		budget:      budget,
	}
}

func (c *Conn) startOutbound() {
	if c.metrics == nil {
		c.metrics = NopMetrics{}
	}
	if c.outboundQueueSize <= 0 {
		c.outboundQueueSize = defaultOutboundQueueSize
	}
	if c.outboundQueueSize < 3 {
		c.outboundQueueSize = 3
	}
	if c.outboundControlQueueSize <= 0 {
		c.outboundControlQueueSize = defaultOutboundControlQueueSize
	}
	c.ensureOutboundTrackedBudget()
	criticalSize := min(defaultOutboundCriticalQueueSize, max(1, c.outboundQueueSize/8))
	bulkSize := min(defaultOutboundBulkQueueSize, max(1, c.outboundQueueSize/8))
	normalSize := c.outboundQueueSize - criticalSize - bulkSize
	c.outbound = make(chan outboundOp, normalSize)
	c.outboundControl = make(chan outboundOp, c.outboundControlQueueSize)
	c.outboundCritical = make(chan outboundOp, criticalSize)
	c.outboundBulk = make(chan outboundOp, bulkSize)
	c.outboundStop = make(chan struct{})
	c.outboundDone = make(chan struct{})
	go c.outboundLoop()
}

// Close 停止连接的出站 actor。它不关闭底层 transport；transport 生命周期仍由 serveConn 管理。
func (c *Conn) Close() {
	c.beginTerminalShutdown()
	c.closeInboundRPCScheduler()
	c.waitOutboundShutdown()
}

// beginTerminalShutdown is the non-blocking ownership transition shared by graceful and hard
// close. It closes both producer gates and cancels RPC work before any potentially blocking
// transport.Close call, so a timed-out batch close cannot keep accepting memory/work.
func (c *Conn) beginTerminalShutdown() {
	c.retire()
	c.signalOutboundStop()
	c.beginCloseInboundRPCScheduler()
}

func (c *Conn) waitOutboundShutdown() {
	if c.outboundDone != nil {
		<-c.outboundDone
	}
}

func (c *Conn) waitOutboundShutdownUntil(timeout time.Duration) bool {
	if c == nil || c.outboundDone == nil {
		return true
	}
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.outboundDone:
		return true
	case <-timer.C:
		return false
	}
}

// closeTransport 只关闭物理 transport，不等待 outbound actor。写失败路径运行在
// actor 自身 goroutine 中，若在这里调用 Close 会等待 outboundDone 而自锁。
func (c *Conn) closeTransport() {
	if c == nil {
		return
	}
	if c.transportLease != nil {
		_ = c.transportLease.Close()
		return
	}
	c.transportClose.Do(func() {
		if c.transport != nil {
			_ = c.transport.Close()
		}
	})
}

// failTransport 把不可恢复的写错误提升为连接级 terminal failure。它只负责
// 把 lifecycle 推进到 retired 并关闭 socket；handleOutboundOp 返回后，actor 自己发停止信号并退出，
// serveConn 被 Close 解开 Recv 后负责注销索引。
func (c *Conn) failTransport() {
	// Publish both producer gates before Close: a custom/broken transport may block in
	// Close, but it must not keep accepting queued bodies or RPC work meanwhile.
	c.beginTerminalShutdown()
	c.closeTransport()
}

// fenceUnavailableAuthKey turns protocol expiry into a connection-level terminal
// boundary. Outbound producers may discover expiry before the read loop sees the
// client's next frame; in that case the socket is closed so the client reconnects
// and receives the ordinary -404 admission response for the stale raw key.
func (c *Conn) fenceUnavailableAuthKey() {
	if c == nil || !c.authKeyProtocolUnavailableNow() {
		return
	}
	c.beginTerminalShutdown()
	c.closeTransport()
}

// fenceUndeliveredRPCResult is the no-reentry terminal path used from a task's
// release callback. That callback may itself run while rpcClose.Do is draining
// queued tasks, so calling beginCloseInboundRPCScheduler again would deadlock on
// sync.Once. Closing the socket wakes serveConn, whose ordinary defer completes
// scheduler/index cleanup; when shutdown already owns the callback, that cleanup
// is already in progress.
func (c *Conn) fenceUndeliveredRPCResult() {
	if c == nil {
		return
	}
	// A replacement/shutdown that already published terminal owns physical
	// lifecycle cleanup (and may intentionally transfer the lease). Only the
	// resultless task that wins false->true is allowed to close this generation.
	if !c.retire() {
		return
	}
	c.signalOutboundStop()
	if c.transportLease != nil {
		c.transportLease.startCloseAlreadyFenced()
		return
	}
	// Legacy construction-only Conns have no owner callback graph, so their
	// exact transport close cannot re-enter logical lifecycle cleanup. Keep a
	// pathological Close outside the shared RPC worker just like the lease path.
	go c.transportClose.Do(func() {
		if c.transport != nil {
			_ = c.transport.Close()
		}
	})
}

// dropSlowConsumer 把出站队列持续拥塞的连接降级为离线连接。它不能等待 outbound
// actor：调用方位于 fan-out 热路径，等待单个慢 socket 会把同一用户的健康设备和
// transactional outbox lane 一起拖住。关闭 transport 会打断可能阻塞的写；serveConn
// 随后负责 Unregister，durable update 由该设备重连后的 getDifference 补偿。
func (c *Conn) dropSlowConsumer() {
	c.beginTerminalShutdown()
	c.closeTransport()
}

func (c *Conn) signalOutboundStop() {
	c.outboundClose.Do(func() {
		c.outboundEnqueueMu.Lock()
		c.outboundClosing = true
		if c.outboundStop != nil {
			close(c.outboundStop)
		}
		c.outboundEnqueueMu.Unlock()
	})
}

// Send 加密并发送一条 server 消息。
func (c *Conn) Send(ctx context.Context, t proto.MessageType, msg bin.Encoder) error {
	return c.send(ctx, t, msg, false)
}

// SendRequiredControl writes a protocol-critical control message before the caller commits
// the state transition guarded by that message. One absolute deadline covers encode admission,
// body-budget reservation, control-queue admission and the physical transport write. A failure
// is terminal: continuing on the same connection could expose state whose required notification
// never reached the client.
//
// Success only confirms the physical write; it does not wait for the client's msgs_ack.
func (c *Conn) SendRequiredControl(ctx context.Context, t proto.MessageType, msg bin.Encoder) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	deadline := now.Add(requiredControlMaxWait)
	if c.writeTimeout > 0 {
		if writeDeadline := now.Add(c.writeTimeout); writeDeadline.Before(deadline) {
			deadline = writeDeadline
		}
	}
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	requiredCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := requiredCtx.Err(); err != nil {
		c.failTransport()
		return err
	}

	err := c.sendOutbound(requiredCtx, t, msg, nil, true)
	if err != nil {
		c.failTransport()
	}
	return err
}

// SendBestEffort 只等待消息进入普通 outbound 队列，不等待网络写完成。
// 用于 updates fanout：队列拥塞时返回 ErrOutboundQueueFull，durable outbox/getDifference 负责兜底。
func (c *Conn) SendBestEffort(ctx context.Context, t proto.MessageType, msg bin.Encoder, timeout time.Duration) error {
	return c.sendBestEffort(ctx, t, msg, nil, timeout)
}

func (c *Conn) SendBestEffortEncoded(ctx context.Context, t proto.MessageType, encoded *encodedOutboundMessage, timeout time.Duration) error {
	return c.sendBestEffort(ctx, t, nil, encoded, timeout)
}

func (c *Conn) sendBestEffort(ctx context.Context, t proto.MessageType, msg bin.Encoder, encoded *encodedOutboundMessage, timeout time.Duration) error {
	if c.outbound == nil || c.outboundControl == nil {
		return ErrConnClosed
	}
	if c.isRetired() {
		return ErrConnClosed
	}
	writeCtx := context.Background()
	if ctx != nil {
		writeCtx = context.WithoutCancel(ctx)
	}
	// Encode before registering as an enqueue owner. Encoder is an external interface and may
	// block forever; connection shutdown must not wait on it. The subsequent producer gate either
	// accepts the completed/tracked body or rejects it and releases the reservation.
	op, err := c.newOutboundSendOp(ctx, t, msg, encoded, false)
	if err != nil {
		// Best-effort durable updates may be dropped under process-wide pressure and
		// recovered via getDifference. Do not close this healthy connection merely because
		// other slow sockets currently own the shared body budget.
		if errors.Is(err, ErrOutboundTrackedBudget) {
			c.metrics.OutboundDropped("tracked_global_byte_budget")
		}
		return err
	}
	if !c.beginOutboundEnqueue() {
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	}
	defer c.endOutboundEnqueue()
	op.ctx = writeCtx
	op.enqueuedAt = time.Now()
	// 快路径：非阻塞入队。fan-out 每 (conn × push) 都走这里，队列有空位时不为
	// 本次推送分配任何 timer（此前 timeout>0 无条件 WithTimeout，稳态白建 timer）。
	q := c.outboundQueue(op)
	select {
	case q <- op:
		return nil
	case <-c.outboundStop:
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	default:
	}
	if timeout == 0 {
		op.releaseReservation(c.outboundTrackedBudget)
		c.metrics.OutboundDropped("push_queue_full")
		return ErrOutboundQueueFull
	}
	c.metrics.OutboundQueueWait(len(q), cap(q))
	if ctx == nil {
		ctx = context.Background()
	}
	var timeoutC <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}
	select {
	case q <- op:
		return nil
	case <-timeoutC:
		op.releaseReservation(c.outboundTrackedBudget)
		c.metrics.OutboundDropped("push_queue_timeout")
		return ErrOutboundQueueFull
	case <-ctx.Done():
		op.releaseReservation(c.outboundTrackedBudget)
		return ctx.Err()
	case <-c.outboundStop:
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	}
}

func (c *Conn) send(ctx context.Context, t proto.MessageType, msg bin.Encoder, control bool) error {
	return c.sendOutbound(ctx, t, msg, nil, control)
}

func (c *Conn) SendEncoded(ctx context.Context, t proto.MessageType, encoded *encodedOutboundMessage) error {
	if encoded != nil {
		if err := encoded.prepareDeliveryHook(defaultRPCDeliveryHookExecutor); err != nil {
			return err
		}
	}
	err := c.sendOutbound(ctx, t, nil, encoded, false)
	if err != nil && encoded != nil {
		encoded.markReplayable()
	}
	return err
}

// enqueueEncodedDelivery transfers an immutable body to the bounded egress actor
// and returns after queue admission, not after socket I/O. terminal becomes actor-
// owned only on success and is invoked for write success, write failure, or drain.
func (c *Conn) enqueueEncodedDelivery(
	ctx context.Context,
	t proto.MessageType,
	encoded *encodedOutboundMessage,
	priority outboundPriority,
	terminal func(error),
) error {
	return c.enqueueEncodedDeliveryReserved(ctx, t, encoded, priority, terminal, nil)
}

// enqueueEncodedDeliveryReserved accepts an optional byte reservation acquired
// while the caller still owned outboundEncodeSlots. Passing nil preserves the
// ordinary pre-encoded path used by callers that did not allocate a retained clone.
func (c *Conn) enqueueEncodedDeliveryReserved(
	ctx context.Context,
	t proto.MessageType,
	encoded *encodedOutboundMessage,
	priority outboundPriority,
	terminal func(error),
	reserved *outboundBodyReservation,
) error {
	if c.outbound == nil || c.outboundControl == nil || c.outboundCritical == nil || c.outboundBulk == nil {
		return ErrConnClosed
	}
	if encoded != nil {
		if err := encoded.prepareDeliveryHook(defaultRPCDeliveryHookExecutor); err != nil {
			return err
		}
	}
	var op outboundOp
	var err error
	if reserved == nil {
		op, err = c.newOutboundSendOp(ctx, t, nil, encoded, false)
	} else {
		op, err = reserved.take(encoded)
		if err == nil {
			op.msgType = t
		}
	}
	if err != nil {
		if encoded != nil {
			encoded.markReplayable()
		}
		c.failOutboundBudget(err)
		return err
	}
	if !c.beginOutboundEnqueue() {
		if reserved == nil || !reserved.reclaim(&op) {
			op.releaseReservation(c.outboundTrackedBudget)
		}
		if encoded != nil {
			encoded.markReplayable()
		}
		return ErrConnClosed
	}
	op.priority = priority
	op.ctx = context.Background()
	op.enqueuedAt = time.Now()
	op.terminal = terminal
	if err := c.enqueueOutboundRegistered(ctx, op); err != nil {
		if reserved == nil || !reserved.reclaim(&op) {
			op.releaseReservation(c.outboundTrackedBudget)
		}
		c.endOutboundEnqueue()
		if encoded != nil {
			encoded.markReplayable()
		}
		return err
	}
	c.endOutboundEnqueue()
	return nil
}

func (c *Conn) sendOutbound(ctx context.Context, t proto.MessageType, msg bin.Encoder, encoded *encodedOutboundMessage, control bool) error {
	return c.sendOutboundWithTerminal(ctx, t, msg, encoded, control, nil)
}

// sendOutboundWithTerminal is the synchronous actor path with an optional
// terminal callback. Once queue admission succeeds, terminal runs on the sole
// actor after the transport has definitively reported write success/failure and
// before the caller's done notification. Early admission failures invoke it in
// the caller, so a waiter always observes exactly one terminal outcome.
func (c *Conn) sendOutboundWithTerminal(
	ctx context.Context,
	t proto.MessageType,
	msg bin.Encoder,
	encoded *encodedOutboundMessage,
	control bool,
	terminal func(error),
) error {
	return c.sendOutboundWithTerminalReserved(ctx, t, msg, encoded, control, terminal, nil)
}

// sendOutboundWithTerminalReserved is the synchronous counterpart of
// enqueueEncodedDeliveryReserved. A replay/rewrap producer reserves before it
// clones; take atomically transfers that charge to the actor so a racing
// watchdog can either release the still-owned reservation or observe that the
// actor has already become its sole owner, never both.
func (c *Conn) sendOutboundWithTerminalReserved(
	ctx context.Context,
	t proto.MessageType,
	msg bin.Encoder,
	encoded *encodedOutboundMessage,
	control bool,
	terminal func(error),
	reserved *outboundBodyReservation,
) error {
	if c.outbound == nil || c.outboundControl == nil {
		if terminal != nil {
			terminal(ErrConnClosed)
		}
		return ErrConnClosed
	}
	var op outboundOp
	var err error
	if reserved == nil {
		op, err = c.newOutboundSendOp(ctx, t, msg, encoded, control)
	} else {
		op, err = reserved.take(encoded)
		if err == nil {
			op.msgType = t
			op.msg = msg
			op.priority = classifyOutboundPriority(encoded, control)
		}
	}
	if err != nil {
		c.failOutboundBudget(err)
		if terminal != nil {
			terminal(err)
		}
		return err
	}
	if !c.beginOutboundEnqueue() {
		if reserved == nil || !reserved.reclaim(&op) {
			op.releaseReservation(c.outboundTrackedBudget)
		}
		if terminal != nil {
			terminal(ErrConnClosed)
		}
		return ErrConnClosed
	}
	op.control = control
	op.ctx = ctx
	op.enqueuedAt = time.Now()
	op.done = make(chan outboundResult, 1)
	op.terminal = terminal
	if err := c.enqueueOutboundRegistered(ctx, op); err != nil {
		if reserved == nil || !reserved.reclaim(&op) {
			op.releaseReservation(c.outboundTrackedBudget)
		}
		c.endOutboundEnqueue()
		if terminal != nil {
			terminal(err)
		}
		return err
	}
	c.endOutboundEnqueue()
	select {
	case res := <-op.done:
		return res.err
	case <-ctx.Done():
		// A physical write can complete at the same instant as the caller's
		// deadline. Prefer the actor's terminal result when it is already
		// available so required-control callers do not poison a healthy Conn.
		select {
		case res := <-op.done:
			return res.err
		default:
		}
		return ctx.Err()
	case <-c.outboundStop:
		select {
		case res := <-op.done:
			return res.err
		default:
		}
		return ErrConnClosed
	}
}

// SendAsync 入队一条 server 消息但不等待发送结果（fire-and-forget），用于读循环里的控制消息
// （ack/pong/bad_msg/future_salts/state_info）：避免读循环被 outbound 写
// 阻塞而连带卡死。走优先(control)队列保证不被普通 push 拖后；队列满时丢弃并记 metrics——此时
// 连接多已严重拥塞，控制消息丢失由客户端重传 / 读写超时兜底。返回非 nil 仅表示连接已关闭。
func (c *Conn) SendAsync(ctx context.Context, t proto.MessageType, msg bin.Encoder) error {
	if c.outbound == nil || c.outboundControl == nil {
		return ErrConnClosed
	}
	if c.isRetired() {
		return ErrConnClosed
	}
	op, err := c.newOutboundSendOp(ctx, t, msg, nil, true)
	if err != nil {
		c.failOutboundBudget(err)
		return err
	}
	if !c.beginOutboundEnqueue() {
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	}
	defer c.endOutboundEnqueue()
	op.control = true
	op.ctx = ctx
	op.enqueuedAt = time.Now()
	// done 为 nil：fire-and-forget，handleOutboundSend 的 finish 对 nil done 安全跳过。
	select {
	case c.outboundControl <- op:
		return nil
	case <-c.outboundStop:
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	default:
		op.releaseReservation(c.outboundTrackedBudget)
		c.metrics.OutboundDropped("control_queue_full")
		return nil
	}
}

// AckServerMessages 接收客户端 msgs_ack，释放已确认的 server 出站消息。
func (c *Conn) AckServerMessages(ids []int64) {
	if len(ids) == 0 || c.outbound == nil || c.outboundControl == nil || c.isRetired() {
		return
	}
	op, err := c.newOutboundVectorOp(outboundAck, ids)
	if err != nil {
		c.failOutboundBudget(err)
		return
	}
	if !c.beginOutboundEnqueue() {
		op.releaseReservation(c.outboundTrackedBudget)
		return
	}
	defer c.endOutboundEnqueue()
	select {
	case c.outboundControl <- op:
	case <-c.outboundStop:
		op.releaseReservation(c.outboundTrackedBudget)
	default:
		op.releaseReservation(c.outboundTrackedBudget)
		c.metrics.OutboundDropped("ack_queue_full")
	}
}

// OutgoingStateInfo 返回本连接出站消息的状态。返回值中 0 表示无出站侧意见，
// 调用方可继续用入站 connState 兜底。
func (c *Conn) OutgoingStateInfo(ctx context.Context, ids []int64) ([]byte, error) {
	if c.outbound == nil {
		return nil, ErrConnClosed
	}
	op, err := c.newOutboundVectorOp(outboundQueryState, ids)
	if err != nil {
		c.failOutboundBudget(err)
		return nil, err
	}
	op.ctx = ctx
	op.done = make(chan outboundResult, 1)
	if err := c.enqueueOutbound(ctx, op); err != nil {
		op.releaseReservation(c.outboundTrackedBudget)
		return nil, err
	}
	select {
	case res := <-op.done:
		return res.info, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.outboundStop:
		return nil, ErrConnClosed
	}
}

// ResendMessages 重发仍在 outgoing queue 中的 server 消息，并返回对应状态。
func (c *Conn) ResendMessages(ctx context.Context, ids []int64) ([]byte, error) {
	if c.outbound == nil {
		return nil, ErrConnClosed
	}
	op, err := c.newOutboundVectorOp(outboundResend, ids)
	if err != nil {
		c.failOutboundBudget(err)
		return nil, err
	}
	op.ctx = ctx
	op.done = make(chan outboundResult, 1)
	if err := c.enqueueOutbound(ctx, op); err != nil {
		op.releaseReservation(c.outboundTrackedBudget)
		return nil, err
	}
	select {
	case res := <-op.done:
		return res.info, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.outboundStop:
		return nil, ErrConnClosed
	}
}

// ResendByRequest 在重复 RPC 请求到达时，按原 client msg_id 找到并重发已有 rpc_result。
func (c *Conn) ResendByRequest(ctx context.Context, reqMsgID int64) (bool, error) {
	if c.outbound == nil {
		return false, ErrConnClosed
	}
	op := outboundOp{
		kind:     outboundResendByRequest,
		control:  true,
		ctx:      ctx,
		reqMsgID: reqMsgID,
		done:     make(chan outboundResult, 1),
	}
	if err := c.enqueueOutbound(ctx, op); err != nil {
		return false, err
	}
	select {
	case res := <-op.done:
		return res.resent, res.err
	case <-ctx.Done():
		return false, ctx.Err()
	case <-c.outboundStop:
		return false, ErrConnClosed
	}
}

func (c *Conn) enqueueOutbound(ctx context.Context, op outboundOp) error {
	if !c.beginOutboundEnqueue() {
		return ErrConnClosed
	}
	defer c.endOutboundEnqueue()
	return c.enqueueOutboundRegistered(ctx, op)
}

func (c *Conn) enqueueOutboundRegistered(ctx context.Context, op outboundOp) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.isRetired() {
		return ErrConnClosed
	}
	q := c.outboundQueue(op)
	select {
	case q <- op:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.outboundStop:
		return ErrConnClosed
	default:
	}
	c.metrics.OutboundQueueWait(len(q), cap(q))
	select {
	case q <- op:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.outboundStop:
		return ErrConnClosed
	}
}

func (c *Conn) outboundQueue(op outboundOp) chan outboundOp {
	if op.control || op.priority == outboundPriorityControl {
		return c.outboundControl
	}
	switch op.priority {
	case outboundPriorityCritical:
		return c.outboundCritical
	case outboundPriorityBulk:
		return c.outboundBulk
	default:
		return c.outbound
	}
}

func (c *Conn) beginOutboundEnqueue() bool {
	// A temporary key is unusable for both inbound RPCs and server-originated
	// updates at the same absolute boundary. Reject before encoding admission;
	// the actor and write path repeat this check to close the two race windows.
	if c.authKeyProtocolUnavailableNow() {
		c.fenceUnavailableAuthKey()
		return false
	}
	c.outboundEnqueueMu.Lock()
	defer c.outboundEnqueueMu.Unlock()
	if c.outboundClosing || c.isRetired() {
		return false
	}
	c.outboundEnqueueWG.Add(1)
	return true
}

func (c *Conn) endOutboundEnqueue() {
	c.outboundEnqueueWG.Done()
}

func (c *Conn) outboundLoop() {
	state := newOutboundState(c.outboundTrackedBudget)
	ordinarySinceBulk := 0
	defer func() {
		// pending frames belong exclusively to this actor. Releasing after drain ensures no
		// resend path can race the final budget return and no Conn body survives actor exit.
		state.releaseAll()
		close(c.outboundDone)
	}()
	for {
		if c.isRetired() {
			c.signalOutboundStop()
			c.drainOutbound()
			return
		}
		op, ok := c.nextOutboundOp(&ordinarySinceBulk)
		if !ok {
			c.drainOutbound()
			return
		}
		if c.isRetired() {
			op.finish(outboundResult{err: ErrConnClosed})
			op.releaseReservation(c.outboundTrackedBudget)
			c.signalOutboundStop()
			c.drainOutbound()
			return
		}
		c.handleOutboundOp(state, op)
		if c.isRetired() {
			c.signalOutboundStop()
			c.drainOutbound()
			return
		}
	}
}

// nextOutboundOp applies the connection-wide egress policy without introducing
// another writer. Required protocol controls stay strict, convergence RPCs pass
// ordinary/bulk work, and a bounded ordinary burst guarantees bulk progress.
func (c *Conn) nextOutboundOp(ordinarySinceBulk *int) (outboundOp, bool) {
	try := func(q <-chan outboundOp) (outboundOp, bool) {
		select {
		case op := <-q:
			return op, true
		default:
			return outboundOp{}, false
		}
	}
	if op, ok := try(c.outboundControl); ok {
		return op, true
	}
	if op, ok := try(c.outboundCritical); ok {
		return op, true
	}
	if *ordinarySinceBulk >= maxOrdinaryBeforeBulk {
		if op, ok := try(c.outboundBulk); ok {
			*ordinarySinceBulk = 0
			return op, true
		}
	}
	if op, ok := try(c.outbound); ok {
		*ordinarySinceBulk++
		return op, true
	}
	if op, ok := try(c.outboundBulk); ok {
		*ordinarySinceBulk = 0
		return op, true
	}

	select {
	case <-c.outboundStop:
		return outboundOp{}, false
	case op := <-c.outboundControl:
		return op, true
	case op := <-c.outboundCritical:
		return op, true
	case op := <-c.outbound:
		*ordinarySinceBulk++
		return op, true
	case op := <-c.outboundBulk:
		*ordinarySinceBulk = 0
		return op, true
	}
}

func (c *Conn) drainOutbound() {
	// signalOutboundStop closes the producer gate before the actor gets here.
	// Waiting first guarantees every producer either enqueued an owned op or
	// released its reservation, after which this final drain cannot miss a body.
	c.outboundEnqueueWG.Wait()
	for {
		select {
		case op := <-c.outboundControl:
			op.finish(outboundResult{err: ErrConnClosed})
			op.releaseReservation(c.outboundTrackedBudget)
		case op := <-c.outboundCritical:
			op.finish(outboundResult{err: ErrConnClosed})
			op.releaseReservation(c.outboundTrackedBudget)
		case op := <-c.outbound:
			op.finish(outboundResult{err: ErrConnClosed})
			op.releaseReservation(c.outboundTrackedBudget)
		case op := <-c.outboundBulk:
			op.finish(outboundResult{err: ErrConnClosed})
			op.releaseReservation(c.outboundTrackedBudget)
		default:
			return
		}
	}
}

func (c *Conn) handleOutboundOp(state *outboundState, op outboundOp) {
	// An operation can sit in a bounded queue across the protocol expiry instant.
	// It must be failed and released without touching the wire.
	if c.authKeyProtocolUnavailableNow() {
		op.finish(outboundResult{err: ErrConnClosed})
		op.releaseReservation(state.budget)
		c.fenceUnavailableAuthKey()
		return
	}
	if op.kind != outboundSend {
		defer op.releaseReservation(state.budget)
	}
	switch op.kind {
	case outboundSend:
		c.handleOutboundSend(state, op)
	case outboundAck:
		for _, reqMsgID := range state.ack(op.ids) {
			if c.rpcResultAcked != nil {
				c.rpcResultAcked(c, reqMsgID)
			}
		}
	case outboundQueryState:
		op.finish(outboundResult{info: state.stateInfo(op.ids)})
	case outboundResend:
		info, err := c.handleOutboundResend(state, op.ctx, op.ids)
		op.finish(outboundResult{info: info, err: err})
	case outboundResendByRequest:
		resent, err := c.handleOutboundResendByRequest(state, op.ctx, op.reqMsgID)
		op.finish(outboundResult{resent: resent, err: err})
	default:
		op.finish(outboundResult{err: fmt.Errorf("unknown outbound op %d", op.kind)})
	}
}

func (c *Conn) handleOutboundSend(state *outboundState, op outboundOp) {
	var binding *outboundLayerBinding
	if op.encoded != nil {
		binding = op.encoded.layer
	}
	if c.lockSessionLayerBinding(binding) {
		defer c.layerProfileMu.RUnlock()
	}
	// Extract the admitted reservation before any actor-side retarget or layer
	// conversion can allocate another full body. supersededBytes covers immutable
	// source bodies still reachable by the terminal callback; reserved covers the
	// body that will be transferred to pending if the write succeeds.
	reserved := op.reservedBytes
	reservationBudget := op.reservationBudget
	if reservationBudget == nil {
		reservationBudget = state.budget
	}
	op.reservedBytes = 0
	op.reservationBudget = nil
	supersededBytes := 0
	defer func() {
		reservationBudget.release(reserved)
		reservationBudget.release(supersededBytes)
	}()

	var err error
	if op.encoded != nil {
		targetReqMsgID := op.encoded.beginWriting()
		if targetReqMsgID != 0 && targetReqMsgID != op.encoded.reqMsgID {
			cloneBytes := len(op.encoded.body)
			if !reservationBudget.reserve(cloneBytes) {
				err = ErrOutboundTrackedBudget
			} else {
				clone, cloneErr := cloneRPCResultForRequest(op.encoded, targetReqMsgID, true)
				if cloneErr != nil {
					reservationBudget.release(cloneBytes)
					err = cloneErr
				} else {
					// The terminal callback still owns the original encoded pointer.
					// Keep its reservation until that callback has cached/retained it.
					supersededBytes += reserved
					reserved = cloneBytes
					op.encoded = clone
				}
			}
		}
	}
	var frame *outboundFrame
	if err == nil {
		frame, err = c.buildFrame(op.ctx, op.msgType, op.msg, op.encoded)
	}
	// A profile-bound preparation can allocate a different body. Reserve the
	// replacement before dropping the original prepared-body reservation. The
	// original body stays charged through the terminal callback because cache/rewrap
	// subscribers can still retain it there.
	if err == nil && frame != nil && op.encoded != nil && !sameBacking(frame.body, op.encoded.body) {
		if !reservationBudget.reserve(len(frame.body)) {
			err = ErrOutboundTrackedBudget
		} else {
			supersededBytes += reserved
			reserved = len(frame.body)
		}
	}
	if err == nil && frame != nil && frameNeedsAck(frame.typeID) {
		// The queue reservation is transferred to pending after write. A frame larger
		// than the per-Conn resend ceiling is rejected before any bytes hit the wire.
		if len(frame.body) > maxTrackedServerBytes {
			err = ErrOutboundTrackedBudget
		}
	}
	if errors.Is(err, ErrOutboundTrackedBudget) {
		c.metrics.OutboundDropped("tracked_global_byte_budget")
		c.failTransport()
	}
	if isOutboundStaleLayerEpoch(err) {
		// Profile correction is normal client recovery. This proactive update
		// belongs to an older epoch; drop it without poisoning the socket.
		c.metrics.OutboundDropped("stale_layer_epoch")
	}
	if err == nil {
		err = c.writeFrame(op.ctx, frame)
	}
	if err == nil && frame != nil && frameNeedsAck(frame.typeID) {
		// 写成功后才提交 content seq_no 递增（peekSeqNo 已按当前计数算好本帧 seq_no）。
		c.commitContentSeqNo()
		frame.reservedBytes = reserved
		frame.reservationBudget = reservationBudget
		reserved = 0
		if dropped := state.addReserved(frame); dropped > 0 {
			for i := 0; i < dropped; i++ {
				c.metrics.OutboundDropped("tracked_queue_overflow")
			}
		}
	}
	queueWait := time.Since(op.enqueuedAt)
	bytes := 0
	typeID := uint32(0)
	if frame != nil {
		bytes = len(frame.body)
		typeID = frame.typeID
	}
	c.metrics.OutboundSend(typeID, queueWait, bytes, err)
	op.finish(outboundResult{err: err})
}

func (c *Conn) handleOutboundResend(state *outboundState, ctx context.Context, ids []int64) ([]byte, error) {
	info := make([]byte, len(ids))
	resent := 0
	for i, id := range ids {
		if state.isKnown(id) {
			info[i] = msgStateReceived
		}
		frame, ok := state.pending[id]
		if !ok {
			continue
		}
		lockedLayer := c.lockSessionLayerBinding(frame.layer)
		if err := validateOutboundLayerBinding(c, &encodedOutboundMessage{layer: frame.layer}); err != nil {
			if lockedLayer {
				c.layerProfileMu.RUnlock()
			}
			if isOutboundStaleLayerEpoch(err) || isOutboundLayerProfileError(err) {
				state.removePending(id)
				c.metrics.OutboundDropped("stale_layer_resend")
				continue
			}
			return info, err
		}
		if err := c.writeFrame(ctx, frame); err != nil {
			if lockedLayer {
				c.layerProfileMu.RUnlock()
			}
			c.metrics.OutboundResend(resent, err)
			return info, err
		}
		if lockedLayer {
			c.layerProfileMu.RUnlock()
		}
		frame.sentAt = time.Now()
		frame.sends++
		resent++
	}
	c.metrics.OutboundResend(resent, nil)
	return info, nil
}

func (c *Conn) handleOutboundResendByRequest(state *outboundState, ctx context.Context, reqMsgID int64) (bool, error) {
	msgID, ok := state.byRequest[reqMsgID]
	if !ok {
		return false, nil
	}
	frame, ok := state.pending[msgID]
	if !ok {
		return false, nil
	}
	if err := c.writeFrame(ctx, frame); err != nil {
		c.metrics.OutboundResend(0, err)
		return false, err
	}
	frame.sentAt = time.Now()
	frame.sends++
	c.metrics.OutboundResend(1, nil)
	return true, nil
}

func (op outboundOp) finish(res outboundResult) {
	if op.terminal != nil {
		op.terminal(res.err)
	}
	if op.done == nil {
		return
	}
	select {
	case op.done <- res:
	default:
	}
}

func (op *outboundOp) releaseReservation(budget *outboundTrackedBudget) {
	if op == nil || op.reservedBytes <= 0 {
		return
	}
	if op.reservationBudget != nil {
		budget = op.reservationBudget
	}
	bytes := op.reservedBytes
	op.reservedBytes = 0
	op.encoded = nil
	op.msg = nil
	op.ids = nil
	op.reservationBudget = nil
	// Make the queued body/vector unreachable before advertising its bytes to another producer.
	budget.release(bytes)
}

func (c *Conn) newOutboundVectorOp(kind outboundOpKind, ids []int64) (outboundOp, error) {
	bytes := len(ids) * 8
	budget := c.ensureOutboundControlTrackedBudget()
	if !budget.reserve(bytes) {
		return outboundOp{}, ErrOutboundTrackedBudget
	}
	return outboundOp{
		kind:              kind,
		control:           true,
		ids:               append([]int64(nil), ids...),
		reservedBytes:     bytes,
		reservationBudget: budget,
	}, nil
}

func (c *Conn) newOutboundSendOp(ctx context.Context, t proto.MessageType, msg bin.Encoder, encoded *encodedOutboundMessage, priorityControl bool) (outboundOp, error) {
	var budget *outboundTrackedBudget
	if encoded == nil {
		var bytes int
		err := withOutboundEncodeSlot(ctx, c.outboundStop, func() error {
			var err error
			encoded, err = encodeOutboundMessageWithoutSlot(msg)
			if err != nil {
				return err
			}
			if encoded == nil {
				return errors.New("nil encoded outbound message")
			}
			bytes = len(encoded.body)
			if bytes > maxOutboundBodyBytes {
				return fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, bytes, maxOutboundBodyBytes)
			}
			budget = c.outboundMessageBudget(encoded.typeID, priorityControl)
			// Keep the transient encode slot until the completed body has entered the
			// retained-byte budget. Otherwise goroutines could successively finish an
			// encode, be descheduled before reserve, and accumulate an unbounded number
			// of completed-but-untracked bodies despite the encode concurrency gate.
			if !budget.reserve(bytes) {
				return ErrOutboundTrackedBudget
			}
			return nil
		})
		if err != nil {
			return outboundOp{}, err
		}
		return outboundOp{
			kind:              outboundSend,
			msgType:           t,
			encoded:           encoded,
			priority:          classifyOutboundPriority(encoded, priorityControl),
			reservedBytes:     bytes,
			reservationBudget: budget,
		}, nil
	}
	if encoded == nil {
		return outboundOp{}, errors.New("nil encoded outbound message")
	}
	// Catch correction-before-enqueue synchronously. The actor repeats the same
	// validation immediately before framing to close the bounded queue race.
	if err := validateOutboundLayerBinding(c, encoded); err != nil {
		return outboundOp{}, err
	}
	bytes := len(encoded.body)
	if bytes > maxOutboundBodyBytes {
		return outboundOp{}, fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, bytes, maxOutboundBodyBytes)
	}
	budget = c.outboundMessageBudget(encoded.typeID, priorityControl)
	if !budget.reserve(bytes) {
		return outboundOp{}, ErrOutboundTrackedBudget
	}
	return outboundOp{
		kind:              outboundSend,
		msgType:           t,
		encoded:           encoded,
		priority:          classifyOutboundPriority(encoded, priorityControl),
		reservedBytes:     bytes,
		reservationBudget: budget,
	}, nil
}

func classifyOutboundPriority(encoded *encodedOutboundMessage, control bool) outboundPriority {
	if control {
		return outboundPriorityControl
	}
	if encoded != nil && encoded.priority != outboundPriorityNormal {
		return encoded.priority
	}
	if encoded != nil && len(encoded.body) >= bulkOutboundThreshold {
		return outboundPriorityBulk
	}
	return outboundPriorityNormal
}

func (c *Conn) outboundMessageBudget(typeID uint32, priorityControl bool) *outboundTrackedBudget {
	if priorityControl || encodedControlFrame(typeID) {
		return c.ensureOutboundControlTrackedBudget()
	}
	return c.ensureOutboundTrackedBudget()
}

func (c *Conn) failOutboundBudget(err error) {
	if !errors.Is(err, ErrOutboundTrackedBudget) {
		return
	}
	if c.metrics != nil {
		c.metrics.OutboundDropped("tracked_global_byte_budget")
	}
	// No socket bytes exist yet. If an intentional session handoff already won
	// the terminal CAS, it owns close/transfer and this old producer must not close
	// the still-current lease. A live connection still gets fenced and closed.
	c.fenceUndeliveredRPCResult()
}

func (c *Conn) ensureOutboundTrackedBudget() *outboundTrackedBudget {
	c.outboundBudgetOnce.Do(func() {
		if c.outboundTrackedBudget == nil {
			// Standalone tests/embedders still get a bounded budget. Server-created
			// connections receive the shared Server budget before this can run.
			c.outboundTrackedBudget = newOutboundTrackedBudget(defaultOutboundTrackedMaxBytes)
		}
	})
	return c.outboundTrackedBudget
}

func (c *Conn) ensureOutboundControlTrackedBudget() *outboundTrackedBudget {
	c.outboundControlBudgetOnce.Do(func() {
		if c.outboundControlTrackedBudget == nil {
			c.outboundControlTrackedBudget = newOutboundTrackedBudget(defaultOutboundControlMaxBytes)
		}
	})
	return c.outboundControlTrackedBudget
}

func (c *Conn) buildFrame(ctx context.Context, t proto.MessageType, msg bin.Encoder, encoded *encodedOutboundMessage) (*outboundFrame, error) {
	if encoded == nil {
		var err error
		encoded, err = encodeOutboundMessage(msg)
		if err != nil {
			return nil, err
		}
	}
	if err := validateOutboundLayerBinding(c, encoded); err != nil {
		return nil, err
	}
	if encoded.layer == nil && !encoded.layerInvariant {
		return nil, ErrOutboundLayerBindingRequired
	}
	content := frameNeedsAck(encoded.typeID)
	msgID := c.msgID.New(t)
	return &outboundFrame{
		msgID:    msgID,
		seqNo:    c.peekSeqNo(content),
		typeID:   encoded.typeID,
		body:     encoded.body,
		reqMsgID: encoded.reqMsgID,
		layer:    encoded.layer,
	}, nil
}

// sameBacking reports whether a and b share the same backing array.
func sameBacking(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

func encodeOutboundMessage(msg bin.Encoder) (*encodedOutboundMessage, error) {
	return encodeOutboundMessageContext(context.Background(), msg)
}

func encodeOutboundMessageContext(ctx context.Context, msg bin.Encoder) (*encodedOutboundMessage, error) {
	var encoded *encodedOutboundMessage
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var err error
		encoded, err = encodeOutboundMessageWithoutSlot(msg)
		return err
	})
	return encoded, err
}

func encodeOutboundMessageWithoutSlot(msg bin.Encoder) (*encodedOutboundMessage, error) {
	if msg == nil {
		return nil, errors.New("nil outbound message")
	}
	var body bin.Buffer
	if err := msg.Encode(&body); err != nil {
		return nil, fmt.Errorf("encode outbound: %w", err)
	}
	typeID, err := (&bin.Buffer{Buf: body.Raw()}).PeekID()
	if err != nil {
		return nil, fmt.Errorf("peek outbound type id: %w", err)
	}
	return &encodedOutboundMessage{
		typeID:         typeID,
		body:           body.Raw(),
		reqMsgID:       outboundRequestMsgID(msg),
		layerInvariant: isLayerInvariantControlEncoder(msg),
	}, nil
}

// isLayerInvariantRPCResultEncoder proves the complete inner graph of the
// legacy MTProto service results that may be wrapped by rpc_result before an
// application Layer is known. Generated application results use their
// LayerRPCResult proof instead and are intentionally absent here.
func isLayerInvariantRPCResultEncoder(msg bin.Encoder) bool {
	switch msg.(type) {
	case *mt.RPCError,
		*mt.RPCAnswerUnknown,
		*mt.RPCAnswerDroppedRunning,
		*mt.RPCAnswerDropped:
		return true
	default:
		return false
	}
}

// isLayerInvariantControlEncoder is intentionally a closed type proof. It
// admits leaf MTProto service values plus the closed destroy_auth_key
// rpc_result whose inner constructor is validated by its concrete type.
// Generic rpc_result, gzip and container envelopes remain excluded because
// they can carry profile-dependent application payloads.
func isLayerInvariantControlEncoder(msg bin.Encoder) bool {
	switch msg.(type) {
	case *mt.Pong,
		*mt.FutureSalts,
		*mt.NewSessionCreated,
		*mt.MsgsAck,
		*mt.MsgsStateInfo,
		*mt.DestroySessionOk,
		*mt.DestroySessionNone,
		*mt.BadMsgNotification,
		*mt.BadServerSalt,
		*destroyAuthKeyRPCResult:
		return true
	default:
		return false
	}
}

// peekSeqNo 计算本帧的 seq_no，但不提交 content 计数递增——递增延到 writeFrame 成功后
// （commitContentSeqNo）。这样写失败（超时/连接关）但连接存活时，下一条 content 帧会复用
// 同一 seq_no 而非留下间隙，避免严格校验的客户端把间隙误判为丢帧。只由 outbound actor 调用。
func (c *Conn) peekSeqNo(content bool) int32 {
	seqNo := c.sentContentMessages * 2
	if content {
		seqNo++
	}
	return seqNo
}

// commitContentSeqNo 在一条 content 帧成功写出后提交 seq_no 递增。只由 outbound actor 调用。
func (c *Conn) commitContentSeqNo() {
	c.sentContentMessages++
}

// deadlineOutboundWriter 是可选的直管写超时接口：telesrv-owned compat transport 实现它，
// 让 outbound actor 每帧只做一次 SetWriteDeadline，不再为写超时分配 context timer
// （gotd transport.Conn 的 Send 本身也只消费 ctx.Deadline，不监听 ctx.Done，语义等价）。
type deadlineOutboundWriter interface {
	SendDeadline(deadline time.Time, b *bin.Buffer) error
}

type deadlineOutboundScratchWriter interface {
	SendDeadlineWithScratch(deadline time.Time, b *bin.Buffer, scratch *[]byte) error
}

type deadlineOutboundGuardedScratchWriter interface {
	SendDeadlineWithScratchGuarded(deadline time.Time, b *bin.Buffer, scratch *[]byte, guard func() error) error
}

var (
	errAuthKeyUnavailableAtPhysicalWrite = errors.New("auth key unavailable at physical write admission")
	errConnRetiredAtPhysicalWrite        = errors.New("connection retired at physical write admission")
)

func (c *Conn) writeFrame(ctx context.Context, frame *outboundFrame) error {
	if c.authKeyProtocolUnavailableNow() {
		c.fenceUnavailableAuthKey()
		return ErrConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// One absolute deadline covers the complete write attempt, including global scratch
	// admission. Previously writeTimeout only started after scratch was acquired, so one blocked
	// writer could make unrelated connections wait for their much longer RPC context deadline.
	deadline := c.outboundWriteDeadline(ctx)
	pool := c.ensureOutboundScratchPool()
	scratch, err := pool.acquireUntil(ctx, c.outboundStop, encryptedOutboundWireLen(len(frame.body)), deadline)
	if err != nil {
		return fmt.Errorf("reserve outbound write scratch: %w", err)
	}
	defer pool.release(scratch)
	out, err := c.encryptOutboundFrameInto(frame, &scratch.wire)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	// Capacity may become available at the deadline boundary, or encryption itself may consume
	// the remaining budget. No socket bytes exist yet, so return a non-terminal timeout instead
	// of calling the writer with an already-expired deadline and misclassifying it as a possibly
	// partial write.
	if err := prewriteDeadlineError(ctx, deadline); err != nil {
		return fmt.Errorf("outbound deadline before write: %w", err)
	}
	// Scratch admission and encryption may straddle expires_at. Recheck at the
	// final pre-write barrier so neither fresh sends nor resends use a stale key.
	if c.authKeyProtocolUnavailableNow() {
		c.fenceUnavailableAuthKey()
		return ErrConnClosed
	}

	writer := c.writer
	if writer == nil {
		writer = c.transport
	}
	if guarded, ok := writer.(deadlineOutboundGuardedScratchWriter); ok {
		err = guarded.SendDeadlineWithScratchGuarded(deadline, out, &scratch.codec, func() error {
			if c.isRetired() {
				return errConnRetiredAtPhysicalWrite
			}
			if c.authKeyProtocolUnavailableNow() {
				return errAuthKeyUnavailableAtPhysicalWrite
			}
			return nil
		})
	} else if sw, ok := writer.(deadlineOutboundScratchWriter); ok {
		err = sw.SendDeadlineWithScratch(deadline, out, &scratch.codec)
	} else if dw, ok := writer.(deadlineOutboundWriter); ok {
		err = dw.SendDeadline(deadline, out)
	} else {
		// 回落路径：gotd full codec / 测试注入 codec 仍走 ctx deadline。
		sendCtx := ctx
		cancel := func() {}
		if !deadline.IsZero() {
			sendCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		err = writer.Send(sendCtx, out)
		cancel()
	}
	if errors.Is(err, errAuthKeyUnavailableAtPhysicalWrite) {
		// The guarded lease has already released physical write ownership and no
		// raw bytes were emitted. Fence outside writeMu so Close cannot deadlock.
		c.fenceUnavailableAuthKey()
		return ErrConnClosed
	}
	if errors.Is(err, errConnRetiredAtPhysicalWrite) {
		// Terminal shutdown already owns lifecycle/transport close. Do not call
		// failTransport here: sendTerminalProtoError is waiting for this actor to
		// drain and must retain the lease long enough to write the final bare -404.
		return ErrConnClosed
	}
	if err != nil {
		// 任一 partial write / timeout 都可能破坏 MTProto 帧边界；该 socket
		// 不可继续复用。这里只发 terminal 信号，不在 actor 内等待自身退出。
		c.failTransport()
		return fmt.Errorf("send: %w", err)
	}
	if frame.sentAt.IsZero() {
		frame.sentAt = time.Now()
		frame.sends = 1
	}
	return nil
}

func (c *Conn) outboundWriteDeadline(ctx context.Context) time.Time {
	var deadline time.Time
	if c.writeTimeout > 0 {
		deadline = time.Now().Add(c.writeTimeout)
	}
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
			deadline = d
		}
	}
	return deadline
}

func prewriteDeadlineError(ctx context.Context, deadline time.Time) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (c *Conn) ensureOutboundScratchPool() *outboundScratchPool {
	c.outboundScratchOnce.Do(func() {
		if c.outboundScratchPool == nil {
			c.outboundScratchPool = newOutboundScratchPool(defaultOutboundWriteMaxBytes)
		}
	})
	return c.outboundScratchPool
}

func (c *Conn) encryptOutboundFrame(frame *outboundFrame) (*bin.Buffer, error) {
	wire := &bin.Buffer{Buf: make([]byte, encryptedOutboundWireLen(len(frame.body)))}
	return c.encryptOutboundFrameInto(frame, wire)
}

func encryptedOutboundWireLen(bodyLen int) int {
	plainWithoutPadding := encryptedFrameHeaderLen + bodyLen
	return 24 + plainWithoutPadding + encryptedPaddingLen(plainWithoutPadding)
}

func (c *Conn) encryptOutboundFrameInto(frame *outboundFrame, wire *bin.Buffer) (*bin.Buffer, error) {
	if frame == nil || wire == nil {
		return nil, errors.New("nil outbound frame scratch")
	}
	wireLen := encryptedOutboundWireLen(len(frame.body))
	ensureBinBufferLen(wire, wireLen)
	plain := wire.Buf[24:]
	binary.LittleEndian.PutUint64(plain[0:8], uint64(c.salt))
	binary.LittleEndian.PutUint64(plain[8:16], uint64(c.sessionID))
	binary.LittleEndian.PutUint64(plain[16:24], uint64(frame.msgID))
	binary.LittleEndian.PutUint32(plain[24:28], uint32(frame.seqNo))
	binary.LittleEndian.PutUint32(plain[28:32], uint32(len(frame.body)))
	copy(plain[encryptedFrameHeaderLen:], frame.body)

	paddingOffset := encryptedFrameHeaderLen + len(frame.body)
	// padding 随机数走 per-Conn 缓冲读：每帧 12..1024 字节直读 crypto/rand 是一次
	// getrandom syscall，缓冲后按 ~1KiB 批量取。只由 outbound actor 单 goroutine 访问，
	// 随机源本身不变（仍是 cipher 的 CSPRNG），只是预读。
	if c.outboundRand == nil {
		c.outboundRand = bufio.NewReaderSize(c.cipher.Rand(), 1024)
	}
	if _, err := io.ReadFull(c.outboundRand, plain[paddingOffset:]); err != nil {
		return nil, err
	}

	msgKey := crypto.MessageKey(c.key.Value, plain, crypto.Server)
	key, iv := crypto.Keys(c.key.Value, msgKey, crypto.Server)
	aesBlock, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}

	copy(wire.Buf[:len(c.key.ID)], c.key.ID[:])
	copy(wire.Buf[len(c.key.ID):len(c.key.ID)+len(msgKey)], msgKey[:])
	encryptIGEInPlace(aesBlock, iv[:], plain)
	return wire, nil
}

func encryptIGEInPlace(block cipher.Block, iv, buf []byte) {
	blockSize := block.BlockSize()
	if blockSize != aes.BlockSize || len(iv) != 2*blockSize || len(buf)%blockSize != 0 {
		panic("mtprotoedge: invalid in-place IGE dimensions")
	}
	previousCipher := iv[:blockSize]
	var previousPlain [aes.BlockSize]byte
	copy(previousPlain[:], iv[blockSize:])
	for offset := 0; offset < len(buf); offset += blockSize {
		current := buf[offset : offset+blockSize]
		var currentPlain [aes.BlockSize]byte
		copy(currentPlain[:], current)
		for i := range current {
			current[i] ^= previousCipher[i]
		}
		block.Encrypt(current, current)
		for i := range current {
			current[i] ^= previousPlain[i]
		}
		previousCipher = current
		previousPlain = currentPlain
	}
}

func encryptedPaddingLen(l int) int {
	return 16 + (16 - (l % 16))
}

func ensureBinBufferLen(b *bin.Buffer, n int) {
	if cap(b.Buf) < n {
		b.Buf = make([]byte, n)
		return
	}
	b.Buf = b.Buf[:n]
}

func frameNeedsAck(typeID uint32) bool {
	switch typeID {
	case mt.MsgsAckTypeID,
		mt.PongTypeID,
		mt.FutureSaltsTypeID,
		mt.BadMsgNotificationTypeID,
		mt.BadServerSaltTypeID,
		mt.MsgsStateInfoTypeID,
		mt.MsgsAllInfoTypeID,
		mt.MsgDetailedInfoTypeID,
		mt.MsgNewDetailedInfoTypeID,
		proto.MessageContainerTypeID:
		return false
	default:
		return true
	}
}

// encodedControlFrame identifies MTProto service responses independently from content-related
// sequencing. new_session_created and destroy_session_* are content-related (and therefore stay
// in resend tracking until ACK), but their small protocol-critical bodies must not compete with
// RPC results/updates for the general outbound body budget.
func encodedControlFrame(typeID uint32) bool {
	switch typeID {
	case mt.MsgsAckTypeID,
		mt.PongTypeID,
		mt.FutureSaltsTypeID,
		mt.BadMsgNotificationTypeID,
		mt.BadServerSaltTypeID,
		mt.MsgsStateInfoTypeID,
		mt.MsgsAllInfoTypeID,
		mt.MsgDetailedInfoTypeID,
		mt.MsgNewDetailedInfoTypeID,
		mt.NewSessionCreatedTypeID,
		mt.DestroySessionOkTypeID,
		mt.DestroySessionNoneTypeID,
		mt.RPCAnswerUnknownTypeID,
		mt.RPCAnswerDroppedRunningTypeID,
		mt.RPCAnswerDroppedTypeID:
		return true
	default:
		return false
	}
}

func outboundRequestMsgID(msg bin.Encoder) int64 {
	switch v := msg.(type) {
	case *proto.Result:
		return v.RequestMessageID
	case *destroyAuthKeyRPCResult:
		return v.RequestMessageID
	default:
		return 0
	}
}

// addReserved 接管调用方已经取得的全局 body 预算。pending 的每个元素恰好对应一份
// reservation；后续只有 removePending/releaseAll 能归还。
func (s *outboundState) addReserved(frame *outboundFrame) int {
	if _, exists := s.pending[frame.msgID]; exists {
		panic("mtprotoedge: duplicate outbound msg_id inserted into resend tracking")
	}
	if s.pending == nil {
		s.pending = make(map[int64]*outboundFrame)
	}
	s.pending[frame.msgID] = frame
	s.order = append(s.order, frame.msgID)
	s.totalBytes += len(frame.body)
	if frame.reqMsgID != 0 {
		if s.byRequest == nil {
			s.byRequest = make(map[int64]int64)
		}
		s.byRequest[frame.reqMsgID] = frame.msgID
	}
	return s.shrinkPending()
}

func (s *outboundState) ack(ids []int64) []int64 {
	var requestIDs []int64
	for _, id := range ids {
		frame, ok := s.pending[id]
		if !ok {
			continue
		}
		if frame.reqMsgID != 0 {
			requestIDs = append(requestIDs, frame.reqMsgID)
		}
		if !s.removePending(id) {
			continue
		}
		s.markAcked(id)
	}
	if len(s.order) > s.maxMessages*2 {
		s.compactOrder()
	}
	return requestIDs
}

func (s *outboundState) stateInfo(ids []int64) []byte {
	info := make([]byte, len(ids))
	for i, id := range ids {
		if s.isKnown(id) {
			info[i] = msgStateReceived
		}
	}
	return info
}

func (s *outboundState) isKnown(id int64) bool {
	if _, ok := s.pending[id]; ok {
		return true
	}
	_, ok := s.acked[id]
	return ok
}

func (s *outboundState) markAcked(id int64) {
	if _, ok := s.acked[id]; ok {
		return
	}
	if s.acked == nil {
		s.acked = make(map[int64]struct{})
	}
	s.acked[id] = struct{}{}
	s.ackOrder = append(s.ackOrder, id)
	for len(s.ackOrder) > maxTrackedAckedMsgIDs {
		oldest := s.ackOrder[0]
		s.ackOrder = s.ackOrder[1:]
		delete(s.acked, oldest)
	}
}

func (s *outboundState) shrinkPending() int {
	dropped := 0
	for (len(s.pending) > s.maxMessages || s.totalBytes > s.maxBytes) && len(s.order) > 0 {
		oldest := s.order[0]
		s.order = s.order[1:]
		if !s.removePending(oldest) {
			continue
		}
		dropped++
	}
	return dropped
}

func (s *outboundState) removePending(id int64) bool {
	frame, ok := s.pending[id]
	if !ok {
		return false
	}
	delete(s.pending, id)
	bytes := len(frame.body)
	s.totalBytes -= bytes
	if frame.reqMsgID != 0 {
		if mapped, exists := s.byRequest[frame.reqMsgID]; exists && mapped == id {
			delete(s.byRequest, frame.reqMsgID)
		}
	}
	// Clear the body reference before making these bytes available to another connection.
	frame.body = nil
	frame.releaseReservation(s.budget)
	return true
}

func (s *outboundState) releaseAll() {
	for _, frame := range s.pending {
		frame.body = nil
		frame.releaseReservation(s.budget)
	}
	s.pending = nil
	s.order = nil
	s.byRequest = nil
	s.totalBytes = 0
}

func (f *outboundFrame) releaseReservation(defaultBudget *outboundTrackedBudget) {
	if f == nil || f.reservedBytes <= 0 {
		return
	}
	budget := f.reservationBudget
	if budget == nil {
		budget = defaultBudget
	}
	bytes := f.reservedBytes
	f.reservedBytes = 0
	f.reservationBudget = nil
	budget.release(bytes)
}

func (s *outboundState) compactOrder() {
	filtered := s.order[:0]
	for _, id := range s.order {
		if _, ok := s.pending[id]; ok {
			filtered = append(filtered, id)
		}
	}
	s.order = filtered
}
