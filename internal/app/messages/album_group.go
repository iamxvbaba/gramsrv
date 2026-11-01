package messages

import (
	"context"
	"errors"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ReserveAlbumGroup hands a batch of RPC-validated album items to the persistence layer for atomic reservation.
// This capability carries only domain DTOs; uploaded-media resolution and tg types stay at the RPC edge.
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
