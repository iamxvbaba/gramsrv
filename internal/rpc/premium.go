package rpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) registerPremium(d *tg.ServerDispatcher) {
	d.OnPremiumGetBoostsStatus(r.onPremiumGetBoostsStatus)
	d.OnPremiumGetBoostsList(r.onPremiumGetBoostsList)
	d.OnPremiumGetMyBoosts(r.onPremiumGetMyBoosts)
	d.OnPremiumApplyBoost(r.onPremiumApplyBoost)
	d.OnPremiumGetUserBoosts(r.onPremiumGetUserBoosts)
}

func (r *Router) onPremiumGetBoostsStatus(ctx context.Context, peer tg.InputPeerClass) (*tg.PremiumBoostsStatus, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, view, err := r.premiumBoostChannelView(ctx, peer, false)
	if err != nil {
		return nil, err
	}
	status, err := r.deps.Channels.GetPremiumBoostStatus(ctx, userID, view.Channel.ID, int(time.Now().Unix()))
	if err != nil {
		return nil, premiumBoostErr(err)
	}
	return tgPremiumBoostsStatus(view.Channel.ID, status, r.cfg.PublicBaseURL), nil
}

func (r *Router) onPremiumGetBoostsList(ctx context.Context, req *tg.PremiumGetBoostsListRequest) (*tg.PremiumBoostsList, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.Limit < 0 || req.Limit > domain.MaxPremiumBoostsListLimit || len(req.Offset) > domain.MaxPremiumBoostsOffsetBytes {
		return nil, limitInvalidErr()
	}
	userID, view, err := r.premiumBoostChannelView(ctx, req.Peer, true)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = domain.MaxPremiumBoostsListLimit
	}
	list, err := r.deps.Channels.ListPremiumBoosts(ctx, userID, view.Channel.ID, req.Gifts, req.Offset, limit, int(time.Now().Unix()))
	if err != nil {
		return nil, premiumBoostErr(err)
	}
	return r.tgPremiumBoostsList(ctx, userID, list), nil
}

func (r *Router) onPremiumGetMyBoosts(ctx context.Context) (*tg.PremiumMyBoosts, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, premiumUntil, err := r.currentPremiumUntil(ctx)
	if err != nil {
		return nil, err
	}
	my, err := r.deps.Channels.GetPremiumMyBoosts(ctx, userID, int(time.Now().Unix()), premiumUntil)
	if err != nil {
		return nil, premiumBoostErr(err)
	}
	return r.tgPremiumMyBoosts(ctx, userID, my), nil
}

func (r *Router) onPremiumApplyBoost(ctx context.Context, req *tg.PremiumApplyBoostRequest) (*tg.PremiumMyBoosts, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	slots, ok := req.GetSlots()
	if !ok {
		return nil, tgerr400("BOOSTS_EMPTY")
	}
	if len(slots) == 0 {
		return nil, tgerr400("SLOTS_EMPTY")
	}
	if len(slots) > domain.MaxPremiumBoostSlotsPerApply {
		return nil, limitInvalidErr()
	}
	for _, slot := range slots {
		if slot != domain.DefaultPremiumBoostSlotID {
			return nil, tgerr400("SLOTS_INVALID")
		}
	}
	userID, view, err := r.premiumBoostChannelView(ctx, req.Peer, false)
	if err != nil {
		return nil, err
	}
	_, premiumUntil, err := r.currentPremiumUntil(ctx)
	if err != nil {
		return nil, err
	}
	my, err := r.deps.Channels.ApplyPremiumBoost(ctx, userID, view.Channel.ID, slots, int(time.Now().Unix()), premiumUntil)
	if err != nil {
		return nil, premiumBoostErr(err)
	}
	return r.tgPremiumMyBoosts(ctx, userID, my), nil
}

func (r *Router) onPremiumGetUserBoosts(ctx context.Context, req *tg.PremiumGetUserBoostsRequest) (*tg.PremiumBoostsList, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, view, err := r.premiumBoostChannelView(ctx, req.Peer, true)
	if err != nil {
		return nil, err
	}
	ids, err := r.userIDsFromInputUsers(ctx, userID, []tg.InputUserClass{req.UserID})
	if err != nil {
		return nil, err
	}
	if len(ids) != 1 || ids[0] == 0 {
		return nil, userIDInvalidErr()
	}
	list, err := r.deps.Channels.GetPremiumUserBoosts(ctx, userID, view.Channel.ID, ids[0], int(time.Now().Unix()))
	if err != nil {
		return nil, premiumBoostErr(err)
	}
	return r.tgPremiumBoostsList(ctx, userID, list), nil
}

