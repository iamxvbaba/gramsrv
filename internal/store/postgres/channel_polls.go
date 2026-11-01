package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// Channel/supergroup message poll voting/closing: membership and message visibility reuse the same validation as reactions,
// and poll-level semantics are delegated to poll.go's shared SQL.

func (s *ChannelStore) VoteChannelMessagePoll(ctx context.Context, req domain.VoteChannelMessagePollRequest) (domain.ChannelMessagePollResult, error) {
	return s.mutateChannelMessagePoll(ctx, req.UserID, req.ChannelID, req.MessageID, req.Date, func(ctx context.Context, tx pgx.Tx, def domain.PollDefinition, date int) error {
		return applyPollVote(ctx, tx, def, req.UserID, req.Options, date)
	})
}

func (s *ChannelStore) CloseChannelMessagePoll(ctx context.Context, req domain.CloseChannelMessagePollRequest) (domain.ChannelMessagePollResult, error) {
	return s.mutateChannelMessagePoll(ctx, req.UserID, req.ChannelID, req.MessageID, req.Date, func(ctx context.Context, tx pgx.Tx, def domain.PollDefinition, _ int) error {
		return closePollAsCreator(ctx, tx, def, req.UserID)
	})
}

func (s *ChannelStore) mutateChannelMessagePoll(
	ctx context.Context,
	userID, channelID int64,
	messageID int,
	date int,
	mutate func(ctx context.Context, tx pgx.Tx, def domain.PollDefinition, date int) error,
) (domain.ChannelMessagePollResult, error) {
	if userID == 0 || channelID == 0 || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.ChannelMessagePollResult{}, domain.ErrChannelInvalid
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessagePollResult{}, fmt.Errorf("mutate channel message poll: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessagePollResult{}, fmt.Errorf("begin channel poll tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	msg, err := s.getChannelMessage(ctx, tx, channelID, messageID)
	if err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindPoll || msg.Media.Poll == nil || msg.Media.Poll.ID == 0 {
		return domain.ChannelMessagePollResult{}, domain.ErrMessageIDInvalid
	}
	pollID := msg.Media.Poll.ID
	defs, err := loadPollDefinitions(ctx, tx, []int64{pollID}, true)
	if err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	def, ok := defs[pollID]
	if !ok {
		return domain.ChannelMessagePollResult{}, domain.ErrPollNotFound
	}
	if err := mutate(ctx, tx, def, date); err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	if err := enrichPollMediaRefs(ctx, tx, []pollMediaRef{{media: msg.Media, viewer: userID}}); err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessagePollResult{}, fmt.Errorf("commit channel poll tx: %w", err)
	}
	committed = true
	return domain.ChannelMessagePollResult{
		PollID:  pollID,
		Channel: channel,
		Message: msg,
	}, nil
}

// populateChannelMessagesPolls enriches all poll media in the page from the requesting viewer's perspective;
// it is uniformly mounted by populateChannelMessagesReactions (all channel message read paths share one choke point).
func (s *ChannelStore) populateChannelMessagesPolls(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, messages []domain.ChannelMessage) error {
	refs := make([]pollMediaRef, 0, 2)
	for i := range messages {
		refs = append(refs, pollMediaRef{media: messages[i].Media, viewer: viewerUserID})
	}
	return enrichPollMediaRefs(ctx, db, refs)
}
