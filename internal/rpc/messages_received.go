package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
)

// onMessagesReceivedMessages acknowledges the client's highest observed
// message id. telesrv does not enqueue mobile PUSH notifications, so there are
// no pending notifications to cancel and the exact cancellation set is empty.
// This acknowledgement must never advance read boundaries or update state.
func (r *Router) onMessagesReceivedMessages(ctx context.Context, _ int) ([]tg.ReceivedNotifyMessage, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	return []tg.ReceivedNotifyMessage{}, nil
}