func (r *Router) premiumBoostChannelView(ctx context.Context, peer tg.InputPeerClass, requireAdmin bool) (int64, domain.ChannelView, error) {
	ref, ok := premiumBoostChannelRef(peer)
	if !ok {
		return 0, domain.ChannelView{}, peerIDInvalidErr()
	}
	input := &tg.InputChannel{ChannelID: ref.ID}
	if ref.CheckAccessHash {
		input.AccessHash = ref.AccessHash
	}
	userID, view, err := r.channelView(ctx, input)
	if err != nil {
		return 0, domain.ChannelView{}, err
	}
	if requireAdmin && view.Self.Role != domain.ChannelRoleCreator && view.Self.Role != domain.ChannelRoleAdmin {
		return 0, domain.ChannelView{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return userID, view, nil
}

func premiumBoostChannelRef(peer tg.InputPeerClass) (channelInputRef, bool) {
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		return channelInputRef{
			ID:              p.ChannelID,
			AccessHash:      p.AccessHash,
			CheckAccessHash: p.AccessHash != 0,
		}, p.ChannelID > 0
	case *tg.InputPeerChannelFromMessage:
		return channelInputRef{ID: p.ChannelID}, p.ChannelID > 0
	case *tg.InputPeerChat:
		return channelInputRef{ID: p.ChatID}, p.ChatID > 0
	default:
		return channelInputRef{}, false
	}
}

func (r *Router) currentPremiumUntil(ctx context.Context) (int64, int, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return 0, 0, internalErr()
	}
	if r.deps.Users == nil {
		return userID, 0, nil
	}
	u, found, err := r.deps.Users.ByID(ctx, userID, userID)
	if err != nil {
		return 0, 0, internalErr()
	}
	if !found {
		return userID, 0, nil
	}
	return userID, u.PremiumUntil, nil
}

func tgPremiumBoostsStatus(channelID int64, in domain.PremiumBoostStatus, publicBaseURL string) *tg.PremiumBoostsStatus {
	out := &tg.PremiumBoostsStatus{
		MyBoost:            len(in.MyBoostSlots) > 0,
		Level:              in.Level,
		CurrentLevelBoosts: in.CurrentLevelBoosts,
		Boosts:             in.Boosts,
		BoostURL:           publicLinkParamWithBaseURL(publicBaseURL, "boost", "c", fmt.Sprintf("%d", channelID)),
	}
	if in.GiftBoosts > 0 {
		out.SetGiftBoosts(in.GiftBoosts)
	}
	if in.HasNextLevelBoosts {
		out.SetNextLevelBoosts(in.NextLevelBoosts)
	}
	if in.PremiumAudienceTotal > 0 {
		out.SetPremiumAudience(tg.StatsPercentValue{
			Part:  float64(in.PremiumAudiencePart),
			Total: float64(in.PremiumAudienceTotal),
		})
	}
	if len(in.MyBoostSlots) > 0 {
		out.SetMyBoostSlots(premiumBoostSlotIDs(in.MyBoostSlots))
	}
	return out
}

func (r *Router) tgPremiumBoostsList(ctx context.Context, viewerUserID int64, in domain.PremiumBoostList) *tg.PremiumBoostsList {
	users := in.Users
	if len(users) == 0 {
		users = r.domainUsersForIDs(ctx, viewerUserID, premiumBoostUserIDs(in.Boosts))
	}
	out := &tg.PremiumBoostsList{
		Count:  in.Count,
		Boosts: tgBoosts(in.Boosts),
		Users:  tgUsersForViewer(viewerUserID, users),
	}
	if in.NextOffset != "" {
		out.SetNextOffset(in.NextOffset)
	}
	return out
}

