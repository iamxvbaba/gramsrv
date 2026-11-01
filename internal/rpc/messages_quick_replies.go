package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (r *Router) onMessagesGetQuickReplies(ctx context.Context, hash int64) (tg.MessagesQuickRepliesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return &tg.MessagesQuickReplies{
			QuickReplies: []tg.QuickReply{},
			Messages:     []tg.MessageClass{},
			Chats:        []tg.ChatClass{},
			Users:        []tg.UserClass{},
		}, nil
	}
	list, err := svc.ListQuickReplies(ctx, userID)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	if hash != 0 && hash == list.Hash {
		return &tg.MessagesQuickRepliesNotModified{}, nil
	}
	out := tgMessagesQuickReplies(list)
	out.Users = r.quickReplyUsers(ctx, userID)
	return out, nil
}

func (r *Router) onMessagesCheckQuickReplyShortcut(ctx context.Context, shortcut string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	available, err := svc.CheckQuickReplyShortcut(ctx, userID, shortcut)
	if err != nil {
		return false, businessAutomationErr(err)
	}
	return available, nil
}

func (r *Router) onMessagesReorderQuickReplies(ctx context.Context, order []int) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	_, err = svc.MutateQuickReplies(ctx, store.QuickReplyAccountMutation{
		Kind: store.QuickReplyAccountReorder, UserID: userID, Date: int(r.clock.Now().Unix()), Order: order,
	}, quickReplyAccountDeliveryEffects)
	if err != nil {
		return false, businessAutomationErr(err)
	}
	return true, nil
}

func (r *Router) onMessagesEditQuickReplyShortcut(ctx context.Context, req *tg.MessagesEditQuickReplyShortcutRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req == nil || req.ShortcutID <= 0 {
		return false, shortcutInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	_, err = svc.MutateQuickReplies(ctx, store.QuickReplyAccountMutation{
		Kind: store.QuickReplyAccountRenameShortcut, UserID: userID, Date: int(r.clock.Now().Unix()),
		ShortcutID: req.ShortcutID, Shortcut: req.Shortcut,
	}, quickReplyAccountDeliveryEffects)
	if err != nil {
		return false, businessAutomationErr(err)
	}
	return true, nil
}

func (r *Router) onMessagesDeleteQuickReplyShortcut(ctx context.Context, shortcutID int) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if shortcutID <= 0 {
		return false, shortcutInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, shortcutInvalidErr()
	}
	_, err = svc.MutateQuickReplies(ctx, store.QuickReplyAccountMutation{
		Kind: store.QuickReplyAccountDeleteShortcut, UserID: userID, Date: int(r.clock.Now().Unix()), ShortcutID: shortcutID,
	}, quickReplyAccountDeliveryEffects)
	if err != nil {
		return false, businessAutomationErr(err)
	}
	return true, nil
}

func (r *Router) onMessagesGetQuickReplyMessages(ctx context.Context, req *tg.MessagesGetQuickReplyMessagesRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || len(req.ID) > domain.MaxQuickReplyMessages {
		return nil, shortcutInvalidErr()
	}
	if req.ShortcutID <= 0 {
		return emptyQuickReplyMessages(), nil
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return emptyQuickReplyMessages(), nil
	}
	list, err := svc.GetQuickReplyMessages(ctx, userID, req.ShortcutID, req.ID)
	if err != nil {
		if errors.Is(err, domain.ErrShortcutInvalid) {
			return emptyQuickReplyMessages(), nil
		}
		return nil, businessAutomationErr(err)
	}
	if req.Hash != 0 && req.Hash == list.Hash {
		return &tg.MessagesMessagesNotModified{Count: list.Count}, nil
	}
	out := tgMessagesQuickReplyMessages(list)
	out.Users = r.quickReplyUsers(ctx, userID)
	r.applyPeerReadModelsToMessages(ctx, userID, out)
	return out, nil
}

func emptyQuickReplyMessages() *tg.MessagesMessages {
	return &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Chats:    []tg.ChatClass{},
		Users:    []tg.UserClass{},
	}
}

