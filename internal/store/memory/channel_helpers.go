package memory

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sort"
	"strings"
	"telesrv/internal/domain"
)

func (s *ChannelStore) SaveChannelDefaultSendAs(_ context.Context, req domain.SaveChannelDefaultSendAsRequest) (domain.ChannelView, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	if req.SendAs != nil && req.SendAs.Type != domain.PeerTypeUser && req.SendAs.Type != domain.PeerTypeChannel {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	dialog := s.dialogForUserLocked(req.UserID, channel)
	if req.SendAs != nil {
		p := *req.SendAs
		dialog.DefaultSendAs = &p
	} else {
		dialog.DefaultSendAs = nil
	}
	if s.dialogs[req.UserID] == nil {
		s.dialogs[req.UserID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[req.UserID][req.ChannelID] = dialog
	member := s.members[req.ChannelID][req.UserID]
	return domain.ChannelView{Channel: cloneChannel(channel), Self: member, Dialog: dialog}, nil
}

func (s *ChannelStore) DeleteChannel(_ context.Context, req domain.DeleteChannelRequest) (domain.DeleteChannelResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.DeleteChannelResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if member.Role != domain.ChannelRoleCreator {
		return domain.DeleteChannelResult{}, domain.ErrChannelAdminRequired
	}
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	channel.Deleted = true
	channel.Username = ""
	s.channels[req.ChannelID] = channel
	// 连带软删关联 monoforum(频道私信容器),与 PG store 等价:仅当 counterpart 是 monoforum 时级联,
	// 防止删 mono 反向误删真实母频道。不级联会留下指向已删父频道的孤儿 mono。
	var linkedMono *domain.Channel
	if channel.LinkedMonoforumID != 0 {
		if mono, ok := s.channels[channel.LinkedMonoforumID]; ok && mono.Monoforum && !mono.Deleted {
			mono.Deleted = true
			mono.Username = ""
			s.channels[mono.ID] = mono
			clone := cloneChannel(mono)
			linkedMono = &clone
		}
	}
	return domain.DeleteChannelResult{Channel: channel, Recipients: recipients, LinkedMonoforum: linkedMono}, nil
}

func (s *ChannelStore) SearchPublicChannels(_ context.Context, viewerUserID int64, query string, limit int) (domain.PublicChannelSearchResult, error) {
	if viewerUserID == 0 {
		return domain.PublicChannelSearchResult{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxPublicChannelSearchLimit {
		limit = domain.MaxPublicChannelSearchLimit
	}
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@")))
	if query == "" {
		return domain.PublicChannelSearchResult{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type item struct {
		channel domain.Channel
		rank    int
	}
	items := make([]item, 0, limit)
	for channelID, channel := range s.channels {
		rank, ok := publicChannelSearchRank(channel, query)
		if !ok {
			continue
		}
		member, joined := s.members[channelID][viewerUserID]
		joined = joined && member.Status == domain.ChannelMemberActive
		if joined {
			continue
		}
		items = append(items, item{
			channel: cloneChannel(channel),
			rank:    rank,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		if items[i].channel.ParticipantsCount != items[j].channel.ParticipantsCount {
			return items[i].channel.ParticipantsCount > items[j].channel.ParticipantsCount
		}
		if items[i].channel.Date != items[j].channel.Date {
			return items[i].channel.Date > items[j].channel.Date
		}
		return items[i].channel.ID > items[j].channel.ID
	})

	out := domain.PublicChannelSearchResult{}
	for _, item := range items {
		if len(out.Results) >= limit {
			break
		}
		out.Results = append(out.Results, item.channel)
	}
	return out, nil
}

func (s *ChannelStore) inviteByChannelHashLocked(channelID int64, hash string) (domain.ChannelInvite, error) {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return domain.ChannelInvite{}, domain.ErrInviteHashEmpty
	}
	invite, ok := s.invites[hash]
	if !ok || invite.ChannelID != channelID {
		return domain.ChannelInvite{}, domain.ErrInviteHashInvalid
	}
	return invite, nil
}

func (s *ChannelStore) inviteByIDLocked(channelID, inviteID int64) (domain.ChannelInvite, error) {
	for _, invite := range s.invites {
		if invite.ChannelID == channelID && invite.InviteID == inviteID {
			return invite, nil
		}
	}
	return domain.ChannelInvite{}, domain.ErrInviteHashInvalid
}

func (s *ChannelStore) SearchPublicPosts(_ context.Context, viewerUserID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error) {
	query := strings.ToLower(strings.TrimSpace(req.Query))
	hashtag := strings.ToLower(strings.TrimSpace(req.Hashtag))
	if (query == "") == (hashtag == "") {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxChannelSearchPostsLimit {
		req.Limit = domain.MaxChannelSearchPostsLimit
	}
	type hit struct {
		channel domain.Channel
		message domain.ChannelMessage
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hits := make([]hit, 0, req.Limit+1)
	for channelID, channel := range s.channels {
		if channel.Deleted || strings.TrimSpace(channel.Username) == "" {
			continue
		}
		for _, msg := range s.messages[channelID] {
			if msg.Deleted || strings.TrimSpace(msg.Body) == "" {
				continue
			}
			if !channelSearchPostAfterCursor(msg, req) {
				continue
			}
			body := strings.ToLower(msg.Body)
			if query != "" && !strings.Contains(body, query) {
				continue
			}
			if hashtag != "" && !strings.Contains(body, "#"+hashtag) {
				continue
			}
			hits = append(hits, hit{channel: channel, message: cloneChannelMessage(msg)})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i].message, hits[j].message
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		if a.ChannelID != b.ChannelID {
			return a.ChannelID > b.ChannelID
		}
		return a.ID > b.ID
	})
	out := domain.ChannelHistory{Count: len(hits)}
	if out.Count > req.Limit {
		out.Count = req.Limit + 1
		hits = hits[:req.Limit]
	}
	channelSeen := make(map[int64]struct{}, len(hits))
	for _, h := range hits {
		out.Messages = append(out.Messages, h.message)
		if _, ok := channelSeen[h.channel.ID]; ok {
			continue
		}
		channelSeen[h.channel.ID] = struct{}{}
		out.Channels = append(out.Channels, h.channel)
	}
	s.populateChannelMessagesReactionsLocked(viewerUserID, out.Channels, out.Messages)
	return out, nil
}

func channelSearchPostAfterCursor(msg domain.ChannelMessage, req domain.ChannelSearchPostsRequest) bool {
	if req.OffsetRate <= 0 && req.OffsetChannelID <= 0 && req.OffsetID <= 0 {
		return true
	}
	if req.OffsetRate > 0 {
		if msg.Date < req.OffsetRate {
			return true
		}
		if msg.Date > req.OffsetRate {
			return false
		}
	}
	if req.OffsetChannelID > 0 {
		if msg.ChannelID < req.OffsetChannelID {
			return true
		}
		if msg.ChannelID > req.OffsetChannelID {
			return false
		}
	}
	if req.OffsetID > 0 {
		return msg.ID < req.OffsetID
	}
	return false
}

func channelGlobalSearchAfterCursor(msg domain.ChannelMessage, req domain.ChannelGlobalSearchRequest) bool {
	if req.OffsetRate <= 0 && req.OffsetChannelID <= 0 && req.OffsetID <= 0 {
		return true
	}
	if req.OffsetRate > 0 {
		if msg.Date < req.OffsetRate {
			return true
		}
		if msg.Date > req.OffsetRate {
			return false
		}
	}
	if req.OffsetChannelID > 0 {
		if msg.ChannelID < req.OffsetChannelID {
			return true
		}
		if msg.ChannelID > req.OffsetChannelID {
			return false
		}
	}
	if req.OffsetID > 0 {
		return msg.ID < req.OffsetID
	}
	return false
}

func (s *ChannelStore) ListActiveChannelIDsForUser(_ context.Context, userID, afterChannelID int64, limit int) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if userID == 0 || afterChannelID < 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	out := make([]int64, 0, limit)
	for channelID, members := range s.members {
		if channelID <= afterChannelID {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted {
			continue
		}
		member, ok := members[userID]
		if !ok || member.Status != domain.ChannelMemberActive {
			continue
		}
		out = append(out, channelID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *ChannelStore) ListDirtyActiveChannelsForUser(_ context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if userID == 0 || sinceDate <= 0 || afterChannelID < 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxChannelDifferenceLimit {
		limit = domain.MaxChannelDifferenceLimit
	}
	out := make([]domain.DirtyChannel, 0, limit)
	for channelID, members := range s.members {
		if channelID <= afterChannelID {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted {
			continue
		}
		member, ok := members[userID]
		if !ok || member.Status != domain.ChannelMemberActive {
			continue
		}
		checkpoint := s.channelUpdateCheckpointLocked(channelID, channel)
		if checkpoint.LatestEventDate > sinceDate {
			out = append(out, domain.DirtyChannel{ChannelID: channelID, Pts: channel.Pts})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *ChannelStore) nextChannelIDLocked() int64 {
	id := s.nextID
	s.nextID++
	return id
}

func (s *ChannelStore) nextAccessHashLocked() int64 {
	hash := s.nextHash
	s.nextHash += 17
	return hash
}

func (s *ChannelStore) channelForViewerLocked(userID, channelID int64) (domain.Channel, domain.ChannelMember, bool, error) {
	channel, member, err := s.channelAndMemberLocked(userID, channelID)
	if err == nil {
		return channel, member, false, nil
	}
	if !errors.Is(err, domain.ErrChannelPrivate) {
		return domain.Channel{}, domain.ChannelMember{}, false, err
	}
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ChannelMember{}, false, domain.ErrChannelInvalid
	}
	existing, found := s.members[channelID][userID]
	if found && (existing.Status == domain.ChannelMemberBanned || existing.Status == domain.ChannelMemberKicked || existing.BannedRights.ViewMessages) {
		return domain.Channel{}, domain.ChannelMember{}, false, domain.ErrChannelUserBanned
	}
	if guest, ok, guestErr := s.linkedDiscussionGuestLocked(userID, channel); guestErr != nil {
		return domain.Channel{}, domain.ChannelMember{}, false, guestErr
	} else if ok {
		return channel, guest, true, nil
	}
	if channel.Monoforum && channel.LinkedMonoforumID != 0 {
		parentMember, ok := s.members[channel.LinkedMonoforumID][userID]
		if ok && parentMember.Status == domain.ChannelMemberActive && isChannelAdmin(parentMember) {
			return channel, syntheticMonoforumAdminMember(channel, parentMember), true, nil
		}
	}
	if !publicPreviewableChannel(channel) {
		return domain.Channel{}, domain.ChannelMember{}, false, domain.ErrChannelPrivate
	}
	return channel, publicPreviewMember(channel, userID, existing, found), true, nil
}

func (s *ChannelStore) dialogForUserLocked(userID int64, channel domain.Channel) domain.ChannelDialog {
	return s.dialogForMemberLocked(userID, channel, s.members[channel.ID][userID])
}

func (s *ChannelStore) dialogForMemberLocked(userID int64, channel domain.Channel, member domain.ChannelMember) domain.ChannelDialog {
	dialog := s.dialogs[userID][channel.ID]
	dialog.UserID = userID
	dialog.ChannelID = channel.ID
	dialog.TopMessageID = s.visibleTopMessageIDForMemberLocked(channel, member)
	// TopMessageDate 必须从可见 top 消息派生(不能继承空缓存的 0),否则会话排序/分页与预览
	// dialog 的日期全错。与 postgres GetChannelDialogs 用 getChannelMessage 设 date 对齐。
	dialog.TopMessageDate = 0
	if dialog.TopMessageID > 0 {
		if top, ok := s.findMessageLocked(channel.ID, dialog.TopMessageID); ok {
			dialog.TopMessageDate = top.Date
		}
	}
	if member.ReadInboxMaxID > dialog.ReadInboxMaxID {
		dialog.ReadInboxMaxID = member.ReadInboxMaxID
	}
	if member.ReadOutboxMaxID > dialog.ReadOutboxMaxID {
		dialog.ReadOutboxMaxID = member.ReadOutboxMaxID
	}
	// 公共已读水位派生：即使实时 fanout 被截断，read_outbox 真值仍前进。
	if derived := s.readMarks[channel.ID].forSender(userID); derived > dialog.ReadOutboxMaxID {
		dialog.ReadOutboxMaxID = derived
	}
	if top := channel.TopMessageID; dialog.ReadOutboxMaxID > top {
		dialog.ReadOutboxMaxID = top
	}
	dialog.UnreadCount = s.channelUnreadCountLocked(userID, channel.ID, dialog.ReadInboxMaxID, dialog.TopMessageID)
	dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(userID, channel.ID, 0)
	dialog.UnreadReactions = s.countChannelUnreadReactionsLocked(userID, channel.ID, 0)
	return dialog
}

func channelReplyBelongsToRoot(msg domain.ChannelMessage, channelID int64, rootID int) bool {
	if msg.ReplyTo == nil || rootID <= 0 {
		return false
	}
	if msg.ReplyTo.Peer.ID != 0 && msg.ReplyTo.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}) {
		return false
	}
	return msg.ReplyTo.TopMessageID == rootID || (msg.ReplyTo.TopMessageID == 0 && msg.ReplyTo.MessageID == rootID)
}

func (s *ChannelStore) resolveChannelReplyLocked(req domain.SendChannelMessageRequest, member domain.ChannelMember, channel domain.Channel, selfBoostsApplied int) (*domain.MessageReply, error) {
	if req.ReplyTo == nil {
		return nil, nil
	}
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, err
	}
	peer := req.ReplyTo.Peer
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	if peer.ID == 0 {
		peer = channelPeer
	}
	if peer != channelPeer {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if req.ReplyTo.MessageID == 0 {
		if req.ReplyTo.TopMessageID <= 0 || !channel.Forum {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		topic, ok := s.topics[req.ChannelID][req.ReplyTo.TopMessageID]
		if !ok || topic.Hidden {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		if topic.Closed && !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
			return nil, domain.ErrChannelWriteForbidden
		}
		reply := cloneMessageReply(req.ReplyTo)
		reply.MessageID = 0
		reply.Peer = channelPeer
		reply.TopMessageID = topic.TopicID
		reply.ForumTopic = true
		return reply, nil
	}
	target, ok := s.findMessageLocked(req.ChannelID, req.ReplyTo.MessageID)
	if !ok || target.Deleted || target.ID <= member.AvailableMinID {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	reply := cloneMessageReply(req.ReplyTo)
	reply.MessageID = target.ID
	reply.Peer = channelPeer
	reply.TopMessageID = target.ID
	if target.ReplyTo != nil && target.ReplyTo.TopMessageID > 0 {
		reply.TopMessageID = target.ReplyTo.TopMessageID
	}
	if req.ReplyTo.TopMessageID > 0 && req.ReplyTo.TopMessageID != reply.TopMessageID {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if channel.Forum && reply.TopMessageID > 0 {
		if topic, ok := s.topics[req.ChannelID][reply.TopMessageID]; ok && !topic.Hidden {
			if topic.Closed && !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
				return nil, domain.ErrChannelWriteForbidden
			}
			reply.ForumTopic = true
		}
	}
	return reply, nil
}

func inactiveChannelDate(dialog domain.Dialog, channel domain.Channel, member domain.ChannelMember) int {
	if dialog.TopMessageDate > 0 {
		return dialog.TopMessageDate
	}
	date := channel.Date
	if member.JoinedAt > date {
		date = member.JoinedAt
	}
	return date
}

func recommendableChannel(channel domain.Channel) bool {
	return !channel.Deleted &&
		channel.Broadcast &&
		!channel.Megagroup &&
		strings.TrimSpace(channel.Username) != ""
}

func publicSearchableChannel(channel domain.Channel) bool {
	return !channel.Deleted &&
		(channel.Broadcast || channel.Megagroup) &&
		strings.TrimSpace(channel.Username) != ""
}

func channelRoleOrder(role domain.ChannelMemberRole) int {
	switch role {
	case domain.ChannelRoleCreator:
		return 0
	case domain.ChannelRoleAdmin:
		return 1
	default:
		return 2
	}
}

func canChangeChannelInfo(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.ChangeInfo)
}

func boolPtr(v bool) *bool {
	return &v
}

func channelInitialAvailableMinID(channel domain.Channel) int {
	if channel.PreHistoryHidden {
		return channel.TopMessageID
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func adminRightsSubset(want, have domain.ChannelAdminRights) bool {
	return (!want.ChangeInfo || have.ChangeInfo) &&
		(!want.PostMessages || have.PostMessages) &&
		(!want.EditMessages || have.EditMessages) &&
		(!want.DeleteMessages || have.DeleteMessages) &&
		(!want.PostStories || have.PostStories) &&
		(!want.EditStories || have.EditStories) &&
		(!want.DeleteStories || have.DeleteStories) &&
		(!want.BanUsers || have.BanUsers) &&
		(!want.InviteUsers || have.InviteUsers) &&
		(!want.PinMessages || have.PinMessages) &&
		(!want.AddAdmins || have.AddAdmins) &&
		(!want.ManageCall || have.ManageCall) &&
		(!want.Anonymous || have.Anonymous) &&
		(!want.ManageRanks || have.ManageRanks) &&
		(!want.ManageDirectMessages || have.ManageDirectMessages)
}

// checkEditMemberRank validates a rank-only (member tag) edit: creator edits
// anyone but no one else edits the creator; admins always edit their own tag,
// and with ManageRanks edit plain members plus admins they promoted; plain
// members edit only their own tag and only while neither the channel default
// nor their personal banned rights set edit_rank. Member tags exist only in
// megagroups: broadcast participants must keep an empty rank so the admins
// participant filter stays a pure admin list there.
func checkEditMemberRank(channel domain.Channel, actor, target domain.ChannelMember) error {
	if !channel.Megagroup {
		return domain.ErrMegagroupIDInvalid
	}
	if actor.UserID == target.UserID {
		if actor.Role == domain.ChannelRoleCreator || actor.Role == domain.ChannelRoleAdmin {
			return nil
		}
		if channel.DefaultBannedRights.EditRank || actor.BannedRights.EditRank {
			return domain.ErrChannelRightForbidden
		}
		return nil
	}
	if target.Role == domain.ChannelRoleCreator {
		return domain.ErrChannelUserCreator
	}
	if actor.Role == domain.ChannelRoleCreator {
		return nil
	}
	if actor.Role != domain.ChannelRoleAdmin || !actor.AdminRights.ManageRanks {
		return domain.ErrChannelAdminRequired
	}
	if target.Role == domain.ChannelRoleAdmin && target.InviterUserID != actor.UserID {
		return domain.ErrChannelRightForbidden
	}
	return nil
}

func adminLogBanType(previous, next domain.ChannelMember) domain.ChannelAdminLogEventType {
	if next.Status == domain.ChannelMemberKicked || next.BannedRights.ViewMessages {
		return domain.ChannelAdminLogParticipantKick
	}
	if previous.Status == domain.ChannelMemberKicked || previous.BannedRights.ViewMessages {
		return domain.ChannelAdminLogParticipantUnkick
	}
	if !zeroChannelBannedRights(next.BannedRights) {
		return domain.ChannelAdminLogParticipantBan
	}
	return domain.ChannelAdminLogParticipantUnban
}

func adminLogSearchText(event domain.ChannelAdminLogEvent) string {
	parts := []string{
		event.Query,
		event.PrevString,
		event.NewString,
	}
	for _, msg := range []*domain.ChannelMessage{event.Message, event.PrevMessage, event.NewMessage} {
		if msg != nil {
			parts = append(parts, msg.Body)
		}
	}
	return strings.ToLower(strings.TrimSpace(strings.Join(parts, " ")))
}

func int64Set(items []int64) map[int64]struct{} {
	if len(items) == 0 {
		return nil
	}
	out := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item != 0 {
			out[item] = struct{}{}
		}
	}
	return out
}

func (s *ChannelStore) refreshChannelCountsLocked(channelID int64) {
	channel := s.channels[channelID]
	var participants, admins, kicked, banned int
	for _, member := range s.members[channelID] {
		if member.Status == domain.ChannelMemberKicked {
			kicked++
		}
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		participants++
		if member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
			admins++
		}
		if !zeroChannelBannedRights(member.BannedRights) {
			banned++
		}
	}
	channel.ParticipantsCount = participants
	channel.AdminsCount = admins
	channel.KickedCount = kicked
	channel.BannedCount = banned
	s.channels[channelID] = channel
}

func diffFinal(returned, all []domain.ChannelUpdateEvent) bool {
	if len(returned) == 0 {
		return true
	}
	return returned[len(returned)-1].Pts >= all[len(all)-1].Pts
}

func randomMemoryPositiveInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:]) & ((1 << 63) - 1)), nil
}

func discussionGroupUpdateResult(changed map[int64]domain.Channel) domain.DiscussionGroupUpdateResult {
	ids := make([]int64, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	out := domain.DiscussionGroupUpdateResult{Channels: make([]domain.Channel, 0, len(ids))}
	for _, id := range ids {
		out.Channels = append(out.Channels, cloneChannel(changed[id]))
	}
	return out
}

func (s *ChannelStore) topicWithViewerCountersLocked(viewerUserID, channelID int64, topic domain.ChannelForumTopic, member domain.ChannelMember) domain.ChannelForumTopic {
	out := cloneChannelForumTopic(topic)
	water := s.channelTopicReadInboxLocked(channelID, viewerUserID, topic.TopicID, member.AvailableMinID)
	out.UnreadCount = s.channelTopicUnreadCountLocked(viewerUserID, channelID, topic.TopicID, water)
	out.ReadInboxMaxID = water
	out.ReadOutboxMaxID = s.channelTopicReadOutboxLocked(channelID, viewerUserID, topic.TopicID)
	out.UnreadMentionsCount = s.countChannelUnreadMentionsLocked(viewerUserID, channelID, topic.TopicID)
	out.UnreadReactionsCount = s.countChannelUnreadReactionsLocked(viewerUserID, channelID, topic.TopicID)
	return out
}