func (r *Router) tgPremiumMyBoosts(ctx context.Context, viewerUserID int64, in domain.PremiumMyBoosts) *tg.PremiumMyBoosts {
	users := in.Users
	if len(users) == 0 {
		users = r.domainUsersForIDs(ctx, viewerUserID, premiumBoostPeerUserIDs(in.Slots))
	}
	return &tg.PremiumMyBoosts{
		MyBoosts: tgMyBoosts(in.Slots),
		Chats:    tgChannels(viewerUserID, in.Channels),
		Users:    tgUsersForViewer(viewerUserID, users),
	}
}

func tgBoosts(slots []domain.PremiumBoostSlot) []tg.Boost {
	out := make([]tg.Boost, 0, len(slots))
	for _, slot := range slots {
		boost := tg.Boost{
			ID:      fmt.Sprintf("%d:%d:%d", slot.UserID, slot.Slot, slot.Date),
			Date:    slot.Date,
			Expires: slot.Expires,
		}
		if slot.UserID != 0 {
			boost.SetUserID(slot.UserID)
		}
		if slot.Gift {
			boost.SetGift(true)
		}
		if slot.Giveaway {
			boost.SetGiveaway(true)
		}
		if slot.Unclaimed {
			boost.SetUnclaimed(true)
		}
		if slot.GiveawayMsgID > 0 {
			boost.SetGiveawayMsgID(slot.GiveawayMsgID)
		}
		if slot.UsedGiftSlug != "" {
			boost.SetUsedGiftSlug(slot.UsedGiftSlug)
		}
		if slot.Multiplier > 1 {
			boost.SetMultiplier(slot.Multiplier)
		}
		if slot.Stars > 0 {
			boost.SetStars(slot.Stars)
		}
		out = append(out, boost)
	}
	return out
}

func tgMyBoosts(slots []domain.PremiumBoostSlot) []tg.MyBoost {
	out := make([]tg.MyBoost, 0, len(slots))
	for _, slot := range slots {
		item := tg.MyBoost{
			Slot:    slot.Slot,
			Date:    slot.Date,
			Expires: slot.Expires,
		}
		if peer := tgPeer(slot.Peer); peer != nil {
			item.SetPeer(peer)
		}
		if slot.CooldownUntil > 0 {
			item.SetCooldownUntilDate(slot.CooldownUntil)
		}
		out = append(out, item)
	}
	return out
}

func premiumBoostUserIDs(slots []domain.PremiumBoostSlot) []int64 {
	ids := make([]int64, 0, len(slots))
	for _, slot := range slots {
		if slot.UserID != 0 {
			ids = append(ids, slot.UserID)
		}
	}
	return ids
}

func premiumBoostSlotIDs(slots []domain.PremiumBoostSlot) []int {
	ids := make([]int, 0, len(slots))
	seen := make(map[int]struct{}, len(slots))
	for _, slot := range slots {
		if slot.Slot <= 0 {
			continue
		}
		if _, ok := seen[slot.Slot]; ok {
			continue
		}
		seen[slot.Slot] = struct{}{}
		ids = append(ids, slot.Slot)
	}
	return ids
}

func premiumBoostPeerUserIDs(slots []domain.PremiumBoostSlot) []int64 {
	ids := make([]int64, 0)
	for _, slot := range slots {
		if slot.Peer.Type == domain.PeerTypeUser && slot.Peer.ID != 0 {
			ids = append(ids, slot.Peer.ID)
		}
	}
	return ids
}

func premiumBoostErr(err error) error {
	if seconds, ok := domain.PremiumBoostFloodWaitSeconds(err); ok {
		return floodWaitErr(seconds)
	}
	switch {
	case errors.Is(err, domain.ErrPremiumRequired):
		return tgerr400("PREMIUM_ACCOUNT_REQUIRED")
	case errors.Is(err, domain.ErrBoostNotModified):
		return tgerr400("BOOST_NOT_MODIFIED")
	case errors.Is(err, domain.ErrChannelAdminRequired):
		return tgerr400("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrChannelInvalid):
		return tgerr400("CHANNEL_INVALID")
	case errors.Is(err, domain.ErrChannelPrivate):
		return tgerr400("CHANNEL_PRIVATE")
	case errors.Is(err, domain.ErrChannelUserBanned):
		return tgerr400("USER_BANNED_IN_CHANNEL")
	default:
		return internalErr()
	}
}
