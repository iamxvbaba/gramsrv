package domain

import "time"

// Authorization 是一条设备授权：auth_key 与 user 的绑定 + initConnection 设备信息。
// auth_key 是协议产物、授权是业务产物，故独立于 store.AuthKeyData。
type Authorization struct {
	AuthKeyID     [8]byte // 协议原生 auth_key_id；store 边界按小端转 int64
	UserID        int64
	Hash          int64
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
// 登录后的设备授权仍由 Authorization 表达；这里仅用于服务端重启后恢复
// pre-auth / setup 流程的 client type 与 layer。
type AuthKeyClientInfo struct {
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
}
