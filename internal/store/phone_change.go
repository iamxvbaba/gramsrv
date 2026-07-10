package store

import (
	"context"

	"telesrv/internal/domain"
)

// PhoneChangeStore 原子修改账号手机号并记录可恢复的 updateUserPhone 事件。
// 生产实现还必须在同一事务入 dispatch outbox。
type PhoneChangeStore interface {
	ChangePhone(ctx context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error)
}
