package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// filterChatInvitePrivacy evaluates the whole target vector through the
// privacy read model before any membership write. The returned slices preserve
// request order.
func (r *Router) filterChatInvitePrivacy(ctx context.Context, inviterUserID int64, targetUserIDs []int64) ([]int64, []tg.MissingInvitee, error) {
	if len(targetUserIDs) == 0 {
		return nil, nil, nil
	}
	if r.deps.Privacy == nil {
		return append([]int64(nil), targetUserIDs...), nil, nil
	}
	visible := make(map[int64]bool, len(targetUserIDs))
	if batch, ok := r.deps.Privacy.(batchPrivacyEvaluator); ok {
		matrix, err := batch.CanSeeBatch(ctx, targetUserIDs, inviterUserID, []domain.PrivacyKey{domain.PrivacyKeyChatInvite})
		if err != nil {
			return nil, nil, err
		}
		for _, targetUserID := range targetUserIDs {
			visible[targetUserID] = matrix[targetUserID][domain.PrivacyKeyChatInvite]
		}
	} else {
		for _, targetUserID := range targetUserIDs {
			allowed, err := r.deps.Privacy.CanSee(ctx, targetUserID, inviterUserID, domain.PrivacyKeyChatInvite)
			if err != nil {
				return nil, nil, err
			}
			visible[targetUserID] = allowed
		}
	}
	allowed := make([]int64, 0, len(targetUserIDs))
	missing := make([]tg.MissingInvitee, 0)
	for _, targetUserID := range targetUserIDs {
		if visible[targetUserID] {
			allowed = append(allowed, targetUserID)
			continue
		}
		missing = append(missing, tg.MissingInvitee{UserID: targetUserID})
	}
	return allowed, missing, nil
}

func emptyInvitedUsersUpdates(date int) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}
