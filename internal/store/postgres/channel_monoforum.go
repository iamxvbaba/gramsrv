package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// SendMonoforumMessage 向 monoforum(频道私信)虚拟频道发一条消息,按 saved_peer 分订阅者子会话。
// 私信消息存进 channel_messages(复用 channel pts/事件/difference);发件权限(订阅者身份/管理员)
// 由 RPC 层校验,store 只校验 monoforum 频道存在,不要求发件人是成员(订阅者不是 monoforum 成员)。
func (s *ChannelStore) SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.MonoforumID == 0 || req.SenderUserID == 0 || req.SavedPeer.ID == 0 ||
		req.SavedPeer.Type != domain.PeerTypeUser || strings.TrimSpace(req.Message) == "" {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	if req.RandomID != 0 {
		if dup, found, err := s.duplicateMonoforumMessage(ctx, req.MonoforumID, req.SenderUserID, req.SavedPeer, req.RandomID); err != nil {
			return domain.SendChannelMessageResult{}, err
		} else if found {
			return dup, nil
		}
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SendChannelMessageResult{}, fmt.Errorf("send monoforum message: db does not support transactions")
	}
	if err := s.ensureChannelMessageIDCounter(ctx, req.MonoforumID); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("begin send monoforum: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, err := getChannelByID(ctx, tx, req.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
		}
		return domain.SendChannelMessageResult{}, err
	}
	if channel.Deleted || !channel.Monoforum {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, req.MonoforumID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate monoforum message id: %w", err)
	}
	pts, err := s.reserveChannelPts(ctx, tx, req.MonoforumID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate monoforum pts: %w", err)
	}
	msg := domain.ChannelMessage{
		ChannelID:    req.MonoforumID,
		ID:           msgID,
		RandomID:     req.RandomID,
		SenderUserID: req.SenderUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
		SavedPeer:    req.SavedPeer,
		Date:         req.Date,
		Body:         req.Message,
		Entities:     append([]domain.MessageEntity(nil), req.Entities...),
		Pts:          pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.MonoforumID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      msg,
		SenderUserID: req.SenderUserID,
	}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		if isUniqueViolation(err) {
			// 唯一约束按 (channel,sender,random_id) 三元组;只有同一订阅者子会话的真重发才算重复。
			// 跨子会话复用同一 random_id(异常客户端)按 saved_peer 过滤后命中不到 → 干净返错,不串消息。
			dup, found, dupErr := s.duplicateMonoforumMessage(ctx, req.MonoforumID, req.SenderUserID, req.SavedPeer, req.RandomID)
			if dupErr != nil || !found {
				return domain.SendChannelMessageResult{}, dupErr
			}
			dup.Duplicate = true
			return dup, nil
		}
		return domain.SendChannelMessageResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, req.MonoforumID, msgID, pts); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("update monoforum top: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("commit send monoforum: %w", err)
	}
	committed = true
	channel.TopMessageID = msgID
	channel.Pts = pts
	return domain.SendChannelMessageResult{Channel: channel, Message: msg, Event: event}, nil
}

// ListMonoforumHistory 拉取某订阅者(saved_peer)在 monoforum 内的私信历史,id 倒序分页。
func (s *ChannelStore) ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error) {
	if filter.MonoforumID == 0 || filter.SavedPeer.ID == 0 {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	channel, err := getChannelByID(ctx, s.db, filter.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelHistory{}, domain.ErrChannelInvalid
		}
		return domain.ChannelHistory{}, err
	}
	if !channel.Monoforum {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	args := []any{filter.MonoforumID, string(filter.SavedPeer.Type), filter.SavedPeer.ID}
	where := `channel_id = $1 AND saved_peer_type = $2 AND saved_peer_id = $3 AND NOT deleted`
	if filter.OffsetID > 0 {
		where += fmt.Sprintf(` AND id < $%d`, len(args)+1)
		args = append(args, filter.OffsetID)
	}
	rows, err := s.db.Query(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE `+where+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)+1), append(args, limit)...)
	if err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("list monoforum history: %w", err)
	}
	defer rows.Close()
	var msgs []domain.ChannelMessage
	for rows.Next() {
		m, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelHistory{}, err
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM channel_messages WHERE channel_id = $1 AND saved_peer_type = $2 AND saved_peer_id = $3 AND NOT deleted`,
		filter.MonoforumID, string(filter.SavedPeer.Type), filter.SavedPeer.ID).Scan(&count); err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("count monoforum history: %w", err)
	}
	return domain.ChannelHistory{Messages: msgs, Count: count, Channel: channel}, nil
}

// ResolveMonoforumSend 按 id 取 monoforum 频道(不要求调用者是 monoforum 成员),并返回调用者是否为
// 其母广播频道的创建者/管理员。非 monoforum/不存在 → ErrChannelInvalid。
func (s *ChannelStore) ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error) {
	if viewerUserID == 0 || monoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	mono, err := getChannelByID(ctx, s.db, monoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, false, domain.ErrChannelInvalid
		}
		return domain.Channel{}, false, err
	}
	if mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	isAdmin := false
	if _, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, mono.LinkedMonoforumID); err == nil {
		isAdmin = member.Status == domain.ChannelMemberActive &&
			(member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin)
	}
	return mono, isAdmin, nil
}

