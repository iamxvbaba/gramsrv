package domain

import "time"

// Authorization 是一条设备授权：auth_key 与 user 的绑定 + initConnection 设备信息。
// auth_key 是协议产物、授权是业务产物，故独立于 store.AuthKeyData。
type Authorization struct {
	AuthKeyID [8]byte // 协议原生 auth_key_id；store 边界按小端转 int64
	UserID    int64
	Hash      int64
	// Layer is the last supported protocol profile explicitly observed for this
	// auth key. It is a durable default for a new session, never an instruction
	// to rewrite an already-active session's own profile.
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
	IP            string
	// PasswordPending 表示该 auth_key 已通过短信验证码、但账号开启了两步验证且尚未通过
	// auth.checkPassword。此状态下业务鉴权须视其为未登录，仅允许继续完成两步验证。
	PasswordPending bool
	CreatedAt       time.Time
	ActiveAt        time.Time
}

// AuthKeyClientInfo 是未登录 auth_key 也需要保留的客户端协商元数据。
// 登录后的设备授权仍由 Authorization 表达。Layer 保存最后一次受支持的显式
// wire profile，供服务端重启后为同一 auth key 的新 session 初始化默认值；活跃
// session 仍以自己的显式 invokeWithLayer 纠正值为准。
type AuthKeyClientInfo struct {
	Layer int
	// LayerObservationID is a read-only ordering token owned by the protocol
	// store. Generic client metadata updates must never manufacture or advance
	// it; only ordered invokeWithLayer evidence may do so.
	LayerObservationID int64
	DeviceModel        string
	Platform           string
	SystemVersion      string
	APIID              int
	AppVersion         string
}
