package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// EditFactCheck writes or deletes a message's fact-check label.
//
// Permissions match channel message editing: the message author themself, or the channel creator / an admin
// with the edit_messages right. The fact check hangs on the message row, so all subscribers read the same verdict.
//
// The write consumes one channel pts and produces an edit_message event, on which the client updates the fact-check
// label in place on the original message.
func (s *ChannelStore) EditFactCheck(ctx context.Context, req domain.EditFactCheckRequest) (domain.EditFactCheckResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 {
		return domain.EditFactCheckResult{}, domain.ErrChannelInvalid
	}
	deleting := strings.TrimSpace(req.Text) == ""
	var check *domain.FactCheck
	if !deleting {
		candidate := domain.FactCheck{
			NeedCheck: req.NeedCheck,
			Country:   strings.ToUpper(strings.TrimSpace(req.Country)),
			Text:      req.Text,
			Entities:  req.Entities,
		}
		if err := domain.ValidateFactCheck(candidate); err != nil {
			return domain.EditFactCheckResult{}, err
		}
		candidate.Hash = domain.FactCheckHash(req.ChannelID, req.MessageID, candidate)
		check = &candidate
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	payload, err := encodeFactCheck(check)
	if err != nil {
		return domain.EditFactCheckResult{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditFactCheckResult{}, fmt.Errorf("edit fact check: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditFactCheckResult{}, fmt.Errorf("begin edit fact check: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditFactCheckResult{}, err
	}
	if !channel.Broadcast {
		return domain.EditFactCheckResult{}, domain.ErrChannelInvalid
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.EditFactCheckResult{}, err
	}
	if msg.Deleted || msg.Action != nil {
		return domain.EditFactCheckResult{}, domain.ErrMessageIDInvalid
	}
	if msg.SenderUserID != req.UserID && !canEditChannelMessage(member) {
		return domain.EditFactCheckResult{}, domain.ErrFactCheckForbidden
	}
	if domain.FactChecksEqual(msg.FactCheck, check) {
		return domain.EditFactCheckResult{}, domain.ErrMessageNotModified
	}
	pts, err := s.reserveChannelPts(ctx, tx, req.ChannelID)
	if err != nil {
		return domain.EditFactCheckResult{}, fmt.Errorf("allocate channel fact check pts: %w", err)
	}
	msg, err = scanChannelMessage(tx.QueryRow(ctx, `
UPDATE channel_messages
SET factcheck = $3,
    pts = $4,
    updated_at = now()
WHERE channel_id = $1 AND id = $2 AND NOT deleted
RETURNING `+channelMessageColumns,
		req.ChannelID, req.MessageID, string(payload), pts))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EditFactCheckResult{}, domain.ErrMessageIDInvalid
	}
	if err != nil {
		return domain.EditFactCheckResult{}, fmt.Errorf("update channel fact check: %w", err)
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateEditMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      msg,
		SenderUserID: req.UserID,
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.EditFactCheckResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET pts = $2, updated_at = now() WHERE id = $1`, req.ChannelID, pts); err != nil {
		return domain.EditFactCheckResult{}, fmt.Errorf("update channel fact check pts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditFactCheckResult{}, fmt.Errorf("commit edit fact check: %w", err)
	}
	committed = true
	channel.Pts = pts
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.EditFactCheckResult{Channel: channel, Message: msg, FactCheck: check, Event: event, Recipients: recipients}, nil
}