// duplicateMonoforumMessage 按 (channel,sender,saved_peer,random_id) 查重发,确保同一发件人向不同
// 订阅者子会话用相同 random_id 时不会互相误判为重复。
func (s *ChannelStore) duplicateMonoforumMessage(ctx context.Context, channelID, senderUserID int64, savedPeer domain.Peer, randomID int64) (domain.SendChannelMessageResult, bool, error) {
	row := s.db.QueryRow(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages
WHERE channel_id = $1 AND sender_user_id = $2 AND saved_peer_type = $3 AND saved_peer_id = $4 AND random_id = $5`,
		channelID, senderUserID, string(savedPeer.Type), savedPeer.ID, randomID)
	msg, err := scanChannelMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SendChannelMessageResult{}, false, nil
	}
	if err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	channel, err := getChannelByID(ctx, s.db, channelID)
	if err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	event, err := s.eventForChannelMessage(ctx, channelID, msg.ID)
	if err != nil {
		return domain.SendChannelMessageResult{}, false, err
	}
	if event.Message.ID != 0 {
		msg = event.Message
	}
	return domain.SendChannelMessageResult{Channel: channel, Message: msg, Event: event, Duplicate: true}, true, nil
}

// ListMonoforumDialogs 列出 monoforum 的订阅者子会话(每个 saved_peer 一条,取其 top 消息),
// 按 top 消息 id 倒序分页。走部分索引 channel_messages_monoforum_sublist_idx。
func (s *ChannelStore) ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error) {
	if filter.MonoforumID == 0 {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	channel, err := getChannelByID(ctx, s.db, filter.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
		}
		return domain.MonoforumDialogList{}, err
	}
	if !channel.Monoforum {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	args := []any{filter.MonoforumID}
	outerWhere := ""
	if filter.OffsetID > 0 {
		outerWhere = fmt.Sprintf(` WHERE top_id < $%d`, len(args)+1)
		args = append(args, filter.OffsetID)
	}
	// 按 saved_peer_id DISTINCT ON 直接命中部分索引 channel_messages_monoforum_sublist_idx
	// (channel_id, saved_peer_id, id DESC),避免对全频道私信做内存全排序;saved_peer_type 对 monoforum
	// 恒为 'user'(发送时强校验),取每组 top 行的值即可,与按 (type,id) 分组结果一致。
	q := `
SELECT saved_peer_type, saved_peer_id, top_id FROM (
    SELECT DISTINCT ON (saved_peer_id) saved_peer_type, saved_peer_id, id AS top_id
    FROM channel_messages
    WHERE channel_id = $1 AND saved_peer_id <> 0 AND NOT deleted
    ORDER BY saved_peer_id, id DESC
) t` + outerWhere + fmt.Sprintf(` ORDER BY top_id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return domain.MonoforumDialogList{}, fmt.Errorf("list monoforum dialogs: %w", err)
	}
	type subRef struct {
		peer  domain.Peer
		topID int
	}
	var refs []subRef
	var topIDs []int
	for rows.Next() {
		var spType string
		var spID int64
		var topID int
		if err := rows.Scan(&spType, &spID, &topID); err != nil {
			rows.Close()
			return domain.MonoforumDialogList{}, err
		}
		refs = append(refs, subRef{peer: domain.Peer{Type: domain.PeerType(spType), ID: spID}, topID: topID})
		topIDs = append(topIDs, topID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.MonoforumDialogList{}, err
	}
	rows.Close()

	msgByID := make(map[int]domain.ChannelMessage, len(topIDs))
	if len(topIDs) > 0 {
		mrows, err := s.db.Query(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE channel_id = $1 AND id = ANY($2::int[])`, filter.MonoforumID, topIDs)
		if err != nil {
			return domain.MonoforumDialogList{}, fmt.Errorf("load monoforum dialog top messages: %w", err)
		}
		for mrows.Next() {
			m, err := scanChannelMessage(mrows)
			if err != nil {
				mrows.Close()
				return domain.MonoforumDialogList{}, err
			}
			msgByID[m.ID] = m
		}
		if err := mrows.Err(); err != nil {
			mrows.Close()
			return domain.MonoforumDialogList{}, err
		}
		mrows.Close()
	}

	out := domain.MonoforumDialogList{MonoforumID: filter.MonoforumID, Channel: channel}
	for _, r := range refs {
		m := msgByID[r.topID]
		out.Dialogs = append(out.Dialogs, domain.MonoforumDialog{SavedPeer: r.peer, TopMessageID: r.topID, TopMessageDate: m.Date})
		if m.ID != 0 {
			out.Messages = append(out.Messages, m)
		}
	}
	if err := s.db.QueryRow(ctx, `SELECT count(DISTINCT saved_peer_id)::int FROM channel_messages WHERE channel_id = $1 AND saved_peer_id <> 0 AND NOT deleted`, filter.MonoforumID).Scan(&out.Count); err != nil {
		return domain.MonoforumDialogList{}, fmt.Errorf("count monoforum dialogs: %w", err)
	}
	return out, nil
}
