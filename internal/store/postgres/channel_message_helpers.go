package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) eventForChannelMessage(ctx context.Context, channelID int64, messageID int) (domain.ChannelUpdateEvent, error) {
	row := s.db.QueryRow(ctx, `
SELECT channel_id, pts, pts_count, date, event_type, message_id, message_ids::text, sender_user_id, user_ids::text, payload::text
FROM channel_update_events
WHERE channel_id = $1 AND message_id = $2 AND event_type = $3
ORDER BY pts ASC LIMIT 1`, channelID, messageID, string(domain.ChannelUpdateNewMessage))
	event, _, err := scanChannelEvent(row)
	return event, err
}

func scanChannelMessage(row rowScanner) (domain.ChannelMessage, error) {
	var msg domain.ChannelMessage
	var fromType string
	var sendAsType sql.NullString
	var sendAsID sql.NullInt64
	var replyMsgID, replyTopID int
	var replyPeerType string
	var replyPeerID int64
	var discussionChannelID int64
	var discussionMessageID int
	var entities, reply, forward, action string
	var mediaJSON string
	var replyMarkupJSON string
	var richMessageJSON string
	var savedPeerType string
	var savedPeerID int64
	var suggestedPostJSON string
	var factCheckJSON string
	if err := row.Scan(
		&msg.ChannelID, &msg.ID, &msg.RandomID, &msg.SenderUserID, &fromType, &msg.From.ID,
		&sendAsType, &sendAsID, &msg.Date, &msg.EditDate, &msg.Post, &msg.Silent, &msg.NoForwards,
		&msg.Body, &entities, &reply, &replyMsgID, &replyPeerType, &replyPeerID, &replyTopID,
		&forward, &discussionChannelID, &discussionMessageID, &action, &msg.Pts, &msg.Deleted, &mediaJSON,
		&replyMarkupJSON, &richMessageJSON, &msg.TTLPeriod, &msg.ExpiresAt, &msg.ViewsCount, &msg.PostAuthor, &msg.Pinned, &msg.ViaBotID, &msg.GroupedID, &msg.FromBoostsApplied, &savedPeerType, &savedPeerID, &msg.PaidMessageStars, &suggestedPostJSON, &msg.ShowMessageSender, &factCheckJSON,
	); err != nil {
		return domain.ChannelMessage{}, err
	}
	msg.From.Type = domain.PeerType(fromType)
	msg.SavedPeer = domain.Peer{Type: domain.PeerType(savedPeerType), ID: savedPeerID}
	msg.SuggestedPost = decodeJSONPtr[domain.SuggestedPost](suggestedPostJSON)
	if sendAsType.Valid && sendAsID.Valid {
		msg.SendAs = &domain.Peer{Type: domain.PeerType(sendAsType.String), ID: sendAsID.Int64}
	}
	parsedEntities, err := decodeMessageEntities(entities)
	if err != nil {
		return domain.ChannelMessage{}, err
	}
	msg.Entities = parsedEntities
	msg.ReplyTo = channelMessageReplyFromColumns(decodeJSONPtr[domain.MessageReply](reply), replyMsgID, replyPeerType, replyPeerID, replyTopID)
	msg.Forward = decodeJSONPtr[domain.MessageForward](forward)
	if discussionChannelID != 0 && discussionMessageID != 0 {
		msg.Discussion = &domain.ChannelDiscussionRef{ChannelID: discussionChannelID, MessageID: discussionMessageID}
	}
	msg.Action = decodeJSONPtr[domain.ChannelMessageAction](action)
	msg.Media, err = decodeMessageMedia(mediaJSON)
	if err != nil {
		return domain.ChannelMessage{}, err
	}
	msg.ReplyMarkup, err = decodeReplyMarkup(replyMarkupJSON)
	if err != nil {
		return domain.ChannelMessage{}, err
	}
	msg.RichMessage, err = decodeRichMessage(richMessageJSON)
	if err != nil {
		return domain.ChannelMessage{}, err
	}
	msg.FactCheck, err = decodeFactCheck(factCheckJSON)
	if err != nil {
		return domain.ChannelMessage{}, err
	}
	return msg, nil
}

