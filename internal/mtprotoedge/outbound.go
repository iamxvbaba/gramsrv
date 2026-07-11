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
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"

	"telesrv/internal/compat/layerwire"
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
	defaultOutboundQueueSize        = 128
	defaultOutboundControlQueueSize = 32
	defaultOutboundTrackedMaxBytes  = int64(512 << 20) // 512 MiB / Server
	defaultOutboundControlMaxBytes  = int64(64 << 20)  // ack/state/resend vectors / Server

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

type outboundOp struct {
	kind    outboundOpKind
	control bool
	ctx     context.Context
	msgType proto.MessageType
	msg     bin.Encoder
	encoded *encodedOutboundMessage
	// reservedBytes accounts for the encoded body while it is queued. For a
	// content frame the reservation is transferred to resend tracking after a
	// successful write; every other terminal path releases it exactly once.
	reservedBytes     int
	reservationBudget *outboundTrackedBudget
	ids               []int64
	reqMsgID          int64
	enqueuedAt        time.Time
	done              chan outboundResult
}

type encodedOutboundMessage struct {
	body     []byte
	typeID   uint32
	reqMsgID int64
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
	sentAt            time.Time
	sends             int
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
	if c.outboundControlQueueSize <= 0 {
		c.outboundControlQueueSize = defaultOutboundControlQueueSize
	}
	c.ensureOutboundTrackedBudget()
	c.outbound = make(chan outboundOp, c.outboundQueueSize)
	c.outboundControl = make(chan outboundOp, c.outboundControlQueueSize)
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
	c.terminal.Store(true)
	c.signalOutboundStop()
	c.beginCloseInboundRPCScheduler()
}

func (c *Conn) waitOutboundShutdown() {
	if c.outboundDone != nil {
		<-c.outboundDone
	}
}

// ForceClose 停止连接并关闭底层 transport。
// 仅用于授权撤销 / destroy_auth_key 这类“必须让对端立即断线”的路径；普通生命周期仍由
// serveConn 统一关闭 transport，避免正常 push/索引清理把长连接误伤成硬断。
func (c *Conn) ForceClose() {
	c.beginTerminalShutdown()
	c.closeTransport()
	c.closeInboundRPCScheduler()
	c.waitOutboundShutdown()
}

// closeTransport 只关闭物理 transport，不等待 outbound actor。写失败路径运行在
// actor 自身 goroutine 中，若在这里调用 Close 会等待 outboundDone 而自锁。
func (c *Conn) closeTransport() {
	c.transportClose.Do(func() {
		if c.transport != nil {
			_ = c.transport.Close()
		}
	})
}

