package store

import (
	"context"
	"time"
)

const PhoneCodePurposeChangePhone = "change_phone"

// PhoneCode 是一条验证码记录（与某次 sendCode 的 phone_code_hash 或邮箱验证键关联）。
// Purpose/UserID/AuthKeyID/SessionID 为已登录敏感操作提供作用域；登录验证码保持零值。
type PhoneCode struct {
	Phone          string
	Code           string
	Channel        string
	Purpose        string
	UserID         int64
	AuthKeyID      [8]byte
	SessionID      int64
	Email          string
	PendingEmail   string
	Attempts       int
	MaxAttempts    int
	VerifiedEmail  bool
	RequireSignUp  bool
	LoginEmailHash string
}

// PhoneCodeScope 标识已登录敏感操作的一次性验证码作用域。SessionID 故意不在
// 作用域内：同一 perm auth key 等待验证码期间允许重建 MTProto session。
// 登录/注册验证码没有 Purpose/UserID/AuthKeyID，保持非 scoped 行为。
type PhoneCodeScope struct {
	Purpose   string
	UserID    int64
	AuthKeyID [8]byte
	Phone     string
}

func (c PhoneCode) Scope() PhoneCodeScope {
	return PhoneCodeScope{
		Purpose:   c.Purpose,
		UserID:    c.UserID,
		AuthKeyID: c.AuthKeyID,
		Phone:     c.Phone,
	}
}

func (s PhoneCodeScope) Valid() bool {
	return s.Purpose != "" && s.UserID != 0 && s.AuthKeyID != ([8]byte{}) && s.Phone != ""
}

// CodeStore 暂存验证码：phone_code_hash → 作用域 + 手机号 + 验证码，带 TTL。
// 实现见 store/memory（测试替身）、store/redisstore。
type CodeStore interface {
	// Set 对 scoped code 必须原子替换同作用域旧 hash，保证单作用域至多一个
	// 活跃验证码；普通登录码仍按 hash 独立保存。
	Set(ctx context.Context, phoneCodeHash string, code PhoneCode, ttl time.Duration) error
	Get(ctx context.Context, phoneCodeHash string) (PhoneCode, bool, error)
	Update(ctx context.Context, phoneCodeHash string, code PhoneCode) error
	Del(ctx context.Context, phoneCodeHash string) error
	// ConsumeScoped 仅当 hash 仍是 scope 的当前活跃 hash 时原子读取并删除；
	// 并发调用至多一个返回 found=true。
	ConsumeScoped(ctx context.Context, phoneCodeHash string, scope PhoneCodeScope) (PhoneCode, bool, error)
}
