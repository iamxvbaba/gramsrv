package rpc

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) reserveAlbumGroup(ctx context.Context, userID int64, peer domain.Peer, items []domain.AlbumGroupReservationItem) (int64, error) {
	reservations, ok := r.deps.Messages.(AlbumGroupService)
	if !ok {
		r.log.Error("messages.sendMultiMedia album reservation capability missing",
			append(r.contextLogFields(ctx), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID))...)
		return 0, internalErr()
	}
	groupedID, err := reservations.ReserveAlbumGroup(ctx, userID, domain.AlbumGroupReservationRequest{
		SenderUserID:      userID,
		Peer:              peer,
		Items:             items,
		ProposedGroupedID: randomNonZeroInt64(),
	})
	if errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		return 0, randomIDDuplicateErr()
	}
	if err != nil {
		r.log.Error("messages.sendMultiMedia album reservation failed",
			append(r.contextLogFields(ctx), zap.Error(err), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID), zap.Int("items", len(items)))...)
		return 0, internalErr()
	}
	if groupedID == 0 {
		r.log.Error("messages.sendMultiMedia album reservation returned zero grouped_id",
			append(r.contextLogFields(ctx), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID))...)
		return 0, internalErr()
	}
	return groupedID, nil
}
