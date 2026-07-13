package store

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// ErrTempAuthKeyAlreadyBound 表示同一 temporary key 已绑定到另一个 permanent key。
// temp key 的 canonical identity 在首次 bind 后不可漂移，重放同一绑定才允许幂等成功。
var ErrTempAuthKeyAlreadyBound = errors.New("temporary auth key already bound")

// TempAuthKeyBindingStore 持久化 auth.bindTempAuthKey 的 temp→perm 绑定。
type TempAuthKeyBindingStore interface {
	Save(ctx context.Context, binding domain.TempAuthKeyBinding) error
	GetByTemp(ctx context.Context, tempAuthKeyID [8]byte) (domain.TempAuthKeyBinding, bool, error)
	// DeleteExpired 以 auth_keys 的握手协议 expiry 为唯一事实源，回收早于
	// expiredBefore（unix 秒）的 temporary key，单次最多 limit 条并返回 key 数。
	// 未绑定与已绑定的 PFS key 必须走同一路径；绑定经 ON DELETE CASCADE 清除。
	DeleteExpired(ctx context.Context, expiredBefore int64, limit int) (int, error)
}
