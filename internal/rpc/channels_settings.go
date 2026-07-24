package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"strings"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onChannelsCheckUsername(ctx context.Context, req *tg.ChannelsCheckUsernameRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return false, err
	}
	okUsername, err := r.deps.Channels.CheckUsername(ctx, userID, channelID, req.Username)
	if err != nil {
		return false, channelUsernameErr(err)
	}
	return okUsername, nil
}

func (r *Router) onChannelsUpdateUsername(ctx context.Context, req *tg.ChannelsUpdateUsernameRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return false, err
	}
	channel, err := r.deps.Channels.UpdateUsername(ctx, userID, domain.UpdateChannelUsernameRequest{
		UserID:    userID,
		ChannelID: channelID,
		Username:  req.Username,
	})
	if err != nil {
		return false, channelUsernameErr(err)
	}
	r.invalidateRPCProjectionForChannel(channel.ID)
	r.pushChannelStateToMembers(ctx, userID, channel)
	return true, nil
}

func (r *Router) onChannelsToggleSignatures(ctx context.Context, req *tg.ChannelsToggleSignaturesRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetSignatures(ctx, userID, channelID, req.SignaturesEnabled)
	})
}

func (r *Router) onChannelsTogglePreHistoryHidden(ctx context.Context, req *tg.ChannelsTogglePreHistoryHiddenRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetPreHistoryHidden(ctx, userID, channelID, req.Enabled)
	})
}

func (r *Router) onChannelsToggleSlowMode(ctx context.Context, req *tg.ChannelsToggleSlowModeRequest) (tg.UpdatesClass, error) {
	if !domain.ValidChannelSlowModeSeconds(req.Seconds) {
		return nil, secondsInvalidErr()
	}
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetSlowMode(ctx, userID, channelID, req.Seconds)
	})
}

