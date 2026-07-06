package rpc

import (
	"context"
	"errors"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"telesrv/internal/domain"
)

func reportResultForOption(option string) (tg.ReportResultClass, error) {
	switch option {
	case "":
		return &tg.ReportResultChooseOption{
			Title: "Report",
			Options: []tg.MessageReportOption{
				{Text: "Spam", Option: []byte("spam")},
				{Text: "Violence", Option: []byte("violence")},
				{Text: "Illegal goods", Option: []byte("illegal_goods")},
				{Text: "Child abuse", Option: []byte("child_abuse")},
				{Text: "Personal data", Option: []byte("personal_data")},
				{Text: "Copyright", Option: []byte("copyright")},
				{Text: "Other", Option: []byte("other")},
			},
		}, nil
	case "other":
		return &tg.ReportResultAddComment{Optional: false, Option: []byte("other:comment")}, nil
	case "spam", "violence", "illegal_goods", "child_abuse", "personal_data", "copyright", "other:comment":
		return &tg.ReportResultReported{}, nil
	default:
		return nil, tgerr.New(400, "OPTION_INVALID")
	}
}

func (r *Router) inputPeerForDomainPeer(ctx context.Context, currentUserID int64, peer domain.Peer) tg.InputPeerClass {
	switch peer.Type {
	case domain.PeerTypeUser:
		if u, ok := domain.SystemUserByID(peer.ID); ok {
			return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		}
		switch {
		case r.deps.Users == nil:
			return nil
		case peer.ID == currentUserID:
			u, err := r.deps.Users.Self(ctx, currentUserID)
			if err != nil || u.ID == 0 {
				return nil
			}
			return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		default:
			u, found, err := r.deps.Users.ByID(ctx, currentUserID, peer.ID)
			if err != nil || !found {
				return nil
			}
			return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		}
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil || peer.ID == 0 {
			return nil
		}
		// 只需 access_hash 解析 InputPeerChannel：走轻量 ResolveChannel（仅访问校验，省 dialog/读态/boost
		// 这 3 条额外查询）。inputPeerFor 是各消息类 RPC 的通用 peer 解析器、调用极频，是 GetChannel 放大的主源头。
		view, err := r.deps.Channels.ResolveChannel(ctx, currentUserID, peer.ID)
		if err != nil || view.Channel.ID == 0 {
			return nil
		}
		return &tg.InputPeerChannel{ChannelID: view.Channel.ID, AccessHash: view.Channel.AccessHash}
	default:
		return nil
	}
}

func validateHistoryBounds(offsetID, addOffset, limit, maxID, minID int) error {
	if offsetID < 0 || offsetID > domain.MaxMessageBoxID || maxID < 0 || maxID > domain.MaxMessageBoxID || minID < 0 || minID > domain.MaxMessageBoxID {
		return messageIDInvalidErr()
	}
	if addOffset < -100 || addOffset > 100 || limit < 0 || limit > maxSearchResultsLimit {
		return limitInvalidErr()
	}
	return nil
}

func (r *Router) savedHistoryChats(ctx context.Context, userID int64, hasParent bool, parent domain.Peer, peer tg.InputPeerClass) []tg.ChatClass {
	if r.deps.Channels == nil {
		return []tg.ChatClass{}
	}
	seen := make(map[int64]struct{}, 2)
	out := make([]tg.ChatClass, 0, 2)
	add := func(channelID int64) {
		if channelID == 0 {
			return
		}
		if _, ok := seen[channelID]; ok {
			return
		}
		seen[channelID] = struct{}{}
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
		if err != nil || view.Channel.ID == 0 {
			return
		}
		out = append(out, tgChannelChatForView(userID, view))
	}
	if hasParent && parent.Type == domain.PeerTypeChannel {
		add(parent.ID)
	}
	if p, ok := r.domainPeerFromInputPeer(userID, peer); ok && p.Type == domain.PeerTypeChannel {
		add(p.ID)
	}
	return out
}

func (r *Router) channelOnlineCount(ctx context.Context, userID, channelID int64) int {
	if channelID == 0 || r.deps.Channels == nil || r.deps.Sessions == nil {
		return 1
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return 1
	}
	online := provider.OnlineChannelUserIDs(channelID, domain.MaxChannelRealtimeFanout)
	candidates := make([]int64, 0, len(online)+1)
	if userID != 0 {
		candidates = append(candidates, userID)
	}
	candidates = append(candidates, online...)
	active, err := r.deps.Channels.FilterActiveMemberIDs(ctx, channelID, candidates)
	if err != nil {
		return 1
	}
	return len(active)
}

