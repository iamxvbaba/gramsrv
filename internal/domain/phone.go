package domain

// PhoneCallState 是私聊 1:1 通话信令状态机的服务端状态。
// 状态迁移、TL 视角映射与推送策略见 docs 与 internal/app/phone。
type PhoneCallState string

const (
	// PhoneCallStateRequested：requestCall 已受理，已向被叫推 phoneCallRequested。
	PhoneCallStateRequested PhoneCallState = "requested"
	// PhoneCallStateRinging：至少一台被叫设备上报 receivedCall（主叫据 receive_date 切振铃 UI）。
	PhoneCallStateRinging PhoneCallState = "ringing"
	// PhoneCallStateAccepted：被叫 acceptCall 已落 g_b，等主叫 confirmCall。
	PhoneCallStateAccepted PhoneCallState = "accepted"
	// PhoneCallStateConfirmed：主叫 confirmCall 完成密钥交换，通话进行中。
	PhoneCallStateConfirmed PhoneCallState = "confirmed"
	// PhoneCallStateDiscarded：终态。作为 tombstone 保留一段时间吸收晚到 RPC 后回收。
	PhoneCallStateDiscarded PhoneCallState = "discarded"
)

// PhoneCallDiscardReason 是挂断原因，与 TL phoneCallDiscardReason* 一一对应。
type PhoneCallDiscardReason string

const (
	PhoneCallDiscardReasonMissed     PhoneCallDiscardReason = "missed"
	PhoneCallDiscardReasonDisconnect PhoneCallDiscardReason = "disconnect"
	PhoneCallDiscardReasonHangup     PhoneCallDiscardReason = "hangup"
	PhoneCallDiscardReasonBusy       PhoneCallDiscardReason = "busy"
	// PhoneCallDiscardReasonMigrateConference 是升级为 conference call 的迁移挂断；
	// 当前不支持 conference，仅原样回显 reason。
	PhoneCallDiscardReasonMigrateConference PhoneCallDiscardReason = "migrate_conference"
)

// PhoneCallProtocol 是 tgcalls 协议协商参数（TL phoneCallProtocol）。
type PhoneCallProtocol struct {
	UDPP2P          bool
	UDPReflector    bool
	MinLayer        int
	MaxLayer        int
	LibraryVersions []string
}

// SessionRef 标识一台具体设备连接（信令定向推送 fast-path 的锚点，允许失效）。
type SessionRef struct {
	RawAuthKeyID [8]byte
	SessionID    int64
}

// Zero 报告锚点是否未记录。
func (s SessionRef) Zero() bool {
	return s == SessionRef{}
}

// PhoneCallConnection 是下发给双方的 WebRTC STUN/TURN 服务条目
// （TL phoneConnectionWebrtc）。requestCall 受理时签发，全生命周期稳定。
type PhoneCallConnection struct {
	ID       int64
	IP       string
	Port     int
	Username string
	Password string
	Stun     bool
	Turn     bool
}

// PhoneCallRequest 是 phone.requestCall 受理入参（隐私/拉黑等校验在 rpc 层先行完成）。
type PhoneCallRequest struct {
	CalleeID     int64
	RandomID     int64
	GAHash       []byte
	Video        bool
	Protocol     PhoneCallProtocol
	CallerDevice SessionRef
	// PrivacyP2P 是 phone_p2p 隐私的双向 AND（rpc 层算定；强制 relay 时置 false）。
	PrivacyP2P bool
	// Connections 是为本通话签发的 STUN/TURN 列表（可空：TURN 未启用时退回
	// 纯信令交换 host candidates 的 LAN 直连）。
	Connections []PhoneCallConnection
}

// PhoneCall 是一通私聊通话的服务端权威态。密钥材料（GAHash/GB/GA）只存活于
// 进程内 registry，随 tombstone 回收销毁，绝不落任何持久化存储。
type PhoneCall struct {
	ID         int64
	AccessHash int64
	// AdminID 是主叫，ParticipantID 是被叫；与 TL phoneCall 字段同名，全生命周期不变。
	AdminID       int64
	ParticipantID int64
	Video         bool
	State         PhoneCallState

	Date        int // requestCall 受理时刻（unix 秒）
	ReceiveDate int // 首台被叫设备 receivedCall 时刻；0 表示尚未触达
	StartDate   int // confirmCall 完成时刻
	DiscardedAt int // 进入终态时刻（tombstone GC 依据）

	GAHash         []byte // 32B，requestCall 携带的承诺
	GB             []byte // 256B，acceptCall 携带
	GA             []byte // 256B，confirmCall 揭示（服务端核验 SHA256(GA)==GAHash）
	KeyFingerprint int64  // E2E 指纹，服务端无法验证，仅转发

	// Protocol 是协商结果（accept 时计算）；CallerProtocol/CalleeProtocol 保留原始两份。
	Protocol       PhoneCallProtocol
	CallerProtocol PhoneCallProtocol
	CalleeProtocol PhoneCallProtocol

	P2PAllowed    bool
	DiscardReason PhoneCallDiscardReason
	// DiscardReasonSlug is set for migrate_conference and must resolve through
	// phone.getGroupCall(inputGroupCallSlug) before the discarded update is sent.
	DiscardReasonSlug string
	Duration          int

	// PrivacyP2P 与 Connections 自 PhoneCallRequest 原样保留（见其注释）。
	PrivacyP2P  bool
	Connections []PhoneCallConnection

	RandomID int64 // 主叫侧幂等去重键的一半（callerID+RandomID）

	// 设备锚点：requestCall / acceptCall 的来源设备，仅作定向推送提示。
	CallerDevice SessionRef
	CalleeDevice SessionRef
}

// Terminal 报告通话是否已进入终态。
func (c PhoneCall) Terminal() bool {
	return c.State == PhoneCallStateDiscarded
}

// PeerOf 返回 userID 在本通话中的对端；userID 不是参与者时返回 0。
func (c PhoneCall) PeerOf(userID int64) int64 {
	switch userID {
	case c.AdminID:
		return c.ParticipantID
	case c.ParticipantID:
		return c.AdminID
	default:
		return 0
	}
}

// HasParticipant 报告 userID 是否为本通话参与者。
func (c PhoneCall) HasParticipant(userID int64) bool {
	return userID != 0 && (userID == c.AdminID || userID == c.ParticipantID)
}
