package channels

import (
	"context"
	"sort"
	"strings"

	"telesrv/internal/domain"
)

// BotProfileResolver is the domain-only view of bot metadata used by channel policy.
type BotProfileResolver interface {
	BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
}

type botProfileBatchResolver interface {
	BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

type activeChannelBotMemberLister interface {
	ListActiveChannelBotMembers(ctx context.Context, viewerUserID, channelID int64, offset, limit int) (domain.ChannelParticipantList, error)
}

type activeChannelBotMemberIDLister interface {
	ListActiveChannelBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error)
}

func (s *Service) getBotParticipants(ctx context.Context, userID, channelID int64, offset, limit int) (domain.ChannelParticipantList, error) {
	if lister, ok := s.channels.(activeChannelBotMemberLister); ok {
		return lister.ListActiveChannelBotMembers(ctx, userID, channelID, offset, limit)
	}
	channel, viewer, active, err := s.channels.ListActiveChannelMembers(ctx, userID, channelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if channel.ParticipantsHidden && !channelServiceMemberIsAdmin(viewer) {
		return domain.ChannelParticipantList{Channel: channel, Count: 0}, nil
	}
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	ids := make([]int64, 0, len(active))
	for _, member := range active {
		if member.UserID != 0 {
			ids = append(ids, member.UserID)
		}
	}
	profiles, err := s.botProfiles(ctx, ids)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	members := make([]domain.ChannelMember, 0, limit)
	count := 0
	for _, member := range active {
		if _, found := profiles[member.UserID]; !found {
			continue
		}
		if count >= offset && len(members) < limit {
			members = append(members, member)
		}
		count++
	}
	return domain.ChannelParticipantList{Channel: channel, Participants: members, Count: count}, nil
}

func (s *Service) botProfiles(ctx context.Context, ids []int64) (map[int64]domain.BotProfile, error) {
	if len(ids) == 0 || s.bots == nil {
		return nil, nil
	}
	if batch, ok := s.bots.(botProfileBatchResolver); ok {
		return batch.BotInfos(ctx, ids)
	}
	out := make(map[int64]domain.BotProfile)
	for _, id := range uniqueNonZero(ids) {
		profile, found, err := s.bots.BotInfo(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out[id] = profile
		}
	}
	return out, nil
}

func (s *Service) rejectBlockedBotInvites(ctx context.Context, userIDs []int64) error {
	if s.bots == nil {
		return nil
	}
	for _, id := range uniqueNonZero(userIDs) {
		profile, found, err := s.bots.BotInfo(ctx, id)
		if err != nil {
			return err
		}
		if found && profile.Nochats {
			return domain.ErrBotGroupsBlocked
		}
	}
	return nil
}

func (s *Service) skippedBotDeliveryUserIDs(ctx context.Context, req domain.SendChannelMessageRequest) ([]int64, error) {
	if s.bots == nil || req.ChannelID == 0 || req.UserID == 0 {
		return nil, nil
	}
	memberIDs, err := s.loadActiveBotMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		return nil, err
	}
	return s.skippedBotDeliveryUserIDsForIDs(ctx, req, memberIDs)
}

// ActiveBotMemberIDs returns active bot members for non-privacy-critical producers
// such as Bot API update queue fanout. Privacy delivery decisions use
// loadActiveBotMemberIDs directly to avoid stale-cache leaks.
func (s *Service) ActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if s == nil || s.channels == nil || viewerUserID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	key := activeBotMemberIDsCacheKey{viewerUserID: viewerUserID, channelID: channelID, limit: limit}
	if s.versions != nil {
		hash, err := s.channelBotMemberIDsHash(ctx, viewerUserID, channelID, key)
		if err != nil {
			return nil, err
		}
		if hash != 0 {
			return s.botMemberIDsCache.getOrLoadVersioned(ctx, key, hash, func() ([]int64, error) {
				return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
			})
		}
		return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
	}
	return s.botMemberIDsCache.getOrLoad(ctx, key, func() ([]int64, error) {
		return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
	})
}

