package messages

import (
	"context"
	"errors"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ReserveAlbumGroup 把 RPC 已验证的一批 album item 交给持久层原子预留。
// 该能力只传 domain DTO；上传媒体解析与 tg 类型仍停留在 RPC edge。
func (s *Service) ReserveAlbumGroup(ctx context.Context, userID int64, req domain.AlbumGroupReservationRequest) (int64, error) {
	if s == nil || s.messages == nil || userID <= 0 {
		return 0, domain.ErrAlbumGroupReservationInvalid
	}
	if req.SenderUserID == 0 {
		req.SenderUserID = userID
	}
	if req.SenderUserID != userID {
		return 0, domain.ErrAlbumGroupReservationInvalid
	}
	reservations, ok := s.messages.(store.AlbumGroupStore)
	if !ok {
		return 0, errors.New("message store does not support album group reservations")
	}
	return reservations.ReserveAlbumGroup(ctx, req)
}