func (r *Router) onMessagesSendQuickReplyMessages(ctx context.Context, req *tg.MessagesSendQuickReplyMessagesRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || req.ShortcutID <= 0 || len(req.ID) > domain.MaxQuickReplyMessages || len(req.RandomID) > domain.MaxQuickReplyMessages {
		return nil, shortcutInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok || r.deps.Messages == nil {
		return nil, shortcutInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeUser {
		return nil, peerIDInvalidErr()
	}
	list, err := svc.GetQuickReplyMessages(ctx, userID, req.ShortcutID, req.ID)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	if len(list.Messages) == 0 {
		return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}, nil
	}
	randomIDs := append([]int64(nil), req.RandomID...)
	if len(randomIDs) == 0 {
		randomIDs = r.quickReplyRandomIDs(len(list.Messages))
	}
	if len(randomIDs) != len(list.Messages) {
		return nil, inputRequestInvalidErr()
	}
	for _, randomID := range randomIDs {
		if randomID == 0 {
			return nil, randomIDEmptyErr()
		}
	}
	if err := r.checkSendRateLimit(ctx, userID, len(list.Messages)); err != nil {
		return nil, err
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, peer.ID)
	if err != nil {
		return nil, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	res := domain.ForwardPrivateMessagesResult{OwnerUserID: userID}
	now := int(r.clock.Now().Unix())
	for i, template := range list.Messages {
		sent, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
			SenderUserID:     userID,
			RecipientUserID:  peer.ID,
			RandomID:         randomIDs[i],
			Message:          template.Message,
			Entities:         append([]domain.MessageEntity(nil), template.Entities...),
			Date:             now,
			OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
			OriginSessionID:  sessionID,
			RecipientBlocked: recipientBlocked,
		})
		if err != nil {
			return nil, messageSendErr(err)
		}
		if !sent.Duplicate {
			if err := r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, sent); err != nil {
				return nil, internalErr()
			}
		}
		res.SenderMessages = append(res.SenderMessages, sent.SenderMessage)
		res.RecipientMessages = append(res.RecipientMessages, sent.RecipientMessage)
		res.SenderEvents = append(res.SenderEvents, sent.SenderEvent)
		res.RecipientEvents = append(res.RecipientEvents, sent.RecipientEvent)
		res.Duplicates = append(res.Duplicates, sent.Duplicate)
	}
	return tgForwardMessagesUpdates(res, randomIDs, r.usersForMessageUpdates(ctx, userID, res.SenderMessages), r.chatsForMessageUpdates(ctx, userID, res.SenderMessages)), nil
}

func (r *Router) onMessagesDeleteQuickReplyMessages(ctx context.Context, req *tg.MessagesDeleteQuickReplyMessagesRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || req.ShortcutID <= 0 || len(req.ID) == 0 || len(req.ID) > domain.MaxQuickReplyMessages {
		return nil, shortcutInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, shortcutInvalidErr()
	}
	snapshot, err := svc.MutateQuickReplies(ctx, store.QuickReplyAccountMutation{
		Kind: store.QuickReplyAccountDeleteMessages, UserID: userID, Date: int(r.clock.Now().Unix()),
		ShortcutID: req.ShortcutID, MessageIDs: req.ID,
	}, quickReplyAccountDeliveryEffects)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	return r.quickReplyMutationUpdates(ctx, userID, snapshot, nil)
}

func (r *Router) onMessagesSaveQuickReplyText(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	shortcut, err := quickReplyShortcutName(req.QuickReplyShortcut)
	if err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID != userID {
		return nil, shortcutInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, premiumAccountRequiredErr()
	}
	date := int(r.clock.Now().Unix())
	snapshot, err := svc.MutateQuickReplies(ctx, store.QuickReplyAccountMutation{
		Kind: store.QuickReplyAccountSaveText, UserID: userID, Date: date, Shortcut: shortcut,
		Message: domain.QuickReplyMessage{
			RandomID: req.RandomID,
			Date:     date,
			Message:  req.Message,
			// Quick-reply templates get server-side auto entities like ordinary sends (url/@mention/#hashtag/bot command).
			Entities: domainMessageEntitiesForViewer(userID, r.augmentAutoEntities(req.Message, req.Entities)),
		},
	}, quickReplyAccountDeliveryEffects)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	return r.quickReplyMutationUpdates(ctx, userID, snapshot, []tg.UpdateClass{
		&tg.UpdateMessageID{ID: snapshot.Result.Message.ID, RandomID: req.RandomID},
	})
}

