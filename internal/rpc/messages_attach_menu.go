package rpc

import (
	"context"
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesGetAttachMenuBots(ctx context.Context, hash int64) (tg.AttachMenuBotsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 || r.deps.Bots == nil {
		return &tg.AttachMenuBots{Hash: 0, Bots: []tg.AttachMenuBot{}, Users: []tg.UserClass{}}, nil
	}
	items, err := r.deps.Bots.ListAttachMenuBots(ctx)
	if err != nil {
		return nil, internalErr()
	}
	stateByBot := make(map[int64]domain.BotAttachMenuState, len(items))
	for _, item := range items {
		if state, found, err := r.deps.Bots.GetAttachMenuState(ctx, userID, item.BotUserID); err == nil && found {
			stateByBot[item.BotUserID] = state
		} else if err != nil {
			return nil, internalErr()
		}
	}
	outHash := attachMenuBotsHash(userID, items, stateByBot)
	if hash != 0 && hash == outHash {
		return &tg.AttachMenuBotsNotModified{}, nil
	}
	bots := make([]tg.AttachMenuBot, 0, len(items))
	userIDs := make([]int64, 0, len(items))
	for _, item := range items {
		state := stateByBot[item.BotUserID]
		bots = append(bots, r.tgAttachMenuBot(ctx, item, state))
		userIDs = append(userIDs, item.BotUserID)
	}
	return &tg.AttachMenuBots{
		Hash:  outHash,
		Bots:  bots,
		Users: r.attachMenuUsers(ctx, userID, userIDs, stateByBot),
	}, nil
}

func (r *Router) onMessagesGetAttachMenuBot(ctx context.Context, bot tg.InputUserClass) (*tg.AttachMenuBotsBot, error) {
	userID, target, err := r.attachMenuBotTarget(ctx, bot)
	if err != nil {
		return nil, err
	}
	item, found, err := r.deps.Bots.GetAttachMenuBot(ctx, target.ID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, botInvalidErr()
	}
	var state domain.BotAttachMenuState
	if st, found, err := r.deps.Bots.GetAttachMenuState(ctx, userID, target.ID); err == nil && found {
		state = st
	} else if err != nil {
		return nil, internalErr()
	}
	stateByBot := map[int64]domain.BotAttachMenuState{target.ID: state}
	return &tg.AttachMenuBotsBot{
		Bot:   r.tgAttachMenuBot(ctx, item, state),
		Users: r.attachMenuUsers(ctx, userID, []int64{target.ID}, stateByBot),
	}, nil
}

func (r *Router) onMessagesToggleBotInAttachMenu(ctx context.Context, req *tg.MessagesToggleBotInAttachMenuRequest) (bool, error) {
	if req == nil {
		return false, botInvalidErr()
	}
	userID, target, err := r.attachMenuBotTarget(ctx, req.Bot)
	if err != nil {
		return false, err
	}
	if _, found, err := r.deps.Bots.GetAttachMenuBot(ctx, target.ID); err != nil {
		return false, internalErr()
	} else if !found {
		return false, botInvalidErr()
	}
	prev, found, err := r.deps.Bots.GetAttachMenuState(ctx, userID, target.ID)
	if err != nil {
		return false, internalErr()
	}
	next := domain.BotAttachMenuState{
		UserID:       userID,
		BotUserID:    target.ID,
		Enabled:      req.Enabled,
		WriteAllowed: prev.WriteAllowed || req.WriteAllowed,
	}
	if _, err := r.deps.Bots.SetAttachMenuState(ctx, next); err != nil {
		return false, botInvalidErr()
	}
	if req.WriteAllowed && !prev.WriteAllowed {
		if _, err := r.deps.Bots.AllowSendMessage(ctx, userID, target.ID, true); err != nil {
			return false, botInvalidErr()
		}
		res, err := r.sendBotAllowedServiceMessageWith(ctx, userID, target.ID, domain.MessageBotAllowedAction{AttachMenu: true})
		if err != nil {
			return false, internalErr()
		}
		if !res.Duplicate {
			senderUsers := r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
			senderChats := r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
			r.pushUserMessage(ctx, userID, "push attach menu bot allowed", tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, 0, false, senderUsers, senderChats))
		}
	}
	if !found || prev.Enabled != next.Enabled || prev.WriteAllowed != next.WriteAllowed {
		r.pushUserMessageTransient(ctx, userID, "push attach menu bots changed", &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateAttachMenuBots{}},
			Date:    int(r.clock.Now().Unix()),
		})
	}
	return true, nil
}

func (r *Router) attachMenuBotTarget(ctx context.Context, bot tg.InputUserClass) (int64, domain.User, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.User{}, internalErr()
	}
	if userID == 0 || r.deps.Bots == nil || bot == nil {
		return 0, domain.User{}, botInvalidErr()
	}
	u, found, err := r.userFromInput(ctx, userID, bot)
	if err != nil {
		return 0, domain.User{}, internalErr()
	}
	if !found || !u.Bot {
		return 0, domain.User{}, botInvalidErr()
	}
	return userID, u, nil
}

