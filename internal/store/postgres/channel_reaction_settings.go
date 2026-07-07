package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ChannelStore) SetAvailableReactions(ctx context.Context, userID, channelID int64, policy domain.ChannelReactionPolicy) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	policyJSON, err := marshalJSON(policy, "{}")
	if err != nil {
		return domain.Channel{}, err
	}
	var channel domain.Channel
	if err := withTx(ctx, s.db, "set channel available reactions", func(tx pgx.Tx) error {
		var err error
		var member domain.ChannelMember
		channel, member, err = s.getChannelForMember(ctx, tx, userID, channelID)
		if err != nil {
			return err
		}
		if !canChangeChannelInfo(member) {
			return domain.ErrChannelAdminRequired
		}
		if _, err := tx.Exec(ctx, `UPDATE channels SET available_reactions = $2, updated_at = now() WHERE id = $1`, channelID, policyJSON); err != nil {
			return fmt.Errorf("update channel available reactions: %w", err)
		}
		channel.ReactionPolicy = policy
		return nil
	}); err != nil {
		return domain.Channel{}, err
	}
	if s.rowCache != nil {
		s.rowCache.put(channel)
	}
	return channel, nil
}
