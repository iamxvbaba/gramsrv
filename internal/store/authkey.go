package store

import (
	"context"
	"errors"
	"math"
)

var (
	// ErrInvalidAuthKeyProtocolExpiry 表示新握手试图写入 migration-only
	// unknown sentinel 或超出 TL int32 时间戳范围的协议寿命。
	ErrInvalidAuthKeyProtocolExpiry = errors.New("invalid auth key protocol expiry")
	// ErrAuthKeyProtocolMetadataConflict 表示同一 cryptographic auth_key_id
	// 被尝试改写为另一 key body 或另一 permanent/temp 类型/寿命。
	ErrAuthKeyProtocolMetadataConflict = errors.New("auth key protocol metadata conflict")
	// ErrAuthKeyNotPermanent 防止 authorization 落到 temporary/legacy-unknown key。
	ErrAuthKeyNotPermanent = errors.New("auth key is not permanent")
	// ErrAuthKeyBindingInvalid 表示 temp/perm 引用缺失、类型错误，或 binding
	// expiry 未归一到握手权威值。
	ErrAuthKeyBindingInvalid = errors.New("invalid temporary auth key binding")
)

// ValidNewAuthKeyProtocolExpiry 只允许握手写 permanent(0) 或 TL int32
// 范围内的 positive temporary expiry。-1 仅能由 migration 0086 写入。
func ValidNewAuthKeyProtocolExpiry(expiresAt int) bool {
	return expiresAt >= 0 && int64(expiresAt) <= math.MaxInt32
}

// AuthKeyData 是一条持久化的 MTProto auth key 记录。
//
// 不依赖 td 协议类型：连接层在边界做 crypto.AuthKey ↔ AuthKeyData 转换。
type AuthKeyData struct {
	ID         [8]byte   // auth_key_id（key 的 SHA1 低 64 位）
	Value      [256]byte // 2048-bit auth key
	ServerSalt int64     // 密钥交换产出的初始 server salt
	CreatedAt  int64     // unix 秒
	// ExpiresAt 是 temporary/media-temporary auth key 的协议失效时间（unix 秒）。
	// 0 只允许表示 permanent key；-1 仅表示 migration 0086 无法证明类型的历史 key，
	// edge 必须用 -404 拒绝并迫使客户端重握手。key 类型是握手事实，不能由
	// authorization 是否存在推断。
	ExpiresAt     int
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
	// Save 保存一条 auth key 记录；同 ID 重试只能保持 key body 与协议类型/寿命不变。
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