func quickReplyShortcutName(input tg.InputQuickReplyShortcutClass) (string, error) {
	switch shortcut := input.(type) {
	case *tg.InputQuickReplyShortcut:
		return shortcut.Shortcut, nil
	default:
		return "", shortcutInvalidErr()
	}
}

func (r *Router) quickReplyRandomIDs(n int) []int64 {
	base := r.clock.Now().UnixNano()
	if base == 0 {
		base = 1
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = base + int64(i)
		if out[i] == 0 {
			out[i] = int64(i + 1)
		}
	}
	return out
}

func (r *Router) quickReplyUsers(ctx context.Context, userID int64) []tg.UserClass {
	if r.deps.Users == nil || userID == 0 {
		return []tg.UserClass{}
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil || self.ID == 0 {
		return []tg.UserClass{}
	}
	return []tg.UserClass{r.tgSelfUserWithUsernames(ctx, self)}
}

func (r *Router) quickReplyMutationUpdates(ctx context.Context, userID int64, snapshot store.QuickReplyAccountMutationSnapshot, prefix []tg.UpdateClass) (*tg.Updates, error) {
	updates := append([]tg.UpdateClass(nil), prefix...)
	date := snapshot.Mutation.Date
	if snapshot.Changed {
		if len(snapshot.Effects) != 1 {
			return nil, internalErr()
		}
		event := snapshot.Effects[0].Event
		if update := tgOtherUpdateFromEvent(event); update != nil {
			updates = append(updates, update)
		}
		updates = appendAuxPtsBookkeeping(updates, event)
		date = event.Date
	}
	if date == 0 {
		date = int(r.clock.Now().Unix())
	}
	return &tg.Updates{Updates: updates, Users: r.quickReplyUsers(ctx, userID), Chats: []tg.ChatClass{}, Date: date, Seq: 0}, nil
}

func quickReplyAccountDeliveryEffects(snapshot store.QuickReplyAccountMutationSnapshot) ([]store.DeliveryEffect, error) {
	if !snapshot.Changed {
		return nil, nil
	}
	event := quickReplyEventFromMutation(snapshot.Mutation.UserID, snapshot.Result, snapshot.Mutation.Date)
	return []store.DeliveryEffect{store.AccountPTSDeliveryEffect(snapshot.Mutation.UserID, event, [8]byte{}, 0)}, nil
}

func quickReplyEventFromMutation(userID int64, mutation domain.QuickReplyMutation, date int) domain.UpdateEvent {
	event := domain.UpdateEvent{
		UserID:            userID,
		Date:              date,
		PtsCount:          1,
		QuickReplies:      append([]domain.QuickReply(nil), mutation.List.QuickReplies...),
		QuickReply:        mutation.QuickReply,
		QuickReplyMessage: mutation.Message,
		MaxID:             mutation.ShortcutID,
		MessageIDs:        append([]int(nil), mutation.MessageIDs...),
	}
	switch mutation.Kind {
	case domain.QuickReplyMutationNew:
		event.Type = domain.UpdateEventNewQuickReply
		event.QuickReply = mutation.QuickReply
	case domain.QuickReplyMutationDelete:
		event.Type = domain.UpdateEventDeleteQuickReply
	case domain.QuickReplyMutationMessage:
		event.Type = domain.UpdateEventQuickReplyMessage
		event.QuickReplyMessage = mutation.Message
	case domain.QuickReplyMutationIDs:
		event.Type = domain.UpdateEventDeleteQuickReplyMessages
	default:
		event.Type = domain.UpdateEventQuickReplies
		event.QuickReplies = append([]domain.QuickReply(nil), mutation.List.QuickReplies...)
	}
	if event.Date == 0 && mutation.Date != 0 {
		event.Date = mutation.Date
	}
	return event
}
