package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSetDefaultReaction(ctx context.Context, reaction tg.ReactionClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	parsed, err := domainMessageReactionFromTL(reaction)
	if err != nil {
		return false, err
	}
	if err := r.validateDefaultReaction(ctx, parsed); err != nil {
		return false, err
	}
	if svc, ok := r.deps.Account.(accountDefaultReactionService); ok {
		if _, err := svc.SetDefaultReaction(ctx, userID, parsed); err != nil {
			return false, internalErr()
		}
	}
	return true, nil
}

func (r *Router) validateDefaultReaction(ctx context.Context, reaction domain.MessageReaction) error {
	if reaction.Type == domain.MessageReactionCustomEmoji {
		return nil
	}
	if reaction.Type != domain.MessageReactionEmoji {
		return reactionInvalidErr()
	}

	if r.deps.Files != nil {
		catalog, err := r.deps.Files.ListAvailableReactions(ctx)
		if err != nil {
			return internalErr()
		}
		if len(catalog) > 0 {
			for _, item := range catalog {
				if !item.Inactive && strings.TrimSpace(item.Reaction) == reaction.Emoticon {
					return nil
				}
			}
			return reactionInvalidErr()
		}
	}

	for _, item := range staticReactionCatalog() {
		if item.Key() == reaction.Key() {
			return nil
		}
	}
	return reactionInvalidErr()
}

func (r *Router) onMessagesGetPaidReactionPrivacy(ctx context.Context) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings := domain.DefaultAccountReactionSettings()
	if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
		next, err := svc.GetReactionSettings(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		settings = next
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePaidReactionPrivacy{
			Private: r.tgPaidReactionPrivacy(ctx, userID, settings.PaidPrivacy),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}, nil
}

func (r *Router) onMessagesTogglePaidReactionPrivacy(ctx context.Context, req *tg.MessagesTogglePaidReactionPrivacyRequest) (bool, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	privacy, err := r.domainPaidReactionPrivacy(ctx, userID, req.Private)
	if err != nil {
		return false, err
	}
	if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
		next, err := svc.SetPaidReactionPrivacy(ctx, userID, privacy)
		if err != nil {
			return false, internalErr()
		}
		privacy = next.PaidPrivacy
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePaidReactionPrivacy{Private: r.tgPaidReactionPrivacy(ctx, userID, privacy)}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
	return true, nil
}

// onMessagesSendPaidReaction sends a paid reaction for a broadcast channel
// message. The account debit, full channel credit, reaction accumulation and
// random_id receipt are committed atomically by the channel service. Crash
// constraint: it must return valid Updates -- DrKLO's StarsController casts the
// response to TLRPC.Updates without an instanceof check.
func (r *Router) onMessagesSendPaidReaction(ctx context.Context, req *tg.MessagesSendPaidReactionRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if req.Count <= 0 || req.Count > domain.MaxPaidReactionStarsPerRequest {
		return nil, starsAmountInvalidErr()
	}
	if req.RandomID == 0 {
		return nil, randomIDEmptyErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	// Paid reactions are only for (broadcast) channel posts.
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	privacy, err := r.paidReactionPrivacyIntent(userID, req)
	if err != nil {
		return nil, err
	}
	paidSvc, ok := r.deps.Channels.(channelPaidReactionService)
	if !ok {
		return nil, notImplementedErr()
	}
	command := domain.SendChannelPaidReactionRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Stars:     int64(req.Count),
		RandomID:  req.RandomID,
		Privacy:   privacy,
		Date:      int(r.clock.Now().Unix()),
	}
	var res domain.ChannelMessagePaidReactionResult
	if replayer, ok := r.deps.Channels.(channelPaidReactionReplayService); ok {
		var found bool
		res, found, err = replayer.ReplayPaidReaction(ctx, userID, command)
		if err != nil {
			return nil, channelReactionErr(err)
		}
		if found {
			return r.paidReactionResponse(ctx, userID, res), nil
		}
	}

	// A new command still requires the current target access hash and the
	// current send-as ownership. Completed receipts above deliberately do not.
	checkedPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if checkedPeer != peer {
		return nil, peerIDInvalidErr()
	}
	if private, present := req.GetPrivate(); present {
		if peerPrivacy, ok := private.(*tg.PaidReactionPrivacyPeer); ok {
			checkedDisplay, checkedErr := r.checkedDomainPeerFromInputPeer(ctx, userID, peerPrivacy.Peer)
			if checkedErr != nil || privacy.Peer == nil || checkedDisplay != *privacy.Peer {
				return nil, sendAsPeerInvalidErr()
			}
		}
	}
	command.Anonymous, command.DisplayPeer, err = r.resolvePaidReactionDisplay(ctx, userID, privacy)
	if err != nil {
		return nil, err
	}
	res, err = paidSvc.SendPaidReaction(ctx, userID, command)
	if err != nil {
		if errors.Is(err, domain.ErrStarsInsufficient) || errors.Is(err, domain.ErrStarsInvalidAmount) {
			return nil, starsErr(err)
		}
		return nil, channelReactionErr(err)
	}
	return r.paidReactionResponse(ctx, userID, res), nil
}