func (s *Service) loadActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if s == nil || s.channels == nil || viewerUserID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	if lister, ok := s.channels.(activeChannelBotMemberIDLister); ok {
		return lister.ListActiveChannelBotMemberIDs(ctx, viewerUserID, channelID, limit)
	}
	if s.bots == nil {
		return nil, nil
	}
	memberIDs, err := s.channels.ListActiveChannelMemberIDs(ctx, viewerUserID, channelID, limit)
	if err != nil {
		return nil, err
	}
	profiles, err := s.botProfiles(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(profiles))
	for _, id := range uniqueNonZero(memberIDs) {
		if _, found := profiles[id]; found {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *Service) skippedBotDeliveryUserIDsForIDs(ctx context.Context, req domain.SendChannelMessageRequest, memberIDs []int64) ([]int64, error) {
	profiles, err := s.botProfiles(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	msg := domain.ChannelMessage{
		ChannelID:    req.ChannelID,
		SenderUserID: req.UserID,
		Body:         req.Message,
		ReplyTo:      req.ReplyTo,
		Action:       req.Action,
	}
	skip := make([]int64, 0)
	for _, id := range memberIDs {
		if id == req.UserID {
			continue
		}
		profile, found := profiles[id]
		if !found || profile.ChatHistory {
			continue
		}
		visible, err := s.botCanSeeChannelMessage(ctx, id, msg, req.MentionUserIDs)
		if err != nil {
			return nil, err
		}
		if !visible {
			skip = append(skip, id)
		}
	}
	return skip, nil
}

func (s *Service) filterBotChannelHistory(ctx context.Context, userID int64, history domain.ChannelHistory) domain.ChannelHistory {
	if s.bots == nil || userID == 0 || history.Channel.ID == 0 {
		return history
	}
	profile, found, err := s.bots.BotInfo(ctx, userID)
	if err != nil || !found || profile.ChatHistory {
		return history
	}
	filtered := history
	filtered.Messages = make([]domain.ChannelMessage, 0, len(history.Messages))
	for _, msg := range history.Messages {
		if visible, err := s.botCanSeeChannelMessage(ctx, userID, msg, nil); err == nil && visible {
			filtered.Messages = append(filtered.Messages, msg)
		}
	}
	filtered.Count = len(filtered.Messages)
	filtered.Users = nil
	filtered.Channels = nil
	return filtered
}

func (s *Service) filterBotChannelDifference(ctx context.Context, userID int64, diff domain.ChannelDifference) domain.ChannelDifference {
	if s.bots == nil || userID == 0 || diff.Channel.ID == 0 {
		return diff
	}
	profile, found, err := s.bots.BotInfo(ctx, userID)
	if err != nil || !found || profile.ChatHistory {
		return diff
	}
	filtered := diff
	filtered.NewMessages = nil
	filtered.OtherUpdates = nil
	filtered.Events = nil
	filtered.Users = nil
	filtered.Channels = nil
	if diff.TooLong {
		for _, msg := range diff.NewMessages {
			if visible, err := s.botCanSeeChannelMessage(ctx, userID, msg, nil); err == nil && visible {
				filtered.NewMessages = append(filtered.NewMessages, msg)
			}
		}
		return filtered
	}
	for _, event := range diff.Events {
		visibleEvent, ok := s.filterBotChannelEvent(ctx, userID, event)
		if !ok {
			continue
		}
		filtered.Events = append(filtered.Events, visibleEvent)
		switch visibleEvent.Type {
		case domain.ChannelUpdateNewMessage:
			filtered.NewMessages = append(filtered.NewMessages, visibleEvent.Message)
		default:
			filtered.OtherUpdates = append(filtered.OtherUpdates, visibleEvent)
		}
	}
	return filtered
}

func (s *Service) filterBotChannelEvent(ctx context.Context, botUserID int64, event domain.ChannelUpdateEvent) (domain.ChannelUpdateEvent, bool) {
	switch event.Type {
	case domain.ChannelUpdateNewMessage, domain.ChannelUpdateEditMessage:
		if event.Message.ID == 0 {
			return event, true
		}
		visible, err := s.botCanSeeChannelMessage(ctx, botUserID, event.Message, nil)
		if err != nil || !visible {
			return domain.ChannelUpdateEvent{}, false
		}
		return event, true
	case domain.ChannelUpdateDeleteMessages:
		// 删除事件只携带消息 id、不含任何内容,且在线推送路径(channels_updates 的
		// channelDeleteMessagesUpdates→enqueueChannelFanout)本就对全体成员无差别投递删除。
		// 若在此按可见性过滤,会因被删消息无法重取(GetChannelMessages 带 AND NOT deleted
		// 恒返空)而把整条 delete 事件丢弃——privacy bot 经 getChannelDifference 补差时将
		// 对所有删除失明(连它本可见消息的删除也收不到),客户端缓存残留"未删"态。故直接放行,
		// 与在线推送行为一致(删除 id 不泄漏内容)。
		return event, true
	case domain.ChannelUpdatePinnedMessages:
		if len(event.MessageIDs) == 0 {
			return event, true
		}
		ids := make([]int, 0, len(event.MessageIDs))
		for _, id := range event.MessageIDs {
			history, err := s.channels.GetChannelMessages(ctx, botUserID, event.ChannelID, []int{id})
			if err != nil || len(history.Messages) == 0 {
				continue
			}
			visible, err := s.botCanSeeChannelMessage(ctx, botUserID, history.Messages[0], nil)
			if err == nil && visible {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return domain.ChannelUpdateEvent{}, false
		}
		event.MessageIDs = ids
		return event, true
	default:
		return event, true
	}
}

func (s *Service) botCanSeeChannelMessage(ctx context.Context, botUserID int64, msg domain.ChannelMessage, mentionUserIDs []int64) (bool, error) {
	if botUserID == 0 {
		return true, nil
	}
	if msg.SenderUserID == botUserID {
		return true, nil
	}
	if msg.Mentioned || containsInt64(mentionUserIDs, botUserID) {
		return true, nil
	}
	if msg.Action != nil && containsInt64(msg.Action.UserIDs, botUserID) {
		return true, nil
	}
	if messageIsCommand(msg.Body) {
		return true, nil
	}
	if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
		history, err := s.channels.GetChannelMessages(ctx, botUserID, msg.ChannelID, []int{msg.ReplyTo.MessageID})
		if err != nil {
			return false, nil
		}
		for _, target := range history.Messages {
			if target.SenderUserID == botUserID {
				return true, nil
			}
		}
	}
	return false, nil
}

func messageIsCommand(message string) bool {
	message = strings.TrimSpace(message)
	return strings.HasPrefix(message, "/") && len(message) > 1
}

func containsInt64(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func channelServiceMemberIsAdmin(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
}

func mergeSkippedUserIDs(a, b []int64) []int64 {
	out := append(append([]int64(nil), a...), b...)
	out = uniqueNonZero(out)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