func (r *Router) onChannelsSetStickers(ctx context.Context, req *tg.ChannelsSetStickersRequest) (bool, error) {
	_, view, err := r.channelChangeInfoView(ctx, req.Channel)
	if err != nil {
		return false, err
	}
	if !view.Channel.Megagroup || view.Channel.Broadcast {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if err := validateEmptyChannelStickerSet(req.Stickerset); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsSetEmojiStickers(ctx context.Context, req *tg.ChannelsSetEmojiStickersRequest) (bool, error) {
	_, view, err := r.channelChangeInfoView(ctx, req.Channel)
	if err != nil {
		return false, err
	}
	if !view.Channel.Megagroup || view.Channel.Broadcast {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if err := validateEmptyChannelStickerSet(req.Stickerset); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsReorderUsernames(ctx context.Context, req *tg.ChannelsReorderUsernamesRequest) (bool, error) {
	if len(req.Order) > maxChannelUsernameOrder {
		return false, limitInvalidErr()
	}
	if _, _, err := r.channelChangeInfoView(ctx, req.Channel); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsToggleUsername(ctx context.Context, req *tg.ChannelsToggleUsernameRequest) (bool, error) {
	if req.Username != "" && !validChannelManagementUsername(req.Username) {
		return false, usernameInvalidErr()
	}
	if _, _, err := r.channelChangeInfoView(ctx, req.Channel); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsDeactivateAllUsernames(ctx context.Context, input tg.InputChannelClass) (bool, error) {
	if _, _, err := r.channelChangeInfoView(ctx, input); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsUpdateColor(ctx context.Context, req *tg.ChannelsUpdateColorRequest) (tg.UpdatesClass, error) {
	return r.applyChannelChangeInfoMutation(ctx, req.Channel, func(ctx context.Context, userID int64, view domain.ChannelView) (domain.Channel, error) {
		return r.deps.Channels.SetColor(ctx, userID, view.Channel.ID, req.ForProfile, domainPeerColorFromChannelUpdate(req))
	})
}

func (r *Router) onChannelsUpdateEmojiStatus(ctx context.Context, req *tg.ChannelsUpdateEmojiStatusRequest) (tg.UpdatesClass, error) {
	viewerUserID, view, err := r.channelChangeInfoView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	status, err := domainChannelEmojiStatus(req.EmojiStatus)
	if err != nil {
		return nil, err
	}
	channel, err := r.deps.Channels.SetEmojiStatus(ctx, viewerUserID, view.Channel.ID, status)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, viewerUserID, channel), nil
}

func (r *Router) onChannelsSetDiscussionGroup(ctx context.Context, req *tg.ChannelsSetDiscussionGroupRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, notImplementedErr()
	}
	if req == nil {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	broadcastID, err := r.optionalChannelIDFromInput(ctx, userID, req.Broadcast)
	if err != nil {
		return false, channelDiscussionErr(err)
	}
	groupID, err := r.optionalChannelIDFromInput(ctx, userID, req.Group)
	if err != nil {
		return false, channelDiscussionErr(err)
	}
	res, err := r.deps.Channels.SetDiscussionGroup(ctx, userID, broadcastID, groupID)
	if err != nil {
		return false, channelDiscussionErr(err)
	}
	for _, channel := range res.Channels {
		r.invalidateRPCProjectionForChannel(channel.ID)
		r.pushChannelStateToMembers(ctx, userID, channel)
	}
	return true, nil
}

func (r *Router) onChannelsEditLocation(ctx context.Context, req *tg.ChannelsEditLocationRequest) (bool, error) {
	if _, _, err := r.channelChangeInfoView(ctx, req.Channel); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsConvertToGigagroup(ctx context.Context, input tg.InputChannelClass) (tg.UpdatesClass, error) {
	userID, view, err := r.channelChangeInfoView(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.channelStateUpdates(userID, view.Channel), nil
}

func (r *Router) onChannelsToggleAntiSpam(ctx context.Context, req *tg.ChannelsToggleAntiSpamRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetAntiSpam(ctx, userID, channelID, req.Enabled)
	})
}

func (r *Router) onChannelsReportAntiSpamFalsePositive(ctx context.Context, req *tg.ChannelsReportAntiSpamFalsePositiveRequest) (bool, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	userID, view, err := r.channelChangeInfoView(ctx, req.Channel)
	if err != nil {
		return false, err
	}
	if !view.Channel.Megagroup || view.Channel.Broadcast {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if r.deps.Moderation == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportAntiSpamFalsePositive(
		ctx, userID, view.Channel.ID, req.MsgID, r.clock.Now(),
	); err != nil {
		return false, moderationReportError(err)
	}
	return true, nil
}

func (r *Router) onChannelsGetChannelRecommendations(ctx context.Context, req *tg.ChannelsGetChannelRecommendationsRequest) (tg.MessagesChatsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Channels == nil {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	}
	sourceChannelID := int64(0)
	if req != nil {
		if input, ok := req.GetChannel(); ok {
			source, err := r.publicRecommendationSourceChannel(ctx, userID, input)
			if err != nil {
				return nil, err
			}
			sourceChannelID = source
		}
	}
	res, err := r.deps.Channels.ChannelRecommendations(ctx, userID, domain.ChannelRecommendationsRequest{
		UserID:          userID,
		SourceChannelID: sourceChannelID,
		Limit:           domain.DefaultChannelRecommendationsLimit,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	chats := tgChannels(userID, res.Channels)
	if res.Count > len(chats) {
		return &tg.MessagesChatsSlice{Count: res.Count, Chats: chats}, nil
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onChannelsSetBoostsToUnblockRestrictions(ctx context.Context, req *tg.ChannelsSetBoostsToUnblockRestrictionsRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.Boosts < 0 || req.Boosts > maxChannelBoostsToUnblockRestrictions {
		return nil, limitInvalidErr()
	}
	return r.applyChannelChangeInfoMutation(ctx, req.Channel, func(ctx context.Context, userID int64, view domain.ChannelView) (domain.Channel, error) {
		return r.deps.Channels.SetBoostsToUnblockRestrictions(ctx, userID, view.Channel.ID, req.Boosts)
	})
}

func (r *Router) onChannelsRestrictSponsoredMessages(ctx context.Context, req *tg.ChannelsRestrictSponsoredMessagesRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.applyChannelChangeInfoMutation(ctx, req.Channel, func(ctx context.Context, userID int64, view domain.ChannelView) (domain.Channel, error) {
		return r.deps.Channels.SetRestrictedSponsored(ctx, userID, view.Channel.ID, req.Restricted)
	})
}

func (r *Router) onChannelsUpdatePaidMessagesPrice(ctx context.Context, req *tg.ChannelsUpdatePaidMessagesPriceRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, view, err := r.channelChangeInfoView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if err := validateChannelPaidMessagesPriceRequest(req, view.Channel); err != nil {
		return nil, err
	}
	stars := req.SendPaidMessagesStars
	if stars < 0 {
		stars = 0
	}
	res, err := r.deps.Channels.SetPaidMessagesPrice(ctx, userID, view.Channel.ID, stars, req.BroadcastMessagesAllowed)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelPaidMessagesPriceUpdates(ctx, userID, res), nil
}

func validateChannelPaidMessagesPriceRequest(req *tg.ChannelsUpdatePaidMessagesPriceRequest, channel domain.Channel) error {
	stars := req.SendPaidMessagesStars
	if stars == -1 && channel.Broadcast && !req.BroadcastMessagesAllowed {
		return nil
	}
	if stars < 0 || stars > maxChannelPaidMessageStars {
		return starsAmountInvalidErr()
	}
	return nil
}

func (r *Router) onChannelsToggleAutotranslation(ctx context.Context, req *tg.ChannelsToggleAutotranslationRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.applyChannelChangeInfoMutation(ctx, req.Channel, func(ctx context.Context, userID int64, view domain.ChannelView) (domain.Channel, error) {
		return r.deps.Channels.SetAutotranslation(ctx, userID, view.Channel.ID, req.Enabled)
	})
}

func (r *Router) onChannelsEditTitle(ctx context.Context, req *tg.ChannelsEditTitleRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil && r.deps.Communities == nil {
		return nil, notImplementedErr()
	}
	if !validChannelTitle(req.Title) {
		return nil, channelInvalidErr(domain.ErrChannelTitleInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if community, ok, err := r.maybeCommunityFromInput(ctx, userID, req.Channel); ok {
		if err != nil {
			return nil, err
		}
		view, changed, err := r.deps.Communities.EditTitle(ctx, userID, community.Community.ID, req.Title)
		if err != nil {
			return nil, communityErr(err)
		}
		return r.communityMutationUpdates(ctx, userID, view, changed), nil
	}
	if r.deps.Channels == nil {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.EditTitle(ctx, userID, domain.EditChannelTitleRequest{
		UserID:    userID,
		ChannelID: channelID,
		Title:     req.Title,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateRPCProjectionForChannel(res.Channel.ID)
	updates := r.channelTitleUpdates(ctx, userID, res)
	// 混合容器拆分（设计 fan-out epic）：channelTitleUpdates 同时含①无 pts UpdateChannel（频道元数据
	// 刷新，无 channel difference 恢复面）②带 pts 改名服务消息（broadcast+megagroup 均产 pts，有
	// difference 恢复面）。①必须同步发（不能进可丢弃队列，否则永久漏人数/元数据刷新），但它无 Users
	// 投影、廉价；②走异步 fan-out（含 owner 预热 + >cap nudge，丢弃由 getChannelDifference 兜底），把
	// per-viewer 服务消息投影移出改名者 RPC 路径。操作者本设备仍由上面的 RPC result 即时回显完整容器。
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelStateUpdates(viewerUserID, res.Channel)
	})
	if res.Event.Pts != 0 {
		r.enqueueChannelMessageFanout(ctx, userID, domain.SendChannelMessageResult{
			Channel:    res.Channel,
			Message:    res.Message,
			Event:      res.Event,
			Recipients: res.Recipients,
		}, nil)
	}
	return updates, nil
}

func (r *Router) onChannelsEditPhoto(ctx context.Context, req *tg.ChannelsEditPhotoRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil && r.deps.Communities == nil {
		return nil, notImplementedErr()
	}
	if req.Photo == nil {
		return nil, photoInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	photo, err := r.resolveInputChatPhoto(ctx, userID, req.Photo)
	if err != nil {
		return nil, err
	}
	if community, ok, err := r.maybeCommunityFromInput(ctx, userID, req.Channel); ok {
		if err != nil {
			return nil, err
		}
		view, changed, err := r.deps.Communities.SetPhoto(ctx, userID, community.Community.ID, photo, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, communityErr(err)
		}
		return r.communityMutationUpdates(ctx, userID, view, changed), nil
	}
	if r.deps.Channels == nil {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.SetPhoto(ctx, userID, channelID, photo, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateRPCProjectionForChannel(res.Channel.ID)
	updates := r.channelPhotoUpdates(ctx, userID, res)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelStateUpdates(viewerUserID, res.Channel)
	})
	if res.Event.Pts != 0 {
		r.enqueueChannelMessageFanout(ctx, userID, domain.SendChannelMessageResult{
			Channel:    res.Channel,
			Message:    res.Message,
			Event:      res.Event,
			Recipients: res.Recipients,
		}, nil)
	}
	return updates, nil
}

func (r *Router) channelTitleUpdates(ctx context.Context, viewerUserID int64, res domain.EditChannelTitleResult) *tg.Updates {
	updates := []tg.UpdateClass{&tg.UpdateChannel{ChannelID: res.Channel.ID}}
	if res.Event.Pts != 0 {
		if update := tgChannelUpdate(viewerUserID, res.Event); update != nil {
			updates = append(updates, update)
		}
	}
	return &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, viewerUserID, []int64{res.Message.SenderUserID}),
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) channelPhotoUpdates(ctx context.Context, viewerUserID int64, res domain.SetChannelPhotoResult) *tg.Updates {
	updates := []tg.UpdateClass{&tg.UpdateChannel{ChannelID: res.Channel.ID}}
	if res.Event.Pts != 0 {
		if update := tgChannelUpdate(viewerUserID, res.Event); update != nil {
			updates = append(updates, update)
		}
	}
	return &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, viewerUserID, []int64{res.Message.SenderUserID}),
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func validChannelTitle(title string) bool {
	n := utf8.RuneCountInString(title)
	return n > 0 && n <= maxChannelTitleLength
}

func validChannelManagementUsername(username string) bool {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for i := 0; i < len(username); i++ {
		c := username[i]
		switch {
		case i == 0 && ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')):
		case i > 0 && ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'):
		default:
			return false
		}
	}
	return true
}

func domainPeerColorFromChannelUpdate(req *tg.ChannelsUpdateColorRequest) domain.ChannelPeerColor {
	if req == nil {
		return domain.ChannelPeerColor{}
	}
	color, hasColor := req.GetColor()
	backgroundEmojiID, hasBackground := req.GetBackgroundEmojiID()
	out := domain.ChannelPeerColor{HasColor: hasColor, Color: color}
	if hasBackground {
		out.BackgroundEmojiID = backgroundEmojiID
	}
	return out
}

func domainChannelEmojiStatus(status tg.EmojiStatusClass) (domain.ChannelEmojiStatus, error) {
	switch s := status.(type) {
	case *tg.EmojiStatusEmpty:
		return domain.ChannelEmojiStatus{}, nil
	case *tg.EmojiStatus:
		if s.DocumentID <= 0 {
			return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
		}
		until, _ := s.GetUntil()
		if until < 0 {
			return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
		}
		return domain.ChannelEmojiStatus{DocumentID: s.DocumentID, Until: until}, nil
	case *tg.EmojiStatusCollectible:
		if s.DocumentID <= 0 {
			return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
		}
		until, _ := s.GetUntil()
		if until < 0 {
			return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
		}
		return domain.ChannelEmojiStatus{DocumentID: s.DocumentID, Until: until}, nil
	case *tg.InputEmojiStatusCollectible:
		return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
	default:
		return domain.ChannelEmojiStatus{}, tgerr400("EMOJI_STATUS_INVALID")
	}
}

func channelUsernameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameOccupied):
		return usernameOccupiedErr()
	case errors.Is(err, domain.ErrChannelNotModified):
		return usernameNotModifiedErr()
	default:
		return channelAdminErr(err)
	}
}
