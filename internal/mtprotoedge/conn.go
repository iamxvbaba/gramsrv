package mtprotoedge

import (
	"bufio"
	"context"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/transport"

	"telesrv/internal/compat/layerwire"
)

// Conn 是一个已识别 session 的客户端连接，持有向其加密发送消息所需的全部上下文。
// 由 SessionManager 管理，供请求响应与主动 push 共用。
//
// Send 并发安全：所有出站消息先进 per-Conn outbound actor，由它串行分配 msg_id/seq_no、
// 加密并写 transport，避免高并发 RPC 响应与 push 交错造成 MTProto 顺序错误。
type outboundWriter interface {
	Send(context.Context, *bin.Buffer) error
}

type connLifecycle uint32

const (
	// The zero value is deliberately provisional so test/embedded Conn values start
	// outside every SessionManager index until they complete activation.
	connLifecycleProvisional connLifecycle = iota
	connLifecycleClaiming
	connLifecycleActive
	// retired is terminal and irreversible. A physical connection that lost an
	// activation claim must never become visible again, even if its read goroutine
	// was already between preflight and publish when a replacement arrived.
	connLifecycleRetired
)

type Conn struct {
	transport transport.Conn
	// transportLease owns exactly one generation of the physical transport.
	// It is nil only for directly constructed test/embedded Conns that retain
	// the legacy raw-transport close fallback.
	transportLease *physicalTransportLease
	writer         outboundWriter
	cipher         crypto.Cipher
	msgID          *proto.MessageIDGen
	writeTimeout   time.Duration
	metrics        Metrics

	authKeyID [8]byte
	// authKeyHex 是 authKeyID 的 hex 缓存：每条 RPC 的结构化日志都会带它，
	// 建连时算一次，避免热路径反复 hex 编码分配。
	authKeyHex string
	sessionID  int64
	salt       int64
	key        crypto.AuthKey

	outbound        chan outboundOp
	outboundControl chan outboundOp
	outboundStop    chan struct{}
	outboundDone    chan struct{}
	outboundClose   sync.Once
	// outboundEnqueueMu orders producer registration against terminal close. Close
	// flips closing under this lock before waiting, so no WaitGroup Add can race Wait.
	outboundEnqueueMu sync.Mutex
	outboundEnqueueWG sync.WaitGroup
	outboundClosing   bool
	// Queue backing is intentionally small and bounded per Conn; control has a separate queue
	// and strict actor priority. Server-created connections share outboundTrackedBudget.
	outboundQueueSize        int
	outboundControlQueueSize int
	outboundTrackedBudget    *outboundTrackedBudget
	outboundBudgetOnce       sync.Once
	// Encoded MTProto service frames and control vectors use independent headroom: pong,
	// new_session_created, bad_msg and msgs_ack must remain admissible when the body budget is
	// full. Content-related control frames keep this budget while pending for resend.
	outboundControlTrackedBudget *outboundTrackedBudget
	outboundControlBudgetOnce    sync.Once
	outboundScratchPool          *outboundScratchPool
	outboundScratchOnce          sync.Once
	// lifecycle is the sole monotonic activation/retirement state machine.
	// retired never transitions back to claiming/active; one atomic state avoids
	// contradictory activation and shutdown observations.
	lifecycle      atomic.Uint32
	transportClose sync.Once

	rpcScheduler *inboundRPCScheduler
	rpcCancel    context.CancelFunc
	rpcClose     sync.Once
	rpcMu        sync.Mutex
	rpcWG        sync.WaitGroup
	// rpcReservationWG 跟踪 Copy 前预算到 commit/abort 的短窗口，使 Close 返回时
	// 全局/单连接预算都已归还或转交给明确的 queued/running task。
	rpcReservationWG sync.WaitGroup
	rpcTimeout       time.Duration
	rpcQueue         []inboundRPC
	rpcQueueSize     int
	rpcReserved      int
	rpcRunning       int
	rpcReady         bool
	rpcClosed        bool
	// inflightRPCBytes 跟踪已入队未完成的 inbound RPC body 总字节，配合 maxInflightRPCBytes
	// 给 RPC 队列设字节预算（不止限条数），防对抗客户端发大请求撑内存。
	inflightRPCBytes atomic.Int64
	// 单连接只保留并发配额；实际 worker 来自 Server 共享池，避免每连接预留 goroutine。
	rpcRootCtx     context.Context
	rpcMaxInflight int

	// sentContentMessages 只由 outbound actor 访问，用于生成 MTProto seq_no。
	sentContentMessages int32
	// outboundRand 只由 outbound actor 访问：对 cipher 随机源的缓冲预读，
	// 把每帧 padding 的 getrandom syscall 摊薄成 ~1KiB 一次。
	outboundRand *bufio.Reader

	identityMu              sync.RWMutex
	businessAuthKeyID       [8]byte
	businessAuthKeyHex      string
	businessAuthKeyResolved bool
	userID                  atomic.Int64
	userIDResolved          atomic.Bool
	receivesUpdates         atomic.Bool
	// membershipsSynced 表示该连接的 channel membership 推送路由（byMemberChannel）
	// 已成功建立。它与 receivesUpdates 共同构成「session 完全就绪」：membership
	// 同步失败时保持 false，让置位短路放行、下一条 RPC 重试同步，避免
	// 「已置位但 channel 路由缺失」的 session 静默漏收超级群推送。
	membershipsSynced atomic.Bool
	// membershipGen 是本连接 channel membership 索引的修订号：任何增量修订
	// （join/leave/kick 的 Add/Remove、身份切换/下线的整体清除）都递增。全量同步方
	// 在读取持久成员列表前采样、落地时带回比对，检测「读取窗口内发生增量修订」的
	// 丢失更新竞态（SetSessionChannelMemberships 改走合并路径并保持未就绪重试）。
	membershipGen atomic.Int64
	// createdAt 是连接建立时刻，供同 auth_key session 数触顶时驱逐真正最旧的连接。
	createdAt time.Time
	// clientLayer 是本连接协商的 TL layer（invokeWithLayer/initConnection），由 handleRPC
	// 在每次 Dispatch 后从 RPC 注册表刷新。出站(rpc_result/push)按此把 227 对象降级给老客户端；
	// 0 表示尚未协商，按 canonical(227) 处理=不降级。
	clientLayer atomic.Int32
}