func (r *Router) onMessagesSetTyping(ctx context.Context, req *tg.MessagesSetTypingRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	topMsgID, topMsgIDSet := req.GetTopMsgID()
	if !topMsgIDSet && req.TopMsgID != 0 {
		topMsgID, topMsgIDSet = req.TopMsgID, true
	}
	if topMsgIDSet {
		switch {
		case topMsgID <= 0:
			topMsgID = 0
		case topMsgID > domain.MaxMessageBoxID:
			return false, msgIDInvalidErr()
		}
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == userID {
		if peer.Type == domain.PeerTypeChannel && peer.ID != 0 && r.deps.Channels != nil {
			action := req.Action
			if action == nil {
				action = &tg.SendMessageCancelAction{}
			}
			updates := &tg.Updates{
				Updates: []tg.UpdateClass{&tg.UpdateChannelUserTyping{
					ChannelID: peer.ID,
					FromID:    &tg.PeerUser{UserID: userID},
					TopMsgID:  topMsgID,
					Action:    action,
				}},
				Date: int(r.clock.Now().Unix()),
			}
			r.pushChannelViewerUpdates(ctx, 0, peer.ID, nil, func(int64) *tg.Updates {
				return updates
			})
		}
		return true, nil
	}
	action := req.Action
	if action == nil {
		action = &tg.SendMessageCancelAction{}
	}
	update := &tg.UpdateUserTyping{
		UserID:   userID,
		TopMsgID: topMsgID,
		Action:   action,
	}
	updates := &tg.UpdateShort{
		Update: update,
		Date:   int(r.clock.Now().Unix()),
	}
	r.pushTypingUpdate(ctx, peer.ID, updates)
	return true, nil
}

func (r *Router) pushTypingUpdate(ctx context.Context, targetUserID int64, updates *tg.UpdateShort) {
	// typing 是 transient（不写 durable log）：未就绪的 session 直接跳过、不进 pending。
	r.pushUserMessageTransient(ctx, targetUserID, "push typing update", updates)
}

func inputMessageBoxID(input tg.InputMessageClass) (int, bool) {
	switch msg := input.(type) {
	case *tg.InputMessageID:
		return msg.ID, true
	default:
		return 0, false
	}
}

func (r *Router) lookupOwnerMessage(ctx context.Context, userID int64, id int) (domain.Message, bool, error) {
	filter := domain.MessageFilter{
		MinID: id - 1,
		Limit: 1,
	}
	if id < domain.MaxMessageBoxID {
		filter.MaxID = id + 1
	}
	list, err := r.deps.Messages.Search(ctx, userID, filter)
	if err != nil {
		return domain.Message{}, false, err
	}
	if len(list.Messages) == 0 || list.Messages[0].ID != id {
		return domain.Message{}, false, nil
	}
	return list.Messages[0], true, nil
}

func draftClearUpdate(peer domain.Peer, topMessageID, date int) *tg.UpdateDraftMessage {
	peerTL := tgPeer(peer)
	if peerTL == nil {
		return nil
	}
	draft := &tg.DraftMessageEmpty{}
	draft.SetDate(date)
	update := &tg.UpdateDraftMessage{Peer: peerTL, Draft: draft}
	if topMessageID > 0 {
		update.SetTopMsgID(topMessageID)
	}
	return update
}

func draftReplyIsEmpty(reply tg.InputReplyToClass) bool {
	if reply == nil {
		return true
	}
	input, ok := reply.(*tg.InputReplyToMessage)
	if !ok {
		return false
	}
	topMsgID, hasTopMsgID := input.GetTopMsgID()
	return input.ReplyToMsgID == 0 && hasTopMsgID && topMsgID > 0
}

func draftInputMedia(media tg.InputMediaClass) tg.InputMediaClass {
	switch media.(type) {
	case nil, *tg.InputMediaEmpty:
		return nil
	default:
		return media
	}
}

func (r *Router) searchGlobalChannelOffsetID(ctx context.Context, userID int64, peer tg.InputPeerClass) (int64, error) {
	if peer == nil {
		return 0, nil
	}
	switch peer.(type) {
	case *tg.InputPeerEmpty:
		return 0, nil
	}
	ref, ok := inputPeerChannelRef(peer)
	if !ok {
		return 0, nil
	}
	if ref.ID <= 0 {
		return 0, peerIDInvalidErr()
	}
	if ref.CheckAccessHash && r.deps.Channels != nil {
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, ref.ID)
		if err != nil {
			return 0, channelInvalidErr(err)
		}
		if !inputChannelAccessHashMatches(ref, view.Channel) {
			return 0, channelInvalidErr(domain.ErrChannelPrivate)
		}
	}
	return ref.ID, nil
}

