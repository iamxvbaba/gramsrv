package store

import (
	"context"

	"telesrv/internal/domain"
)

// AlbumGroupStore 持久化 sendMultiMedia 的预发送分组预留。
//
// ReserveAlbumGroup 必须原子满足：
//   - 请求内没有旧绑定时，全部 random_id 绑定 ProposedGroupedID；
//   - 命中唯一旧 grouped_id 时，全部缺失项收敛到该旧值；
//   - 命中多个旧 grouped_id 时返回 domain.ErrMessageRandomIDDuplicate，且不写入；
//   - 并发、多实例的重叠请求等价于某个串行顺序。
type AlbumGroupStore interface {
	ReserveAlbumGroup(ctx context.Context, req domain.AlbumGroupReservationRequest) (groupedID int64, err error)
}