// failTransport 把不可恢复的写错误提升为连接级 terminal failure。它只负责
// 标记 terminal + 关闭 socket；handleOutboundOp 返回后，actor 自己发停止信号并退出，
// serveConn 被 Close 解开 Recv 后负责注销索引。
func (c *Conn) failTransport() {
	// Publish both producer gates before Close: a custom/broken transport may block in
	// Close, but it must not keep accepting queued bodies or RPC work meanwhile.
	c.beginTerminalShutdown()
	c.closeTransport()
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

// SendPriority 加密并优先发送一条 server 控制消息。
func (c *Conn) SendPriority(ctx context.Context, t proto.MessageType, msg bin.Encoder) error {
	return c.send(ctx, t, msg, true)
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
	if c.terminal.Load() {
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
	select {
	case c.outbound <- op:
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
	c.metrics.OutboundQueueWait(len(c.outbound), cap(c.outbound))
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
	case c.outbound <- op:
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
	return c.sendOutbound(ctx, t, nil, encoded, false)
}

func (c *Conn) sendOutbound(ctx context.Context, t proto.MessageType, msg bin.Encoder, encoded *encodedOutboundMessage, control bool) error {
	if c.outbound == nil || c.outboundControl == nil {
		return ErrConnClosed
	}
	op, err := c.newOutboundSendOp(ctx, t, msg, encoded, control)
	if err != nil {
		c.failOutboundBudget(err)
		return err
	}
	if !c.beginOutboundEnqueue() {
		op.releaseReservation(c.outboundTrackedBudget)
		return ErrConnClosed
	}
	op.control = control
	op.ctx = ctx
	op.enqueuedAt = time.Now()
	op.done = make(chan outboundResult, 1)
	if err := c.enqueueOutboundRegistered(ctx, op); err != nil {
		op.releaseReservation(c.outboundTrackedBudget)
		c.endOutboundEnqueue()
		return err
	}
	c.endOutboundEnqueue()
	select {
	case res := <-op.done:
		return res.err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.outboundStop:
		return ErrConnClosed
	}
}

// SendAsync 入队一条 server 消息但不等待发送结果（fire-and-forget），用于读循环里的控制消息
// （ack/pong/new_session_created/bad_msg/future_salts/state_info）：避免读循环被 outbound 写
// 阻塞而连带卡死。走优先(control)队列保证不被普通 push 拖后；队列满时丢弃并记 metrics——此时
// 连接多已严重拥塞，控制消息丢失由客户端重传 / 读写超时兜底。返回非 nil 仅表示连接已关闭。
func (c *Conn) SendAsync(ctx context.Context, t proto.MessageType, msg bin.Encoder) error {
	if c.outbound == nil || c.outboundControl == nil {
		return ErrConnClosed
	}
	if c.terminal.Load() {
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
	if len(ids) == 0 || c.outbound == nil || c.outboundControl == nil || c.terminal.Load() {
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
	if c.terminal.Load() {
		return ErrConnClosed
	}
	q := c.outbound
	if op.control {
		q = c.outboundControl
	}
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

func (c *Conn) beginOutboundEnqueue() bool {
	c.outboundEnqueueMu.Lock()
	defer c.outboundEnqueueMu.Unlock()
	if c.outboundClosing || c.terminal.Load() {
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
	defer func() {
		// pending frames belong exclusively to this actor. Releasing after drain ensures no
		// resend path can race the final budget return and no Conn body survives actor exit.
		state.releaseAll()
		close(c.outboundDone)
	}()
	for {
		if c.terminal.Load() {
			c.signalOutboundStop()
			c.drainOutbound()
			return
		}
		select {
		case op := <-c.outboundControl:
			if c.terminal.Load() {
				op.releaseReservation(c.outboundTrackedBudget)
				op.finish(outboundResult{err: ErrConnClosed})
				c.signalOutboundStop()
				c.drainOutbound()
				return
			}
			c.handleOutboundOp(state, op)
			if c.terminal.Load() {
				c.signalOutboundStop()
				c.drainOutbound()
				return
			}
			continue
		default:
		}
		select {
		case <-c.outboundStop:
			c.drainOutbound()
			return
		case op := <-c.outboundControl:
			if c.terminal.Load() {
				op.releaseReservation(c.outboundTrackedBudget)
				op.finish(outboundResult{err: ErrConnClosed})
				c.signalOutboundStop()
				c.drainOutbound()
				return
			}
			c.handleOutboundOp(state, op)
		case op := <-c.outbound:
			if c.terminal.Load() {
				op.releaseReservation(c.outboundTrackedBudget)
				op.finish(outboundResult{err: ErrConnClosed})
				c.signalOutboundStop()
				c.drainOutbound()
				return
			}
			c.handleOutboundOp(state, op)
		}
		if c.terminal.Load() {
			c.signalOutboundStop()
			c.drainOutbound()
			return
		}
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
			op.releaseReservation(c.outboundTrackedBudget)
			op.finish(outboundResult{err: ErrConnClosed})
		case op := <-c.outbound:
			op.releaseReservation(c.outboundTrackedBudget)
			op.finish(outboundResult{err: ErrConnClosed})
		default:
			return
		}
	}
}

func (c *Conn) handleOutboundOp(state *outboundState, op outboundOp) {
	if op.kind != outboundSend {
		defer op.releaseReservation(state.budget)
	}
	switch op.kind {
	case outboundSend:
		c.handleOutboundSend(state, op)
	case outboundAck:
		state.ack(op.ids)
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
	frame, err := c.buildFrame(op.ctx, op.msgType, op.msg, op.encoded)
	reserved := op.reservedBytes
	reservationBudget := op.reservationBudget
	if reservationBudget == nil {
		reservationBudget = state.budget
	}
	op.reservedBytes = 0
	op.reservationBudget = nil
	// A per-connection layer downgrade can allocate a different body. Reserve the
	// replacement before dropping the canonical queue reservation so the transient
	// two-body peak is also covered by the Server budget.
	if err == nil && frame != nil && op.encoded != nil && !sameBacking(frame.body, op.encoded.body) {
		if !reservationBudget.reserve(len(frame.body)) {
			err = ErrOutboundTrackedBudget
		} else {
			reservationBudget.release(reserved)
			reserved = len(frame.body)
			op.encoded = nil
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
	reservationBudget.release(reserved)
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
		if err := c.writeFrame(ctx, frame); err != nil {
			c.metrics.OutboundResend(resent, err)
			return info, err
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
			reservedBytes:     bytes,
			reservationBudget: budget,
		}, nil
	}
	if encoded == nil {
		return outboundOp{}, errors.New("nil encoded outbound message")
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
		reservedBytes:     bytes,
		reservationBudget: budget,
	}, nil
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
	c.failTransport()
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
	// 出站统一在此按本连接协商 layer 降级：
	//   - push fan-out 用 onceEncodedOutbound 把更新编码一次(canonical)再 SendEncoded 给多条
	//     连接共享，故必须在此**逐连接**降级，且**绝不改共享 encoded**(downgradedClone 拷贝)。
	//   - rpc_result 的内层对象已在 encodeRPCResult 按 layer 降级，其 mt.* 外壳在此为顶层直通(no-op)。
	//   - 控制消息(mt.*)顶层直通。layer>=227 整条零开销。
	encoded = c.downgradedCloneContext(ctx, encoded)
	content := frameNeedsAck(encoded.typeID)
	msgID := c.msgID.New(t)
	return &outboundFrame{
		msgID:    msgID,
		seqNo:    c.peekSeqNo(content),
		typeID:   encoded.typeID,
		body:     encoded.body,
		reqMsgID: encoded.reqMsgID,
	}, nil
}

// downgradedClone 返回按本连接协商 layer 降级后的消息，**绝不修改入参**——push fan-out
// 多条连接共享同一 encoded，逐连接降级必须各自拷贝，否则会污染其他连接的字节。
// layer>=227 或 Transcode 直通(mt.* / 无变化)时原样返回入参，零拷贝。降级失败 fail-safe：
// 返回 canonical 并计 metrics（宁可老客户端对个别长尾对象渲染异常，也不让连接/流崩）。
func (c *Conn) downgradedClone(encoded *encodedOutboundMessage) *encodedOutboundMessage {
	return c.downgradedCloneContext(context.Background(), encoded)
}

func (c *Conn) downgradedCloneContext(ctx context.Context, encoded *encodedOutboundMessage) *encodedOutboundMessage {
	if encoded == nil {
		return nil
	}
	if c.ClientLayer() >= layerwire.CanonicalLayer {
		return encoded
	}
	var down []byte
	err := withOutboundEncodeSlot(ctx, c.outboundStop, func() error {
		var err error
		down, err = layerwire.Transcode(encoded.body, c.ClientLayer())
		return err
	})
	if err != nil {
		c.metrics.OutboundDropped("layerwire_downgrade_failed")
		return encoded
	}
	if sameBacking(down, encoded.body) {
		return encoded // 直通：未变(mt.*/顶层未知)，无需拷贝或重算 typeID
	}
	out := &encodedOutboundMessage{body: down, typeID: encoded.typeID, reqMsgID: encoded.reqMsgID}
	if id, e := (&bin.Buffer{Buf: down}).PeekID(); e == nil {
		out.typeID = id
	}
	return out
}

// sameBacking reports whether a and b share the same backing array (Transcode
// returns its input unchanged for passthrough cases).
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
		typeID:   typeID,
		body:     body.Raw(),
		reqMsgID: outboundRequestMsgID(msg),
	}, nil
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

func (c *Conn) writeFrame(ctx context.Context, frame *outboundFrame) error {
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

	writer := c.writer
	if writer == nil {
		writer = c.transport
	}
	if dw, ok := writer.(deadlineOutboundWriter); ok {
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
		mt.RPCAnswerDroppedTypeID,
		destroyAuthKeyOkTypeID,
		destroyAuthKeyFailTypeID:
		return true
	default:
		return false
	}
}

func outboundRequestMsgID(msg bin.Encoder) int64 {
	switch v := msg.(type) {
	case *proto.Result:
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

func (s *outboundState) ack(ids []int64) {
	for _, id := range ids {
		if !s.removePending(id) {
			continue
		}
		s.markAcked(id)
	}
	if len(s.order) > s.maxMessages*2 {
		s.compactOrder()
	}
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