func (s forwardSource) userID() int64 {
	if s.from.Type == domain.PeerTypeUser {
		return s.from.ID
	}
	if s.forward != nil && s.forward.From.Type == domain.PeerTypeUser {
		return s.forward.From.ID
	}
	return 0
}

func (r *Router) onMessagesGetOutboxReadDate(ctx context.Context, req *tg.MessagesGetOutboxReadDateRequest) (*tg.OutboxReadDate, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Messages == nil {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	date, err := r.deps.Messages.GetOutboxReadDate(ctx, userID, domain.OutboxReadDateRequest{
		OwnerUserID: userID,
		Peer:        peer,
		ID:          req.MsgID,
	})
	if err != nil {
		return nil, messageReadDateErr(err)
	}
	return &tg.OutboxReadDate{Date: date}, nil
}

func (r *Router) onMessagesGetMessageReadParticipants(ctx context.Context, req *tg.MessagesGetMessageReadParticipantsRequest) ([]tg.ReadParticipantDate, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if r.deps.Channels == nil {
		return []tg.ReadParticipantDate{}, nil
	}
	res, err := r.deps.Channels.GetMessageReadParticipants(ctx, userID, domain.ChannelReadParticipantsRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, channelInvalidErr(err)
	}
	out := make([]tg.ReadParticipantDate, 0, len(res.Participants))
	for _, p := range res.Participants {
		if p.UserID == 0 {
			continue
		}
		out = append(out, tg.ReadParticipantDate{UserID: p.UserID, Date: p.Date})
	}
	return out, nil
}

func messageReadDateErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageNotReadYet):
		return messageNotReadYetErr()
	default:
		return internalErr()
	}
}

func forwardMessagesUnsupportedOptionErr(req *tg.MessagesForwardMessagesRequest) error {
	switch {
	case req.QuickReplyShortcut != nil:
		return shortcutInvalidErr()
	case req.Effect != 0:
		return effectIDInvalidErr()
	case req.VideoTimestamp != 0:
		return mediaInvalidErr()
	case req.AllowPaidStars < 0:
		return starsAmountInvalidErr()
	case req.AllowPaidStars > 0 || req.AllowPaidFloodskip:
		return paymentUnsupportedErr()
	case !req.SuggestedPost.Zero():
		return suggestedPostPeerInvalidErr()
	default:
		return nil
	}
}

func (r *Router) metrics() Metrics {
	if r.deps.Metrics == nil {
		return NopMetrics{}
	}
	return r.deps.Metrics
}

func (r *Router) domainFolderPeerFromInputPeer(ctx context.Context, userID int64, peer tg.InputPeerClass) (domain.Peer, int64, error) {
	if inputPeerClassNil(peer) {
		return domain.Peer{}, 0, peerIDInvalidErr()
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return domain.Peer{Type: domain.PeerTypeUser, ID: p.UserID}, p.AccessHash, nil
	case *tg.InputPeerChannel:
		out := domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChannelID}
		if err := r.validateInputPeerChannelAccess(ctx, userID, peer, p.ChannelID); err != nil {
			return domain.Peer{}, 0, err
		}
		return out, p.AccessHash, nil
	case *tg.InputPeerChannelFromMessage:
		out := domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChannelID}
		if p.ChannelID <= 0 {
			return domain.Peer{}, 0, peerIDInvalidErr()
		}
		return out, 0, nil
	case *tg.InputPeerChat:
		if p.ChatID <= 0 {
			return domain.Peer{}, 0, peerIDInvalidErr()
		}
		return domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChatID}, 0, nil
	case *tg.InputPeerSelf:
		if userID == 0 {
			return domain.Peer{}, 0, peerIDInvalidErr()
		}
		var accessHash int64
		if r.deps.Users != nil {
			if self, err := r.deps.Users.Self(ctx, userID); err == nil {
				accessHash = self.AccessHash
			}
		}
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, accessHash, nil
	default:
		return domain.Peer{}, 0, peerIDInvalidErr()
	}
}