func (r *Router) paidReactionResponse(ctx context.Context, userID int64, res domain.ChannelMessagePaidReactionResult) tg.UpdatesClass {
	// Build and fan out updateMessageReactions; the requester additionally gets updateStarsBalance.
	ids := []int{res.Message.ID}
	build := func(viewerUserID int64) *tg.Updates {
		updates := r.channelPaidReactionUpdates(ctx, userID, viewerUserID, res, ids)
		if updates != nil && viewerUserID == userID {
			updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: res.PayerBalance.Balance}})
		}
		return updates
	}
	if !res.Duplicate {
		recipients := append([]int64{res.Message.SenderUserID}, res.Recipients...)
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, recipients, build)
	}
	return build(userID)
}

// paidReactionPrivacyIntent preserves the wire flag distinction: missing
// private means account-default; explicit paidReactionPrivacyDefault means self.
func (r *Router) paidReactionPrivacyIntent(userID int64, req *tg.MessagesSendPaidReactionRequest) (domain.PaidReactionPrivacy, error) {
	if private, ok := req.GetPrivate(); ok {
		return r.parsePaidReactionPrivacy(userID, private)
	}
	return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAccountDefault}, nil
}

// resolvePaidReactionDisplay resolves mutable account defaults only for a new
// command and verifies that any channel identity is currently send-as eligible.
func (r *Router) resolvePaidReactionDisplay(ctx context.Context, userID int64, intent domain.PaidReactionPrivacy) (bool, domain.Peer, error) {
	effective := intent
	if intent.Kind == domain.PaidReactionPrivacyAccountDefault {
		effective = domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyDefault}
		if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
			settings, err := svc.GetReactionSettings(ctx, userID)
			if err != nil {
				return false, domain.Peer{}, internalErr()
			}
			effective = settings.PaidPrivacy
		}
	}
	if err := r.validatePaidReactionPrivacy(ctx, userID, effective); err != nil {
		return false, domain.Peer{}, err
	}
	switch effective.Kind {
	case domain.PaidReactionPrivacyAnonymous:
		return true, domain.Peer{}, nil
	case domain.PaidReactionPrivacyPeer:
		return false, *effective.Peer, nil
	default:
		return false, domain.Peer{}, nil
	}
}

func (r *Router) domainPaidReactionPrivacy(ctx context.Context, userID int64, in tg.PaidReactionPrivacyClass) (domain.PaidReactionPrivacy, error) {
	privacy, err := r.parsePaidReactionPrivacy(userID, in)
	if err != nil {
		return domain.PaidReactionPrivacy{}, err
	}
	if peerPrivacy, ok := in.(*tg.PaidReactionPrivacyPeer); ok {
		checked, checkedErr := r.checkedDomainPeerFromInputPeer(ctx, userID, peerPrivacy.Peer)
		if checkedErr != nil || privacy.Peer == nil || checked != *privacy.Peer {
			return domain.PaidReactionPrivacy{}, sendAsPeerInvalidErr()
		}
	}
	if err := r.validatePaidReactionPrivacy(ctx, userID, privacy); err != nil {
		return domain.PaidReactionPrivacy{}, err
	}
	return privacy, nil
}

func (r *Router) parsePaidReactionPrivacy(userID int64, in tg.PaidReactionPrivacyClass) (domain.PaidReactionPrivacy, error) {
	switch typed := in.(type) {
	case nil, *tg.PaidReactionPrivacyDefault:
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyDefault}, nil
	case *tg.PaidReactionPrivacyAnonymous:
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAnonymous}, nil
	case *tg.PaidReactionPrivacyPeer:
		peer, ok := r.domainPeerFromInputPeer(userID, typed.Peer)
		if !ok || peer.ID == 0 {
			return domain.PaidReactionPrivacy{}, sendAsPeerInvalidErr()
		}
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer}, nil
	default:
		return domain.PaidReactionPrivacy{}, inputConstructorInvalidErr()
	}
}

func (r *Router) validatePaidReactionPrivacy(ctx context.Context, userID int64, privacy domain.PaidReactionPrivacy) error {
	if privacy.Kind != domain.PaidReactionPrivacyPeer {
		return nil
	}
	if privacy.Peer == nil || privacy.Peer.Type != domain.PeerTypeChannel || privacy.Peer.ID <= 0 || r.deps.Channels == nil {
		return sendAsPeerInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, privacy.Peer.ID)
	if err != nil {
		return sendAsPeerInvalidErr()
	}
	if view.Channel.ID == privacy.Peer.ID && view.Channel.Broadcast && !view.Channel.Deleted &&
		view.Channel.CreatorUserID == userID && view.Self.Status == domain.ChannelMemberActive &&
		view.Self.Role == domain.ChannelRoleCreator {
		return nil
	}
	return sendAsPeerInvalidErr()
}

func (r *Router) tgPaidReactionPrivacy(ctx context.Context, userID int64, in domain.PaidReactionPrivacy) tg.PaidReactionPrivacyClass {
	switch in.Kind {
	case domain.PaidReactionPrivacyAnonymous:
		return &tg.PaidReactionPrivacyAnonymous{}
	case domain.PaidReactionPrivacyPeer:
		if in.Peer == nil {
			return &tg.PaidReactionPrivacyDefault{}
		}
		if peer := r.inputPeerForDomainPeer(ctx, userID, *in.Peer); peer != nil {
			return &tg.PaidReactionPrivacyPeer{Peer: peer}
		}
	}
	return &tg.PaidReactionPrivacyDefault{}
}