func (r *Router) tgAttachMenuBot(ctx context.Context, item domain.BotAttachMenuBot, state domain.BotAttachMenuState) tg.AttachMenuBot {
	out := tg.AttachMenuBot{
		Inactive:                 item.Inactive || !state.Enabled,
		HasSettings:              item.HasSettings,
		RequestWriteAccess:       item.RequestWriteAccess,
		ShowInAttachMenu:         item.ShowInAttachMenu,
		ShowInSideMenu:           item.ShowInSideMenu,
		SideMenuDisclaimerNeeded: item.SideMenuDisclaimerNeeded,
		BotID:                    item.BotUserID,
		ShortName:                item.ShortName,
		PeerTypes:                tgAttachMenuPeerTypes(item.PeerTypes),
		Icons:                    r.tgAttachMenuIcons(ctx, item.Icons),
	}
	if len(out.PeerTypes) == 0 {
		out.PeerTypes = []tg.AttachMenuPeerTypeClass{&tg.AttachMenuPeerTypePM{}, &tg.AttachMenuPeerTypeChat{}, &tg.AttachMenuPeerTypeBroadcast{}}
	}
	return out
}

func (r *Router) tgAttachMenuIcons(ctx context.Context, icons []domain.BotAttachMenuIcon) []tg.AttachMenuBotIcon {
	if len(icons) == 0 {
		return []tg.AttachMenuBotIcon{}
	}
	out := make([]tg.AttachMenuBotIcon, 0, len(icons))
	for _, icon := range icons {
		if icon.Name == "" || icon.DocumentID == 0 || r.deps.Files == nil {
			continue
		}
		doc, found, err := r.deps.Files.GetDocument(ctx, icon.DocumentID)
		if err != nil || !found {
			continue
		}
		tgIcon := tg.AttachMenuBotIcon{Name: icon.Name, Icon: tgDocument(doc)}
		if len(icon.Colors) > 0 {
			colors := make([]tg.AttachMenuBotIconColor, 0, len(icon.Colors))
			for _, color := range icon.Colors {
				if color.Name != "" {
					colors = append(colors, tg.AttachMenuBotIconColor{Name: color.Name, Color: color.Color})
				}
			}
			if len(colors) > 0 {
				tgIcon.SetColors(colors)
			}
		}
		out = append(out, tgIcon)
	}
	return out
}

func tgAttachMenuPeerTypes(in []string) []tg.AttachMenuPeerTypeClass {
	out := make([]tg.AttachMenuPeerTypeClass, 0, len(in))
	seen := map[string]bool{}
	for _, typ := range in {
		if seen[typ] {
			continue
		}
		seen[typ] = true
		switch typ {
		case "same_bot_pm":
			out = append(out, &tg.AttachMenuPeerTypeSameBotPM{})
		case "bot_pm":
			out = append(out, &tg.AttachMenuPeerTypeBotPM{})
		case "pm":
			out = append(out, &tg.AttachMenuPeerTypePM{})
		case "chat", "megagroup":
			out = append(out, &tg.AttachMenuPeerTypeChat{})
		case "broadcast", "channel":
			out = append(out, &tg.AttachMenuPeerTypeBroadcast{})
		}
	}
	return out
}

func (r *Router) attachMenuUsers(ctx context.Context, viewerID int64, ids []int64, states map[int64]domain.BotAttachMenuState) []tg.UserClass {
	if len(ids) == 0 || r.deps.Users == nil {
		return []tg.UserClass{}
	}
	users, err := r.deps.Users.ByIDs(ctx, viewerID, ids)
	if err != nil {
		return []tg.UserClass{}
	}
	out := make([]tg.UserClass, 0, len(users))
	for _, user := range users {
		tgUser := r.withBotProfileFlags(ctx, r.tgUser(user))
		if state, ok := states[user.ID]; ok && state.Enabled {
			tgUser.SetAttachMenuEnabled(true)
		}
		out = append(out, tgUser)
	}
	return out
}

func attachMenuBotsHash(userID int64, items []domain.BotAttachMenuBot, states map[int64]domain.BotAttachMenuState) int64 {
	if len(items) == 0 {
		return 0
	}
	parts := []string{"attach-menu", strconv.FormatInt(userID, 10)}
	for _, item := range items {
		state := states[item.BotUserID]
		parts = append(parts,
			strconv.FormatInt(item.BotUserID, 10),
			item.ShortName,
			strconv.FormatBool(item.Inactive),
			strconv.FormatBool(item.HasSettings),
			strconv.FormatBool(item.RequestWriteAccess),
			strconv.FormatBool(item.ShowInAttachMenu),
			strconv.FormatBool(item.ShowInSideMenu),
			strconv.FormatBool(item.SideMenuDisclaimerNeeded),
			strconv.FormatBool(state.Enabled),
			strconv.FormatBool(state.WriteAllowed),
		)
		for _, typ := range item.PeerTypes {
			parts = append(parts, typ)
		}
		for _, icon := range item.Icons {
			parts = append(parts, icon.Name, strconv.FormatInt(icon.DocumentID, 10))
		}
	}
	return stableBotAppInt64(parts...)
}