func (r *Router) domainPeerFromInputPeer(userID int64, peer tg.InputPeerClass) (domain.Peer, bool) {
	if inputPeerClassNil(peer) {
		return domain.Peer{}, false
	}
	switch p := peer.(type) {
	case *tg.InputPeerEmpty:
		return domain.Peer{}, false
	case *tg.InputPeerUser:
		return domain.Peer{Type: domain.PeerTypeUser, ID: p.UserID}, true
	case *tg.InputPeerChannel:
		return domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChannelID}, true
	case *tg.InputPeerChannelFromMessage:
		return domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChannelID}, p.ChannelID > 0
	case *tg.InputPeerChat:
		return domain.Peer{Type: domain.PeerTypeChannel, ID: p.ChatID}, p.ChatID > 0
	case *tg.InputPeerSelf:
		if userID == 0 {
			return domain.Peer{}, false
		}
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, true
	default:
		return domain.Peer{}, false
	}
}

func inputPeerClassNil(peer tg.InputPeerClass) bool {
	switch typed := peer.(type) {
	case nil:
		return true
	case *tg.InputPeerEmpty:
		return typed == nil
	case *tg.InputPeerSelf:
		return typed == nil
	case *tg.InputPeerChat:
		return typed == nil
	case *tg.InputPeerUser:
		return typed == nil
	case *tg.InputPeerChannel:
		return typed == nil
	case *tg.InputPeerUserFromMessage:
		return typed == nil
	case *tg.InputPeerChannelFromMessage:
		return typed == nil
	default:
		return false
	}
}

func isLegacyInputPeerChat(peer tg.InputPeerClass) bool {
	typed, ok := peer.(*tg.InputPeerChat)
	return ok && typed != nil
}

func inputPeerChannelRef(peer tg.InputPeerClass) (channelInputRef, bool) {
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		if p == nil {
			return channelInputRef{}, false
		}
		return channelInputRef{
			ID:              p.ChannelID,
			AccessHash:      p.AccessHash,
			CheckAccessHash: p.AccessHash != 0,
		}, p.ChannelID > 0
	case *tg.InputPeerChannelFromMessage:
		if p == nil {
			return channelInputRef{}, false
		}
		return channelInputRef{ID: p.ChannelID}, p.ChannelID > 0
	default:
		return channelInputRef{}, false
	}
}

func (r *Router) validateInputPeerChannelAccess(ctx context.Context, userID int64, peer tg.InputPeerClass, channelID int64) error {
	ref, ok := inputPeerChannelRef(peer)
	if !ok || ref.ID != channelID || channelID <= 0 {
		return nil
	}
	if !ref.CheckAccessHash || r.deps.Channels == nil {
		return nil
	}
	// 这里只校验 InputPeerChannel 的 access_hash，不消费 dialog/read/unread/boost
	// 等完整频道视图字段。走 ResolveChannel，避免 messages.search/getPeerSettings
	// 这类高频入口为一次纯 access check 触发完整 GetChannel 投影。
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return channelInvalidErr(domain.ErrChannelPrivate)
	}
	return nil
}

func (r *Router) resolveInputPeerChannelView(ctx context.Context, userID int64, peer tg.InputPeerClass, channelID int64) (domain.ChannelView, error) {
	if channelID <= 0 || r.deps.Channels == nil {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if ref, ok := inputPeerChannelRef(peer); ok {
		if ref.ID != channelID || (ref.CheckAccessHash && !inputChannelAccessHashMatches(ref, view.Channel)) {
			return domain.ChannelView{}, domain.ErrChannelPrivate
		}
	}
	return view, nil
}

func (r *Router) checkedDomainPeerFromInputPeer(ctx context.Context, userID int64, peer tg.InputPeerClass) (domain.Peer, error) {
	out, ok := r.domainPeerFromInputPeer(userID, peer)
	if !ok || out.ID == 0 {
		return domain.Peer{}, peerIDInvalidErr()
	}
	if out.Type == domain.PeerTypeChannel {
		if err := r.validateInputPeerChannelAccess(ctx, userID, peer, out.ID); err != nil {
			return domain.Peer{}, err
		}
	}
	return out, nil
}

func (r *Router) chatsForInputPeer(ctx context.Context, userID int64, peer tg.InputPeerClass) []tg.ChatClass {
	p, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil || p.Type != domain.PeerTypeChannel || p.ID == 0 || r.deps.Channels == nil {
		return []tg.ChatClass{}
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, p.ID)
	if err != nil || view.Channel.ID == 0 {
		return []tg.ChatClass{}
	}
	return []tg.ChatClass{tgChannelChatForView(userID, view)}
}
