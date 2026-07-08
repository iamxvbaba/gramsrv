package store

import "context"

// AuthKeyData 是一条持久化的 MTProto auth key 记录。
//
// 不依赖 td 协议类型：连接层在边界做 crypto.AuthKey ↔ AuthKeyData 转换。
type AuthKeyData struct {
	ID            [8]byte   // auth_key_id（key 的 SHA1 低 64 位）
	Value         [256]byte // 2048-bit auth key
	ServerSalt    int64     // 密钥交换产出的初始 server salt
	CreatedAt     int64     // unix 秒
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
	// 用户绑定不在此处：auth_key 是协议产物，授权（auth_key↔user + 设备信息）由 authorization 承载（P2）。
}

type AuthKeyClientInfo struct {
	Layer         int
	DeviceModel   string
	Platform      string
	SystemVersion string
	APIID         int
	AppVersion    string
}

// AuthKeyStore 持久化 auth key。实现见 store/memory（测试替身）、store/postgres。
type AuthKeyStore interface {
	// Save 保存或覆盖一条 auth key 记录。
	Save(ctx context.Context, k AuthKeyData) error
	// Get 按 auth_key_id 查询；不存在时 found=false。
	Get(ctx context.Context, id [8]byte) (data AuthKeyData, found bool, err error)
	// UpdateClientInfo 合并更新 auth key 的客户端协商元数据。
	// 空字段不覆盖已有值，layer/api_id 为 0 时不覆盖。
	UpdateClientInfo(ctx context.Context, id [8]byte, info AuthKeyClientInfo) error
	// Delete 删除一条 auth key 记录（destroy_auth_key）。不存在时静默成功。
	// 连接层每帧按 auth_key_id 回查本接口，删除后该 key 的入站帧立即失效。
	Delete(ctx context.Context, id [8]byte) error
}