func (c *Conn) lifecycleState() connLifecycle {
	if c == nil {
		return connLifecycleRetired
	}
	return connLifecycle(c.lifecycle.Load())
}

func (c *Conn) isRetired() bool {
	return c == nil || c.lifecycleState() == connLifecycleRetired
}

// retire irreversibly fences the logical connection. The caller that wins the
// transition may additionally own one-shot physical cleanup.
func (c *Conn) retire() bool {
	if c == nil {
		return false
	}
	for {
		state := c.lifecycle.Load()
		if connLifecycle(state) == connLifecycleRetired {
			return false
		}
		if c.lifecycle.CompareAndSwap(state, uint32(connLifecycleRetired)) {
			return true
		}
	}
}

func (c *Conn) beginActivationClaim() bool {
	if c == nil || !c.isPhysicalTransportCurrentOpen() {
		return false
	}
	if !c.lifecycle.CompareAndSwap(uint32(connLifecycleProvisional), uint32(connLifecycleClaiming)) {
		return false
	}
	// Physical close can win after the pre-check but before the lifecycle CAS.
	// Do not let a doomed claimant enter SessionManager and retire a healthy old
	// owner for the same logical session.
	if c.lifecycleState() != connLifecycleClaiming || !c.isPhysicalTransportCurrentOpen() {
		c.retire()
		return false
	}
	return true
}

