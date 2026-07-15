package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) accountBusinessAutomation() (AccountBusinessAutomationService, bool) {
	if r.deps.Account == nil {
		return nil, false
	}
	svc, ok := r.deps.Account.(AccountBusinessAutomationService)
	return svc, ok
}

func (r *Router) onAccountUpdateBusinessWorkHours(ctx context.Context, req *tg.AccountUpdateBusinessWorkHoursRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	hours, err := domainBusinessWorkHours(req)
	if err != nil {
		return false, err
	}
	if _, err := svc.UpdateBusinessWorkHours(ctx, userID, hours); err != nil {
		return false, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

func (r *Router) onAccountUpdateBusinessLocation(ctx context.Context, req *tg.AccountUpdateBusinessLocationRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	location, err := domainBusinessLocation(req)
	if err != nil {
		return false, err
	}
	if _, err := svc.UpdateBusinessLocation(ctx, userID, location); err != nil {
		return false, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

func (r *Router) onAccountUpdateBusinessIntro(ctx context.Context, req *tg.AccountUpdateBusinessIntroRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	intro, err := domainBusinessIntro(req)
	if err != nil {
		return false, err
	}
	if _, err := svc.UpdateBusinessIntro(ctx, userID, intro); err != nil {
		return false, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

func (r *Router) onAccountUpdateBusinessGreetingMessage(ctx context.Context, req *tg.AccountUpdateBusinessGreetingMessageRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	greeting, err := r.domainBusinessGreeting(ctx, userID, req)
	if err != nil {
		return false, err
	}
	if _, err := svc.UpdateBusinessGreetingMessage(ctx, userID, greeting); err != nil {
		return false, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

func (r *Router) onAccountUpdateBusinessAwayMessage(ctx context.Context, req *tg.AccountUpdateBusinessAwayMessageRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, premiumAccountRequiredErr()
	}
	away, err := r.domainBusinessAway(ctx, userID, req)
	if err != nil {
		return false, err
	}
	if _, err := svc.UpdateBusinessAwayMessage(ctx, userID, away); err != nil {
		return false, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForUser(userID)
	return true, nil
}

func (r *Router) onAccountGetBusinessChatLinks(ctx context.Context) (*tg.AccountBusinessChatLinks, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return &tg.AccountBusinessChatLinks{
			Links: []tg.BusinessChatLink{},
			Chats: []tg.ChatClass{},
			Users: []tg.UserClass{},
		}, nil
	}
	links, err := svc.ListBusinessChatLinks(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountBusinessChatLinks{
		Links: tgBusinessChatLinks(links),
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}, nil
}

func (r *Router) onAccountCreateBusinessChatLink(ctx context.Context, link tg.InputBusinessChatLink) (*tg.BusinessChatLink, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, premiumAccountRequiredErr()
	}
	input, err := domainBusinessChatLinkInput(link)
	if err != nil {
		return nil, err
	}
	created, err := svc.CreateBusinessChatLink(ctx, userID, input)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	out := tgBusinessChatLink(created)
	return &out, nil
}

func (r *Router) onAccountEditBusinessChatLink(ctx context.Context, req *tg.AccountEditBusinessChatLinkRequest) (*tg.BusinessChatLink, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || strings.TrimSpace(req.Slug) == "" {
		return nil, chatlinkSlugEmptyErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, premiumAccountRequiredErr()
	}
	input, err := domainBusinessChatLinkInput(req.Link)
	if err != nil {
		return nil, err
	}
	updated, err := svc.EditBusinessChatLink(ctx, userID, req.Slug, input)
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	out := tgBusinessChatLink(updated)
	return &out, nil
}

func (r *Router) onAccountDeleteBusinessChatLink(ctx context.Context, slug string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if strings.TrimSpace(slug) == "" {
		return false, chatlinkSlugEmptyErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, chatlinkSlugExpiredErr()
	}
	deleted, err := svc.DeleteBusinessChatLink(ctx, userID, slug)
	if err != nil {
		return false, businessAutomationErr(err)
	}
	if !deleted {
		return false, chatlinkSlugExpiredErr()
	}
	return true, nil
}

func (r *Router) onAccountResolveBusinessChatLink(ctx context.Context, slug string) (*tg.AccountResolvedBusinessChatLinks, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, chatlinkSlugEmptyErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, chatlinkSlugExpiredErr()
	}
	link, found, err := svc.ResolveBusinessChatLink(ctx, slug, true)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, chatlinkSlugExpiredErr()
	}
	viewerID, _, _ := r.currentUserID(ctx)
	users := []tg.UserClass{}
	if r.deps.Users != nil {
		user, found, err := r.deps.Users.ByID(ctx, viewerID, link.OwnerUserID)
		if err != nil {
			return nil, internalErr()
		}
		if found {
			if viewerID != 0 && user.ID == viewerID {
				users = append(users, r.tgSelfUser(user))
			} else {
				users = append(users, r.tgUser(user))
			}
		}
	}
	return &tg.AccountResolvedBusinessChatLinks{
		Peer:     &tg.PeerUser{UserID: link.OwnerUserID},
		Message:  link.Message,
		Entities: tgMessageEntities(link.Entities),
		Chats:    []tg.ChatClass{},
		Users:    users,
	}, nil
}

func (r *Router) onAccountGetConnectedBots(ctx context.Context) (*tg.AccountConnectedBots, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return &tg.AccountConnectedBots{ConnectedBots: []tg.ConnectedBot{}, Users: []tg.UserClass{}}, nil
	}
	bot, found, err := svc.GetConnectedBusinessBot(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || bot.BotUserID == 0 {
		return &tg.AccountConnectedBots{ConnectedBots: []tg.ConnectedBot{}, Users: []tg.UserClass{}}, nil
	}
	botUser, found, err := r.connectedBusinessBotByID(ctx, userID, bot.BotUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return &tg.AccountConnectedBots{ConnectedBots: []tg.ConnectedBot{}, Users: []tg.UserClass{}}, nil
	}
	return &tg.AccountConnectedBots{
		ConnectedBots: []tg.ConnectedBot{tgConnectedBot(bot)},
		Users:         []tg.UserClass{r.tgUser(botUser)},
	}, nil
}

func (r *Router) onAccountUpdateConnectedBot(ctx context.Context, req *tg.AccountUpdateConnectedBotRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return nil, premiumAccountRequiredErr()
	}
	botUser, found, err := r.connectedBusinessBotFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, botBusinessMissingErr()
	}
	if req.Deleted {
		if _, err := svc.DeleteConnectedBusinessBot(ctx, userID, botUser.ID); err != nil {
			return nil, businessAutomationErr(err)
		}
		r.invalidateRPCProjectionForViewer(userID)
		return r.connectedBusinessBotEmptyUpdates(botUser), nil
	}
	recipients, err := r.domainBusinessBotRecipients(ctx, userID, req.Recipients)
	if err != nil {
		return nil, err
	}
	saved, err := svc.SaveConnectedBusinessBot(ctx, userID, domain.ConnectedBusinessBot{
		BotUserID:  botUser.ID,
		Recipients: recipients,
		Rights:     domainBusinessBotRightsForUpdate(req),
	})
	if err != nil {
		return nil, businessAutomationErr(err)
	}
	r.invalidateRPCProjectionForViewer(userID)
	return r.connectedBusinessBotEmptyUpdates(botUser, tgConnectedBot(saved)), nil
}

func (r *Router) onAccountToggleConnectedBotPaused(ctx context.Context, req *tg.AccountToggleConnectedBotPausedRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == userID {
		return false, peerIDInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, botBusinessMissingErr()
	}
	if _, err := svc.SetConnectedBusinessBotPaused(ctx, userID, peer.ID, req.Paused); err != nil {
		return false, businessAutomationErr(err)
	}
	settings, err := r.connectedBusinessBotPeerSettings(ctx, userID, peer, domain.PeerSettings{})
	if err != nil {
		return false, err
	}
	if err := r.recordConnectedBusinessPeerSettings(ctx, userID, peer, settings); err != nil {
		return false, internalErr()
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	return true, nil
}

func (r *Router) onAccountDisablePeerConnectedBot(ctx context.Context, input tg.InputPeerClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == userID {
		return false, peerIDInvalidErr()
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return false, botBusinessMissingErr()
	}
	if _, err := svc.DisableConnectedBusinessBotForPeer(ctx, userID, peer.ID); err != nil {
		return false, businessAutomationErr(err)
	}
	if err := r.recordConnectedBusinessPeerSettings(ctx, userID, peer, domain.PeerSettings{}); err != nil {
		return false, internalErr()
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	return true, nil
}

func (r *Router) connectedBusinessBotByID(ctx context.Context, currentUserID, botUserID int64) (domain.User, bool, error) {
	if r.deps.Users == nil || botUserID == 0 {
		return domain.User{}, false, nil
	}
	u, found, err := r.deps.Users.ByID(ctx, currentUserID, botUserID)
	if err != nil {
		return domain.User{}, false, internalErr()
	}
	if !found || !connectedBusinessBotUsable(u) {
		return domain.User{}, false, nil
	}
	return u, true, nil
}

func (r *Router) connectedBusinessBotFromInput(ctx context.Context, currentUserID int64, input tg.InputUserClass) (domain.User, bool, error) {
	if r.deps.Users == nil {
		return domain.User{}, false, internalErr()
	}
	u, found, err := r.userFromInput(ctx, currentUserID, input)
	if err != nil {
		return domain.User{}, false, internalErr()
	}
	if !found || !connectedBusinessBotUsable(u) {
		return domain.User{}, false, nil
	}
	return u, true, nil
}

func connectedBusinessBotUsable(u domain.User) bool {
	return u.Bot && u.ID != 0 && u.ID != domain.BotFatherUserID
}

func (r *Router) connectedBusinessBotEmptyUpdates(botUser domain.User, bots ...tg.ConnectedBot) *tg.Updates {
	users := []tg.UserClass{}
	if botUser.ID != 0 {
		users = append(users, r.tgUser(botUser))
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{},
		Users:   users,
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
	}
}

func (r *Router) connectedBusinessBotPeerSettings(ctx context.Context, ownerUserID int64, peer domain.Peer, settings domain.PeerSettings) (domain.PeerSettings, error) {
	if peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == ownerUserID {
		return settings, nil
	}
	svc, ok := r.accountBusinessAutomation()
	if !ok {
		return settings, nil
	}
	bot, found, err := svc.GetConnectedBusinessBot(ctx, ownerUserID)
	if err != nil {
		return domain.PeerSettings{}, internalErr()
	}
	if !found || bot.BotUserID == 0 {
		return settings, nil
	}
	state, stateFound, err := svc.GetConnectedBusinessBotPeerState(ctx, ownerUserID, peer.ID)
	if err != nil {
		return domain.PeerSettings{}, internalErr()
	}
	if stateFound && state.Disabled {
		return settings, nil
	}
	existingChat, isContact := r.connectedBusinessPeerFacts(ctx, ownerUserID, peer.ID)
	if !domain.BusinessBotRecipientsMatch(bot.Recipients, existingChat, isContact, peer.ID) {
		return settings, nil
	}
	settings.BusinessBotID = bot.BotUserID
	settings.BusinessBotPaused = stateFound && state.Paused
	settings.BusinessBotCanReply = bot.Rights.Reply && !settings.BusinessBotPaused
	if botUser, found, err := r.connectedBusinessBotByID(ctx, ownerUserID, bot.BotUserID); err != nil {
		return domain.PeerSettings{}, err
	} else if found {
		settings.BusinessBotManageURL = r.connectedBusinessBotManageURL(botUser)
	}
	if settings.BusinessBotManageURL == "" {
		settings.BusinessBotManageURL = "telesrv://business-bot"
	}
	return settings, nil
}

func (r *Router) connectedBusinessPeerFacts(ctx context.Context, ownerUserID, peerUserID int64) (existingChat, isContact bool) {
	if r.deps.Dialogs != nil {
		list, err := r.deps.Dialogs.GetPeerDialogs(ctx, ownerUserID, []domain.Peer{{Type: domain.PeerTypeUser, ID: peerUserID}})
		if err == nil && len(list.Dialogs) > 0 && list.Dialogs[0].TopMessage > 0 {
			existingChat = true
		}
	}
	if r.deps.Contacts != nil {
		settings, err := r.deps.Contacts.GetPeerSettings(ctx, ownerUserID, domain.Peer{Type: domain.PeerTypeUser, ID: peerUserID})
		isContact = err == nil && !settings.AddContact
	}
	return existingChat, isContact
}

func (r *Router) connectedBusinessBotManageURL(bot domain.User) string {
	if bot.Username == "" {
		return ""
	}
	return r.publicLink(bot.Username)
}

func (r *Router) recordConnectedBusinessPeerSettings(ctx context.Context, userID int64, peer domain.Peer, settings domain.PeerSettings) error {
	if r.deps.Updates == nil || userID == 0 {
		return nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	event, _, err := r.deps.Updates.RecordPeerSettings(ctx, authKeyID, userID, peer, settings, rawAuthKeyIDForOrigin(ctx), sessionID)
	if err != nil {
		return err
	}
	if sessionID != 0 {
		r.bookkeepAuxPtsForCurrentSession(ctx, event)
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, tgUpdateForOutboxEvent(event))
	return nil
}

func premiumAccountRequiredErr() error { return tgerr400("PREMIUM_ACCOUNT_REQUIRED") }

func botBusinessMissingErr() error { return tgerr400("BOT_BUSINESS_MISSING") }

func botNotConnectedYetErr() error { return tgerr400("BOT_NOT_CONNECTED_YET") }

func botAlreadyDisabledErr() error { return tgerr400("BOT_ALREADY_DISABLED") }

func chatlinkSlugEmptyErr() error { return tgerr400("CHATLINK_SLUG_EMPTY") }

func chatlinkSlugExpiredErr() error { return tgerr400("CHATLINK_SLUG_EXPIRED") }

func businessAutomationErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPremiumRequired):
		return premiumAccountRequiredErr()
	case errors.Is(err, domain.ErrBusinessProfileInvalid):
		return inputRequestInvalidErr()
	case errors.Is(err, domain.ErrBusinessRecipientsEmpty):
		return tgerr400("BUSINESS_RECIPIENTS_EMPTY")
	case errors.Is(err, domain.ErrBotBusinessMissing):
		return botBusinessMissingErr()
	case errors.Is(err, domain.ErrBotNotConnectedYet):
		return botNotConnectedYetErr()
	case errors.Is(err, domain.ErrBotAlreadyDisabled):
		return botAlreadyDisabledErr()
	case errors.Is(err, domain.ErrBusinessChatLinkInvalid):
		return inputRequestInvalidErr()
	case errors.Is(err, domain.ErrBusinessChatLinkNotFound):
		return chatlinkSlugExpiredErr()
	case errors.Is(err, domain.ErrBusinessChatLinksTooMuch):
		return tgerr400("CHATLINKS_TOO_MUCH")
	case errors.Is(err, domain.ErrShortcutInvalid):
		return shortcutInvalidErr()
	case errors.Is(err, domain.ErrShortcutOccupied):
		return tgerr400("SHORTCUT_OCCUPIED")
	case errors.Is(err, domain.ErrQuickRepliesTooMuch):
		return tgerr400("SHORTCUTS_TOO_MUCH")
	default:
		return internalErr()
	}
}

func (r *Router) applyBusinessProfileToUserFull(ctx context.Context, full *tg.UserFull, profile domain.BusinessProfile) {
	if full == nil {
		return
	}
	if hours, ok := tgBusinessWorkHours(profile.WorkHours); ok {
		full.SetBusinessWorkHours(hours)
	}
	if location, ok := tgBusinessLocation(profile.Location); ok {
		full.SetBusinessLocation(location)
	}
	if intro, ok := r.tgBusinessIntro(ctx, profile.Intro); ok {
		full.SetBusinessIntro(intro)
	}
	if greeting, ok := tgBusinessGreeting(profile.Greeting); ok {
		full.SetBusinessGreetingMessage(greeting)
	}
	if away, ok := tgBusinessAway(profile.Away); ok {
		full.SetBusinessAwayMessage(away)
	}
}
