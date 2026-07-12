package store

import (
	"context"

	"telesrv/internal/domain"
)

// AuthorizationStore 持久化设备授权（auth_key ↔ user 绑定）。实现见 store/memory（测试替身）、store/postgres。
type AuthorizationStore interface {
	Bind(ctx context.Context, a domain.Authorization) error
	ByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
	UpdateLayer(ctx context.Context, authKeyID [8]byte, layer int) error
	// UpdateClientInfo 合并更新已绑定授权的客户端元数据，使设备列表与 auth key 协商事实一致。
	UpdateClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error
	ListByUser(ctx context.Context, userID int64) ([]domain.Authorization, error)
	Delete(ctx context.Context, authKeyID [8]byte) error
	DeleteByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	DeleteByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
	// MarkPasswordPassed 清除 auth_key 的 password_pending 标记，使其转为完全授权（两步验证通过后调用）。
	MarkPasswordPassed(ctx context.Context, authKeyID [8]byte) error
}
