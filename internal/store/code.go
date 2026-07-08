package store

import (
	"context"
	"time"
)

// PhoneCode 是一条登录验证码记录（与某次 sendCode 的 phone_code_hash 或邮箱验证键关联）。
type PhoneCode struct {
	Phone          string
	Code           string
	Channel        string
	Email          string
	PendingEmail   string
	Attempts       int
	MaxAttempts    int
	VerifiedEmail  bool
	RequireSignUp  bool
	LoginEmailHash string
}

// CodeStore 暂存登录验证码：phone_code_hash → 手机号 + 验证码，带 TTL。
// 实现见 store/memory（测试替身）、store/redisstore。
type CodeStore interface {
	Set(ctx context.Context, phoneCodeHash string, code PhoneCode, ttl time.Duration) error
	Get(ctx context.Context, phoneCodeHash string) (PhoneCode, bool, error)
	Update(ctx context.Context, phoneCodeHash string, code PhoneCode) error
	Del(ctx context.Context, phoneCodeHash string) error
}
