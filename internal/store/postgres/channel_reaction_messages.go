package postgres

import (
	"context"
	"fmt"
	"strings"
	"telesrv/internal/domain"
)

func (s *ChannelStore) SetChannelMessageReactions(ctx context.Context, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.Reactions) > domain.MaxChannelMessageReactionsPerUser {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	for _, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
		}
	}
	req.Reactions = domain.TrimMessageReactionsToUserMax(req.Reactions, req.ReactionsPerUserMax)
	if req.Date <= 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("set channel message reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("begin set channel message reactions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if len(req.Reactions) > 0 {
		selfBoostsApplied := 0
		if channel.Megagroup {
			selfBoostsApplied, err = countActiveUserBoostsForPeer(ctx, tx, req.UserID, domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}, req.Date)
			if err != nil {
				return domain.ChannelMessageReactionsResult{}, err
			}
		}
		if domain.ChannelBannedRightsBlockReactions(channel, member, selfBoostsApplied) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelWriteForbidden
		}
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	// 仅新增/替换受策略约束；空向量是撤销，策略收紧后也必须允许撤销存量 reaction。
	for _, reaction := range req.Reactions {
		if !channel.ReactionPolicy.AllowsReaction(reaction) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionInvalid
		}
	}
	if len(req.Reactions) > 0 {
		// READ COMMITTED 下并发新增不同新种类互不可见、会同时通过去重闸门，
		// 事务级 advisory lock 按 (channel, message) 串行化带闸门的写入（独立于
		// lockUsersForUpdate 的单参数键空间）；撤销不经闸门，无需上锁。
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashint8($1::bigint), $2::int)`, req.ChannelID, req.MessageID); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("advisory lock channel message reactions: %w", err)
		}
		// 官方 REACTIONS_TOO_MANY 只挡「引入消息上尚不存在的新种类」：存量已超限
		//（管理员调低 reactions_limit / 部署前数据）时，重发自己的 reaction 或给
		// 已有种类投票必须放行，否则客户端点击合法 chip 也会收到 400。
		existing := make(map[string]struct{})
		final := make(map[string]struct{})
		rows, err := tx.Query(ctx, `