func (c *Conn) publishActivation() bool {
	if c == nil || !c.isPhysicalTransportCurrentOpen() {
		return false
	}
	if !c.lifecycle.CompareAndSwap(uint32(connLifecycleClaiming), uint32(connLifecycleActive)) {
		return false
	}
	// A concurrent transport failure can retire the Conn between the first
	// physical check and the CAS. Never let that intermediate active value escape.
	if c.lifecycleState() != connLifecycleActive || !c.isPhysicalTransportCurrentOpen() {
		c.retire()
		return false
	}
	return true
}

func (c *Conn) isActive() bool {
	return c != nil && c.lifecycleState() == connLifecycleActive && c.isPhysicalTransportCurrentOpen()
}

// transferTransportOwnership hands this Conn's physical socket to the next
// logical generation. The caller must have fenced and drained the old writer.
func (c *Conn) transferTransportOwnership() (*physicalTransportLease, bool) {
	if c == nil || c.transportLease == nil {
		return nil, false
	}
	return c.transportLease.Transfer()
}

func (c *Conn) isPhysicalTransportCurrentOpen() bool {
	return c != nil && (c.transportLease == nil || c.transportLease.IsCurrentOpen())
}

// ClientLayer 返回连接协商的 TL layer；未协商时返回 canonical layer（227，不降级）。
func (c *Conn) ClientLayer() int {
	if l := c.clientLayer.Load(); l != 0 {
		return int(l)
	}
	return layerwire.CanonicalLayer
}

// SetClientLayer 记录连接协商的 TL layer。
func (c *Conn) SetClientLayer(layer int) { c.clientLayer.Store(int32(layer)) }

// AuthKeyID 返回连接的 auth_key_id。
func (c *Conn) AuthKeyID() [8]byte { return c.authKeyID }

// BusinessAuthKeyID 返回业务视角的 auth_key_id。
//
// temp auth_key 绑定后解析为 perm auth_key；第二个返回值表示本连接是否已完成解析，
// 即便解析结果等于原始 auth_key_id 也会返回 true，以避免每个 RPC 重复查绑定表。
func (c *Conn) BusinessAuthKeyID() ([8]byte, bool) {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.businessAuthKeyID, c.businessAuthKeyResolved
}

// BusinessAuthKeyHex 返回业务视角 auth_key_id 的 hex 缓存（每 RPC 日志用，免重复编码）。
func (c *Conn) BusinessAuthKeyHex() (string, bool) {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.businessAuthKeyHex, c.businessAuthKeyResolved
}

// SetBusinessAuthKeyID 缓存业务视角 auth_key_id。
func (c *Conn) SetBusinessAuthKeyID(id [8]byte) {
	c.identityMu.Lock()
	changed := !c.businessAuthKeyResolved || c.businessAuthKeyID != id
	if changed || c.businessAuthKeyHex == "" {
		c.businessAuthKeyHex = hex.EncodeToString(id[:])
	}
	c.businessAuthKeyID = id
	c.businessAuthKeyResolved = true
	c.identityMu.Unlock()
	if changed {
		c.userID.Store(0)
		c.userIDResolved.Store(false)
	}
}

// SessionID 返回连接的 session_id。
func (c *Conn) SessionID() int64 { return c.sessionID }

// UserID 返回绑定的用户 id；未登录为 0。
func (c *Conn) UserID() int64 { return c.userID.Load() }

// UserIDResolved 返回 user_id 授权状态是否已为当前连接解析过。
//
// resolved=true 且 userID=0 表示该 auth_key 当前未登录；这样登录前的多次 RPC
// 不会反复查询授权表，后续登录成功会由 BindUser 覆盖为真实用户。
func (c *Conn) UserIDResolved() (userID int64, resolved bool) {
	return c.userID.Load(), c.userIDResolved.Load()
}

// ReceivesUpdates 报告该连接是否接收主动推送的 updates。
func (c *Conn) ReceivesUpdates() bool { return c.receivesUpdates.Load() }

// SetReceivesUpdates 设置该连接是否接收主动推送的 updates。
// 登录后的主连接在 updates.getState/getDifference 建立同步基线后置为 true。
func (c *Conn) SetReceivesUpdates(v bool) { c.receivesUpdates.Store(v) }