func scanChannelMessageWithCount(row rowScanner) (domain.ChannelMessage, int, error) {
	var msg domain.ChannelMessage
	var fromType string
	var sendAsType sql.NullString
	var sendAsID sql.NullInt64
	var replyMsgID, replyTopID int
	var replyPeerType string
	var replyPeerID int64
	var discussionChannelID int64
	var discussionMessageID int
	var entities, reply, forward, action string
	var count int
	var mediaJSON string
	var replyMarkupJSON string
	var richMessageJSON string
	var savedPeerType string
	var savedPeerID int64
	var suggestedPostJSON string
	var factCheckJSON string
	if err := row.Scan(
		&msg.ChannelID, &msg.ID, &msg.RandomID, &msg.SenderUserID, &fromType, &msg.From.ID,
		&sendAsType, &sendAsID, &msg.Date, &msg.EditDate, &msg.Post, &msg.Silent, &msg.NoForwards,
		&msg.Body, &entities, &reply, &replyMsgID, &replyPeerType, &replyPeerID, &replyTopID,
		&forward, &discussionChannelID, &discussionMessageID, &action, &msg.Pts, &msg.Deleted, &mediaJSON,
		&replyMarkupJSON, &richMessageJSON, &msg.TTLPeriod, &msg.ExpiresAt, &msg.ViewsCount, &msg.PostAuthor, &msg.Pinned, &msg.ViaBotID, &msg.GroupedID, &msg.FromBoostsApplied, &savedPeerType, &savedPeerID, &msg.PaidMessageStars, &suggestedPostJSON, &msg.ShowMessageSender, &factCheckJSON, &count,
	); err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	msg.From.Type = domain.PeerType(fromType)
	msg.SavedPeer = domain.Peer{Type: domain.PeerType(savedPeerType), ID: savedPeerID}
	msg.SuggestedPost = decodeJSONPtr[domain.SuggestedPost](suggestedPostJSON)
	if sendAsType.Valid && sendAsID.Valid {
		msg.SendAs = &domain.Peer{Type: domain.PeerType(sendAsType.String), ID: sendAsID.Int64}
	}
	parsedEntities, err := decodeMessageEntities(entities)
	if err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	msg.Entities = parsedEntities
	msg.ReplyTo = channelMessageReplyFromColumns(decodeJSONPtr[domain.MessageReply](reply), replyMsgID, replyPeerType, replyPeerID, replyTopID)
	msg.Forward = decodeJSONPtr[domain.MessageForward](forward)
	if discussionChannelID != 0 && discussionMessageID != 0 {
		msg.Discussion = &domain.ChannelDiscussionRef{ChannelID: discussionChannelID, MessageID: discussionMessageID}
	}
	msg.Action = decodeJSONPtr[domain.ChannelMessageAction](action)
	msg.Media, err = decodeMessageMedia(mediaJSON)
	if err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	msg.ReplyMarkup, err = decodeReplyMarkup(replyMarkupJSON)
	if err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	msg.RichMessage, err = decodeRichMessage(richMessageJSON)
	if err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	msg.FactCheck, err = decodeFactCheck(factCheckJSON)
	if err != nil {
		return domain.ChannelMessage{}, 0, err
	}
	return msg, count, nil
}

func channelMessageReplyFromColumns(reply *domain.MessageReply, msgID int, peerType string, peerID int64, topID int) *domain.MessageReply {
	if reply != nil {
		if reply.MessageID == 0 {
			reply.MessageID = msgID
		}
		if reply.TopMessageID == 0 {
			reply.TopMessageID = topID
		}
		if reply.Peer.ID == 0 && peerType != "" && peerID != 0 {
			reply.Peer = domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
		}
		if reply.MessageID <= 0 && reply.TopMessageID <= 0 {
			return nil
		}
		return reply
	}
	if msgID <= 0 && topID <= 0 {
		return nil
	}
	out := &domain.MessageReply{
		MessageID:    msgID,
		TopMessageID: topID,
	}
	if peerType != "" && peerID != 0 {
		out.Peer = domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
	}
	return out
}

type pgChannelMessageIDAllocator struct {
	db sqlcgen.DBTX
}

// NewChannelMessageIDAllocator returns the Postgres-backed allocator. The
// monolith and the standalone tools (cmd/subscribebackfill) share this one so
// every writer advances the same per-channel counter; the previous split where
// the server used Redis and tools used MAX(id)+1 let the counters drift, and
// the next server-written id collided with a tool-written row.
func NewChannelMessageIDAllocator(db sqlcgen.DBTX) store.ChannelMessageIDAllocator {
	return pgChannelMessageIDAllocator{db: db}
}

func (a pgChannelMessageIDAllocator) NextChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	// Allocate from a per-channel counter row inside a single statement. The
	// row lock serializes concurrent writers, so a registration auto-subscribe
	// join racing a message send can no longer pick the same id (the old
	// MAX(id)+1 read outside the write transaction caused channel_messages_pkey
	// violations). GREATEST keeps the counter ahead if rows were inserted
	// without it (restore, manual backfill) and MAX(id) is only a safety net;
	// the counter, not the message table, drives the sequence.
	var id int64
	err := a.db.QueryRow(ctx, `
INSERT INTO channel_message_id_counters AS c (channel_id, next_id)
VALUES ($1, COALESCE((SELECT MAX(id) FROM channel_messages WHERE channel_id = $1), 0) + 1)
ON CONFLICT (channel_id) DO UPDATE
SET next_id = GREATEST(c.next_id + 1, (SELECT COALESCE(MAX(id), 0) + 1 FROM channel_messages WHERE channel_id = $1))
RETURNING next_id`, channelID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("allocate channel message id: %w", err)
	}
	return int(id), nil
}

func (a pgChannelMessageIDAllocator) CurrentChannelMessageID(ctx context.Context, channelID int64) (int, error) {
	var id int
	err := a.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channel_messages WHERE channel_id = $1`, channelID).Scan(&id)
	return id, err
}
