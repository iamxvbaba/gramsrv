package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// filterChatInvitePrivacy evaluates the whole target vector through the
// privacy read model before any membership write. The returned slices preserve
// request order.
//
// Bots are not subject to the chat_invite rule: like Telegram, whether a bot
// may be added is decided only by its bot_nochats setting (/setjoingroups),
// enforced by the membership write (BOT_GROUPS_BLOCKED). Evaluating the rule
// would test the inviter against the bot's own contact list, which a bot never
// has, so the contacts-only default made every bot uninvitable. A failed bot
// lookup aborts the invite before any membership write instead of silently
// evaluating the bot as a human target.
func (r *Router) filterChatInvitePrivacy(ctx context.Context, inviterUserID int64, targetUserIDs []int64) ([]int64, []tg.MissingInvitee, error) {
	if len(targetUserIDs) == 0 {
		return nil, nil, nil
	}
	if r.deps.Privacy == nil {
		return append([]int64(nil), targetUserIDs...), nil, nil
	}
	visible := make(map[int64]bool, len(targetUserIDs))
	humans := make([]int64, 0, len(targetUserIDs))
	for _, targetUserID := range targetUserIDs {
		bot, _, err := r.lookupUserBotStatus(ctx, targetUserID)
		if err != nil {
			return nil, nil, err
		}
		if bot {
			visible[targetUserID] = true
			continue
		}
		humans = append(humans, targetUserID)
	}
	if len(humans) == 0 {
		// only bots: nothing to evaluate
	} else if batch, ok := r.deps.Privacy.(batchPrivacyEvaluator); ok {
		matrix, err := batch.CanSeeBatch(ctx, humans, inviterUserID, []domain.PrivacyKey{domain.PrivacyKeyChatInvite})
		if err != nil {
			return nil, nil, err
		}
		for _, targetUserID := range humans {
			visible[targetUserID] = matrix[targetUserID][domain.PrivacyKeyChatInvite]
		}
	} else {
		for _, targetUserID := range humans {
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