SELECT reaction_type, reaction_value, BOOL_OR(reacted_user_id <> $3)
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2
GROUP BY reaction_type, reaction_value`, req.ChannelID, req.MessageID, req.UserID)
		if err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("list channel message reaction values: %w", err)
		}
		for rows.Next() {
			var reactionType, value string
			var byOthers bool
			if err := rows.Scan(&reactionType, &value, &byOthers); err != nil {
				rows.Close()
				return domain.ChannelMessageReactionsResult{}, err
			}
			key := string(reactionType) + "\x00" + value
			existing[key] = struct{}{}
			if byOthers {
				final[key] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.ChannelMessageReactionsResult{}, err
		}
		rows.Close()
		newKind := false
		for _, reaction := range req.Reactions {
			key := reaction.Key()
			if _, ok := existing[key]; !ok {
				newKind = true
			}
			final[key] = struct{}{}
		}
		if newKind && len(final) > channel.ReactionPolicy.UniqueReactionsLimit() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionsTooMany
		}
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3`, req.ChannelID, req.MessageID, req.UserID); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete channel message reactions: %w", err)
	}
	// 广播频道 reaction 匿名（官方语义），作者不收 unread 角标，不写 unread 簿记。
	unreadEligible := !channel.Broadcast || channel.Megagroup
	for i, reaction := range req.Reactions {
		if _, err := tx.Exec(ctx, `
INSERT INTO channel_message_reactions (
    channel_id, message_id, reacted_user_id, sender_user_id, reaction_type, reaction_value,
    big, unread, chosen_order, reaction_date
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			req.ChannelID, req.MessageID, req.UserID, msg.SenderUserID, string(reaction.Type), reaction.Value(), req.Big, unreadEligible && msg.SenderUserID != 0 && msg.SenderUserID != req.UserID, i+1, req.Date); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("insert channel message reaction: %w", err)
		}
		if req.AddToRecent {
			if _, err := tx.Exec(ctx, `
INSERT INTO user_recent_reactions (user_id, reaction_type, reaction_value, reaction_date)
VALUES ($1,$2,$3,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
				req.UserID, string(reaction.Type), reaction.Value(), req.Date); err != nil {
				return domain.ChannelMessageReactionsResult{}, fmt.Errorf("upsert recent message reaction: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO user_top_reactions (user_id, reaction_type, reaction_value, reaction_count, reaction_date)
VALUES ($1,$2,$3,1,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_count = user_top_reactions.reaction_count + 1, reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
			req.UserID, string(reaction.Type), reaction.Value(), req.Date); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("upsert top message reaction: %w", err)
		}
	}
	if unreadEligible {
		if err := refreshChannelUnreadReactionsCountTx(ctx, tx, msg.SenderUserID, req.ChannelID); err != nil {
			return domain.ChannelMessageReactionsResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("commit set channel message reactions: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{msg}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	msg = messages[0]
	reactions := emptyChannelMessageReactions(channel)
	if msg.Reactions != nil {
		reactions = *msg.Reactions
	} else {
		msg.Reactions = &reactions
	}
	// sendReaction 实时推送走在线 viewer scope（rpc 层封顶），不预热全量成员列表。
	return domain.ChannelMessageReactionsResult{
		Channel:    channel,
		Message:    msg,
		Messages:   []domain.ChannelMessage{msg},
		Reactions:  reactions,
		Recipients: []int64{req.UserID},
	}, nil
}

// AddChannelMessagePaidReaction 为一条广播频道消息增投付费 reaction 星数（累计），返回聚合
// 状态供 rpc 投影与扇出。扣费在 rpc 层经 Stars 账本 Debit 完成，本方法只负责累计与聚合。
func (s *ChannelStore) AddChannelMessagePaidReaction(ctx context.Context, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if req.Stars <= 0 || req.Stars > domain.MaxPaidReactionStarsPerRequest {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if req.Date <= 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("add channel paid reaction: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("begin add channel paid reaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	// 付费 reaction 仅用于广播频道帖子（官方语义）。
	if !channel.Broadcast || channel.Megagroup {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrReactionInvalid
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrMessageIDInvalid
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_message_paid_reactions (channel_id, message_id, reactor_user_id, stars, anonymous, reaction_date)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (channel_id, message_id, reactor_user_id)
DO UPDATE SET stars = channel_message_paid_reactions.stars + EXCLUDED.stars,
             anonymous = EXCLUDED.anonymous,
             reaction_date = EXCLUDED.reaction_date`,
		req.ChannelID, req.MessageID, req.UserID, req.Stars, req.Anonymous, req.Date); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("upsert channel paid reaction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("commit add channel paid reaction: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{msg}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	msg = messages[0]
	paid, err := s.aggregateChannelPaidReactions(ctx, req.ChannelID, req.MessageID, req.UserID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil || len(recipients) == 0 {
		recipients = []int64{req.UserID}
	}
	return domain.ChannelMessagePaidReactionResult{
		Channel:    channel,
		Message:    msg,
		Paid:       paid,
		Recipients: recipients,
	}, nil
}

// aggregateChannelPaidReactions 汇总一条消息的付费 reaction：总星数 + viewer 自身 + top reactors。
func (s *ChannelStore) aggregateChannelPaidReactions(ctx context.Context, channelID int64, messageID int, viewerUserID int64) (domain.ChannelMessagePaidReactions, error) {
	rows, err := s.db.Query(ctx, `
SELECT reactor_user_id, stars, anonymous
FROM channel_message_paid_reactions
WHERE channel_id = $1 AND message_id = $2
ORDER BY stars DESC, reactor_user_id ASC`, channelID, messageID)
	if err != nil {
		return domain.ChannelMessagePaidReactions{}, fmt.Errorf("aggregate channel paid reactions: %w", err)
	}
	defer rows.Close()
	var out domain.ChannelMessagePaidReactions
	var myReactor domain.PaidReactor
	myInTop := false
	for rows.Next() {
		var r domain.PaidReactor
		if err := rows.Scan(&r.UserID, &r.Stars, &r.Anonymous); err != nil {
			return domain.ChannelMessagePaidReactions{}, err
		}
		out.TotalStars += r.Stars
		r.My = r.UserID == viewerUserID
		if r.My {
			out.MyStars = r.Stars
			out.MyAnonymous = r.Anonymous
			myReactor = r
		}
		if len(out.TopReactors) < domain.MaxPaidReactionTopReactors {
			out.TopReactors = append(out.TopReactors, r)
			if r.My {
				myInTop = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessagePaidReactions{}, err
	}
	// viewer 自身始终出现在 top reactors（官方：你的条目总在列表里，带 My 标志）。
	if out.MyStars > 0 && !myInTop {
		out.TopReactors = append(out.TopReactors, myReactor)
	}
	return out, nil
}

func (s *ChannelStore) DeleteChannelParticipantReaction(ctx context.Context, req domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID || req.ParticipantUserID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete channel participant reaction: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("begin delete channel participant reaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelAdminRequired
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3`,
		req.ChannelID, req.MessageID, req.ParticipantUserID); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete participant reaction: %w", err)
	}
	if err := refreshChannelUnreadReactionsCountTx(ctx, tx, msg.SenderUserID, req.ChannelID); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("commit delete participant reaction: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{msg}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	msg = messages[0]
	reactions := emptyChannelMessageReactions(channel)
	if msg.Reactions != nil {
		reactions = *msg.Reactions
	} else {
		msg.Reactions = &reactions
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil {
		recipients = []int64{req.UserID}
	}
	return domain.ChannelMessageReactionsResult{
		Channel:    channel,
		Message:    msg,
		Messages:   []domain.ChannelMessage{msg},
		Reactions:  reactions,
		Recipients: recipients,
	}, nil
}

func (s *ChannelStore) DeleteChannelParticipantReactions(ctx context.Context, req domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ParticipantUserID == 0 {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxDeleteParticipantReactionsBatch {
		req.Limit = domain.MaxDeleteParticipantReactionsBatch
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("delete channel participant reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("begin delete channel participant reactions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelAdminRequired
	}
	rows, err := tx.Query(ctx, `
SELECT message_id, MAX(sender_user_id)
FROM channel_message_reactions
WHERE channel_id = $1 AND reacted_user_id = $2
GROUP BY message_id
ORDER BY MAX(reaction_date) DESC, message_id DESC
LIMIT $3`, req.ChannelID, req.ParticipantUserID, req.Limit)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("list participant reaction messages: %w", err)
	}
	ids := make([]int, 0, req.Limit)
	owners := make(map[int64]struct{})
	for rows.Next() {
		var msgID int
		var senderUserID int64
		if err := rows.Scan(&msgID, &senderUserID); err != nil {
			rows.Close()
			return domain.DeleteChannelParticipantReactionsResult{}, err
		}
		ids = append(ids, msgID)
		if senderUserID != 0 {
			owners[senderUserID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.DeleteChannelParticipantReactionsResult{}, err
	}
	rows.Close()
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND reacted_user_id = $2 AND message_id = ANY($3::int[])`,
			req.ChannelID, req.ParticipantUserID, int32s(ids)); err != nil {
			return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("delete participant reactions: %w", err)
		}
		for ownerID := range owners {
			if err := refreshChannelUnreadReactionsCountTx(ctx, tx, ownerID, req.ChannelID); err != nil {
				return domain.DeleteChannelParticipantReactionsResult{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("commit delete participant reactions: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{}
	if len(ids) > 0 {
		res, err := s.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
			UserID:    req.UserID,
			ChannelID: req.ChannelID,
			IDs:       ids,
		})
		if err != nil {
			return domain.DeleteChannelParticipantReactionsResult{}, err
		}
		messages = res.Messages
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil {
		recipients = []int64{req.UserID}
	}
	return domain.DeleteChannelParticipantReactionsResult{
		Channel:    channel,
		Messages:   messages,
		Recipients: recipients,
		Deleted:    len(ids),
	}, nil
}

func (s *ChannelStore) GetChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if len(req.IDs) == 0 {
		return domain.ChannelMessageReactionsResult{Channel: channel}, nil
	}
	id32, _, err := validUniqueChannelMessageIDs(req.IDs)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	args := []any{req.ChannelID, id32}
	where := "channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted"
	if member.AvailableMinID > 0 {
		args = append(args, member.AvailableMinID)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id DESC`, args...)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("get channel message reactions messages: %w", err)
	}
	defer rows.Close()
	messages := make([]domain.ChannelMessage, 0, len(req.IDs))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelMessageReactionsResult{}, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	res := domain.ChannelMessageReactionsResult{Channel: channel, Messages: messages}
	if len(messages) == 1 {
		res.Message = messages[0]
		res.Reactions = emptyChannelMessageReactions(channel)
		if messages[0].Reactions != nil {
			res.Reactions = *messages[0].Reactions
		}
	}
	return res, nil
}

func (s *ChannelStore) ListChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxChannelMessageReactionListLimit {
		req.Limit = domain.MaxChannelMessageReactionListLimit
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	if channel.Broadcast && !channel.Megagroup {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelRightForbidden
	}
	msg, err := s.getChannelMessage(ctx, s.db, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	if msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsList{}, domain.ErrMessageIDInvalid
	}
	baseWhere := []string{"channel_id = $1", "message_id = $2"}
	baseArgs := []any{req.ChannelID, req.MessageID}
	if req.Reaction != nil {
		if !req.Reaction.Valid() {
			return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
		}
		baseArgs = append(baseArgs, string(req.Reaction.Type), req.Reaction.Value())
		baseWhere = append(baseWhere, fmt.Sprintf("reaction_type = $%d AND reaction_value = $%d", len(baseArgs)-1, len(baseArgs)))
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM channel_message_reactions WHERE `+strings.Join(baseWhere, " AND "), baseArgs...).Scan(&count); err != nil {
		return domain.ChannelMessageReactionsList{}, fmt.Errorf("count channel message reactions: %w", err)
	}
	where := append([]string(nil), baseWhere...)
	args := append([]any(nil), baseArgs...)
	if req.Offset != "" {
		cursor, ok := parseChannelReactionOffset(req.Offset)
		if !ok {
			return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
		}
		if cursor.legacyValue {
			args = append(args, cursor.date, cursor.userID, cursor.value)
			n := len(args)
			where = append(where, fmt.Sprintf("(reaction_date < $%d OR (reaction_date = $%d AND (reacted_user_id < $%d OR (reacted_user_id = $%d AND reaction_value > $%d))))", n-2, n-2, n-1, n-1, n))
		} else {
			args = append(args, cursor.date, cursor.userID, string(cursor.reactionType), cursor.value)
			n := len(args)
			where = append(where, fmt.Sprintf("(reaction_date < $%d OR (reaction_date = $%d AND (reacted_user_id < $%d OR (reacted_user_id = $%d AND (reaction_type > $%d OR (reaction_type = $%d AND reaction_value > $%d))))))", n-3, n-3, n-2, n-2, n-1, n-1, n))
		}
	}
	args = append(args, req.Limit+1)
	rows, err := s.db.Query(ctx, `
SELECT channel_id, message_id, reacted_user_id, sender_user_id, reaction_type, reaction_value,
       big, unread, chosen_order, reaction_date
FROM channel_message_reactions
WHERE `+strings.Join(where, " AND ")+`
ORDER BY reaction_date DESC, reacted_user_id DESC, reaction_type ASC, reaction_value ASC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, fmt.Errorf("list channel message reactions: %w", err)
	}
	defer rows.Close()
	reactions := make([]domain.ChannelMessagePeerReaction, 0, req.Limit+1)
	for rows.Next() {
		row, err := scanChannelMessagePeerReaction(rows, req.UserID)
		if err != nil {
			return domain.ChannelMessageReactionsList{}, err
		}
		reactions = append(reactions, row)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	next := ""
	if len(reactions) > req.Limit {
		reactions = reactions[:req.Limit]
		next = channelReactionOffset(reactions[len(reactions)-1])
	}
	return domain.ChannelMessageReactionsList{
		Channel:    channel,
		Message:    msg,
		Count:      count,
		Reactions:  reactions,
		NextOffset: next,
	}, nil
}
