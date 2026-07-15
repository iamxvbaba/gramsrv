package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func domainBusinessWorkHours(req *tg.AccountUpdateBusinessWorkHoursRequest) (*domain.BusinessWorkHours, error) {
	if req == nil {
		return nil, nil
	}
	hours, ok := req.GetBusinessWorkHours()
	if !ok {
		return nil, nil
	}
	out := &domain.BusinessWorkHours{
		TimezoneID: hours.TimezoneID,
		WeeklyOpen: make([]domain.BusinessWeeklyOpen, 0, len(hours.WeeklyOpen)),
	}
	for _, item := range hours.WeeklyOpen {
		out.WeeklyOpen = append(out.WeeklyOpen, domain.BusinessWeeklyOpen{
			StartMinute: item.StartMinute,
			EndMinute:   item.EndMinute,
		})
	}
	return out, nil
}

func tgBusinessWorkHours(in *domain.BusinessWorkHours) (tg.BusinessWorkHours, bool) {
	if in == nil {
		return tg.BusinessWorkHours{}, false
	}
	out := tg.BusinessWorkHours{
		TimezoneID: in.TimezoneID,
		WeeklyOpen: make([]tg.BusinessWeeklyOpen, 0, len(in.WeeklyOpen)),
	}
	for _, item := range in.WeeklyOpen {
		out.WeeklyOpen = append(out.WeeklyOpen, tg.BusinessWeeklyOpen{
			StartMinute: item.StartMinute,
			EndMinute:   item.EndMinute,
		})
	}
	if in.OpenNow {
		out.SetOpenNow(true)
	}
	return out, true
}

func domainBusinessLocation(req *tg.AccountUpdateBusinessLocationRequest) (*domain.BusinessLocation, error) {
	if req == nil {
		return nil, nil
	}
	address, addressSet := req.GetAddress()
	geo, geoSet := req.GetGeoPoint()
	if !addressSet && !geoSet {
		return nil, nil
	}
	out := &domain.BusinessLocation{Address: address}
	if geoSet {
		switch point := geo.(type) {
		case *tg.InputGeoPoint:
			out.Geo = &domain.GeoPoint{Lat: point.Lat, Long: point.Long}
		case *tg.InputGeoPointEmpty, nil:
		default:
			return nil, inputConstructorInvalidErr()
		}
	}
	return out, nil
}

func tgBusinessLocation(in *domain.BusinessLocation) (tg.BusinessLocation, bool) {
	if in == nil {
		return tg.BusinessLocation{}, false
	}
	out := tg.BusinessLocation{Address: in.Address}
	if in.Geo != nil {
		out.SetGeoPoint(&tg.GeoPoint{Lat: in.Geo.Lat, Long: in.Geo.Long, AccessHash: 1})
	}
	return out, true
}

func domainBusinessIntro(req *tg.AccountUpdateBusinessIntroRequest) (*domain.BusinessIntro, error) {
	if req == nil {
		return nil, nil
	}
	intro, ok := req.GetIntro()
	if !ok {
		return nil, nil
	}
	out := &domain.BusinessIntro{
		Title:       intro.Title,
		Description: intro.Description,
	}
	if sticker, ok := intro.GetSticker(); ok {
		doc, ok := sticker.(*tg.InputDocument)
		if !ok || doc.ID == 0 {
			return nil, documentInvalidErr()
		}
		out.StickerDocumentID = doc.ID
	}
	return out, nil
}

func (r *Router) tgBusinessIntro(ctx context.Context, in *domain.BusinessIntro) (tg.BusinessIntro, bool) {
	if in == nil {
		return tg.BusinessIntro{}, false
	}
	out := tg.BusinessIntro{Title: in.Title, Description: in.Description}
	if in.StickerDocumentID != 0 && r.deps.Files != nil {
		if doc, found, err := r.deps.Files.GetDocument(ctx, in.StickerDocumentID); err == nil && found {
			out.SetSticker(tgDocument(doc))
		}
	}
	return out, true
}

func (r *Router) domainBusinessGreeting(ctx context.Context, currentUserID int64, req *tg.AccountUpdateBusinessGreetingMessageRequest) (*domain.BusinessGreetingMessage, error) {
	if req == nil {
		return nil, nil
	}
	message, ok := req.GetMessage()
	if !ok {
		return nil, nil
	}
	recipients, err := r.domainBusinessRecipients(ctx, currentUserID, message.Recipients)
	if err != nil {
		return nil, err
	}
	return &domain.BusinessGreetingMessage{
		ShortcutID:     message.ShortcutID,
		Recipients:     recipients,
		NoActivityDays: message.NoActivityDays,
	}, nil
}

func tgBusinessGreeting(in *domain.BusinessGreetingMessage) (tg.BusinessGreetingMessage, bool) {
	if in == nil {
		return tg.BusinessGreetingMessage{}, false
	}
	return tg.BusinessGreetingMessage{
		ShortcutID:     in.ShortcutID,
		Recipients:     tgBusinessRecipients(in.Recipients),
		NoActivityDays: in.NoActivityDays,
	}, true
}

func (r *Router) domainBusinessAway(ctx context.Context, currentUserID int64, req *tg.AccountUpdateBusinessAwayMessageRequest) (*domain.BusinessAwayMessage, error) {
	if req == nil {
		return nil, nil
	}
	message, ok := req.GetMessage()
	if !ok {
		return nil, nil
	}
	schedule, err := domainBusinessAwaySchedule(message.Schedule)
	if err != nil {
		return nil, err
	}
	recipients, err := r.domainBusinessRecipients(ctx, currentUserID, message.Recipients)
	if err != nil {
		return nil, err
	}
	return &domain.BusinessAwayMessage{
		ShortcutID:  message.ShortcutID,
		Schedule:    schedule,
		Recipients:  recipients,
		OfflineOnly: message.OfflineOnly,
	}, nil
}

func tgBusinessAway(in *domain.BusinessAwayMessage) (tg.BusinessAwayMessage, bool) {
	if in == nil {
		return tg.BusinessAwayMessage{}, false
	}
	out := tg.BusinessAwayMessage{
		ShortcutID: in.ShortcutID,
		Schedule:   tgBusinessAwaySchedule(in.Schedule),
		Recipients: tgBusinessRecipients(in.Recipients),
	}
	if in.OfflineOnly {
		out.SetOfflineOnly(true)
	}
	return out, true
}

func (r *Router) domainBusinessRecipients(ctx context.Context, currentUserID int64, in tg.InputBusinessRecipients) (domain.BusinessRecipients, error) {
	out := domain.BusinessRecipients{
		ExistingChats:   in.ExistingChats,
		NewChats:        in.NewChats,
		Contacts:        in.Contacts,
		NonContacts:     in.NonContacts,
		ExcludeSelected: in.ExcludeSelected,
	}
	if len(in.Users) == 0 {
		return out, nil
	}
	seen := make(map[int64]struct{}, len(in.Users))
	for _, input := range in.Users {
		user, found, err := r.userFromInput(ctx, currentUserID, input)
		if err != nil {
			return domain.BusinessRecipients{}, internalErr()
		}
		if !found || user.ID == 0 {
			return domain.BusinessRecipients{}, userIDInvalidErr()
		}
		if _, ok := seen[user.ID]; ok {
			continue
		}
		seen[user.ID] = struct{}{}
		out.Users = append(out.Users, user.ID)
	}
	return out, nil
}

func tgBusinessRecipients(in domain.BusinessRecipients) tg.BusinessRecipients {
	out := tg.BusinessRecipients{
		ExistingChats:   in.ExistingChats,
		NewChats:        in.NewChats,
		Contacts:        in.Contacts,
		NonContacts:     in.NonContacts,
		ExcludeSelected: in.ExcludeSelected,
	}
	if len(in.Users) > 0 {
		out.SetUsers(append([]int64(nil), in.Users...))
	}
	return out
}

func (r *Router) domainBusinessBotRecipients(ctx context.Context, currentUserID int64, in tg.InputBusinessBotRecipients) (domain.BusinessBotRecipients, error) {
	users, err := r.businessBotRecipientUserIDs(ctx, currentUserID, in.Users)
	if err != nil {
		return domain.BusinessBotRecipients{}, err
	}
	excludeUsers, err := r.businessBotRecipientUserIDs(ctx, currentUserID, in.ExcludeUsers)
	if err != nil {
		return domain.BusinessBotRecipients{}, err
	}
	return domain.BusinessBotRecipients{
		ExistingChats:   in.ExistingChats,
		NewChats:        in.NewChats,
		Contacts:        in.Contacts,
		NonContacts:     in.NonContacts,
		ExcludeSelected: in.ExcludeSelected,
		Users:           users,
		ExcludeUsers:    excludeUsers,
	}, nil
}

func (r *Router) businessBotRecipientUserIDs(ctx context.Context, currentUserID int64, inputs []tg.InputUserClass) ([]int64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(inputs))
	seen := make(map[int64]struct{}, len(inputs))
	for _, input := range inputs {
		user, found, err := r.userFromInput(ctx, currentUserID, input)
		if err != nil {
			return nil, internalErr()
		}
		if !found || user.ID == 0 {
			return nil, userIDInvalidErr()
		}
		if _, ok := seen[user.ID]; ok {
			continue
		}
		seen[user.ID] = struct{}{}
		out = append(out, user.ID)
	}
	return out, nil
}

func tgBusinessBotRecipients(in domain.BusinessBotRecipients) tg.BusinessBotRecipients {
	out := tg.BusinessBotRecipients{
		ExistingChats:   in.ExistingChats,
		NewChats:        in.NewChats,
		Contacts:        in.Contacts,
		NonContacts:     in.NonContacts,
		ExcludeSelected: in.ExcludeSelected,
	}
	if len(in.Users) > 0 {
		out.SetUsers(append([]int64(nil), in.Users...))
	}
	if len(in.ExcludeUsers) > 0 {
		out.SetExcludeUsers(append([]int64(nil), in.ExcludeUsers...))
	}
	return out
}

func domainBusinessBotRights(in tg.BusinessBotRights) domain.BusinessBotRights {
	return domain.BusinessBotRights{
		Reply:                   in.Reply,
		ReadMessages:            in.ReadMessages,
		DeleteSentMessages:      in.DeleteSentMessages,
		DeleteReceivedMessages:  in.DeleteReceivedMessages,
		EditName:                in.EditName,
		EditBio:                 in.EditBio,
		EditProfilePhoto:        in.EditProfilePhoto,
		EditUsername:            in.EditUsername,
		ViewGifts:               in.ViewGifts,
		SellGifts:               in.SellGifts,
		ChangeGiftSettings:      in.ChangeGiftSettings,
		TransferAndUpgradeGifts: in.TransferAndUpgradeGifts,
		TransferStars:           in.TransferStars,
		ManageStories:           in.ManageStories,
	}
}

func domainBusinessBotRightsForUpdate(req *tg.AccountUpdateConnectedBotRequest) domain.BusinessBotRights {
	if rights, ok := req.GetRights(); ok {
		return domainBusinessBotRights(rights)
	}
	return domain.BusinessBotRights{Reply: true}
}

func tgBusinessBotRights(in domain.BusinessBotRights) tg.BusinessBotRights {
	return tg.BusinessBotRights{
		Reply:                   in.Reply,
		ReadMessages:            in.ReadMessages,
		DeleteSentMessages:      in.DeleteSentMessages,
		DeleteReceivedMessages:  in.DeleteReceivedMessages,
		EditName:                in.EditName,
		EditBio:                 in.EditBio,
		EditProfilePhoto:        in.EditProfilePhoto,
		EditUsername:            in.EditUsername,
		ViewGifts:               in.ViewGifts,
		SellGifts:               in.SellGifts,
		ChangeGiftSettings:      in.ChangeGiftSettings,
		TransferAndUpgradeGifts: in.TransferAndUpgradeGifts,
		TransferStars:           in.TransferStars,
		ManageStories:           in.ManageStories,
	}
}

func tgConnectedBot(in domain.ConnectedBusinessBot) tg.ConnectedBot {
	return tg.ConnectedBot{
		BotID:      in.BotUserID,
		Recipients: tgBusinessBotRecipients(in.Recipients),
		Rights:     tgBusinessBotRights(in.Rights),
	}
}

func domainBusinessAwaySchedule(in tg.BusinessAwayMessageScheduleClass) (domain.BusinessAwaySchedule, error) {
	switch schedule := in.(type) {
	case *tg.BusinessAwayMessageScheduleAlways:
		return domain.BusinessAwaySchedule{Kind: domain.BusinessAwayScheduleAlways}, nil
	case *tg.BusinessAwayMessageScheduleOutsideWorkHours:
		return domain.BusinessAwaySchedule{Kind: domain.BusinessAwayScheduleOutsideWorkHours}, nil
	case *tg.BusinessAwayMessageScheduleCustom:
		return domain.BusinessAwaySchedule{
			Kind:      domain.BusinessAwayScheduleCustom,
			StartDate: schedule.StartDate,
			EndDate:   schedule.EndDate,
		}, nil
	default:
		return domain.BusinessAwaySchedule{}, inputConstructorInvalidErr()
	}
}

func tgBusinessAwaySchedule(in domain.BusinessAwaySchedule) tg.BusinessAwayMessageScheduleClass {
	switch in.Kind {
	case domain.BusinessAwayScheduleOutsideWorkHours:
		return &tg.BusinessAwayMessageScheduleOutsideWorkHours{}
	case domain.BusinessAwayScheduleCustom:
		return &tg.BusinessAwayMessageScheduleCustom{StartDate: in.StartDate, EndDate: in.EndDate}
	default:
		return &tg.BusinessAwayMessageScheduleAlways{}
	}
}

func domainBusinessChatLinkInput(in tg.InputBusinessChatLink) (domain.BusinessChatLinkInput, error) {
	entities, _ := in.GetEntities()
	title, _ := in.GetTitle()
	return domain.BusinessChatLinkInput{
		Message:  in.Message,
		Entities: domainMessageEntities(entities),
		Title:    title,
	}, nil
}

func tgBusinessChatLink(in domain.BusinessChatLink) tg.BusinessChatLink {
	return tg.BusinessChatLink{
		Link:     in.Link,
		Message:  in.Message,
		Entities: tgMessageEntities(in.Entities),
		Title:    in.Title,
		Views:    in.Views,
	}
}

func tgBusinessChatLinks(in []domain.BusinessChatLink) []tg.BusinessChatLink {
	out := make([]tg.BusinessChatLink, 0, len(in))
	for _, item := range in {
		out = append(out, tgBusinessChatLink(item))
	}
	return out
}

func tgQuickReply(in domain.QuickReply) tg.QuickReply {
	return tg.QuickReply{
		ShortcutID: in.ID,
		Shortcut:   in.Shortcut,
		TopMessage: in.TopMessage,
		Count:      in.Count,
	}
}

func tgQuickReplies(in []domain.QuickReply) []tg.QuickReply {
	out := make([]tg.QuickReply, 0, len(in))
	for _, item := range in {
		out = append(out, tgQuickReply(item))
	}
	return out
}

func tgQuickReplyMessage(in domain.QuickReplyMessage) tg.MessageClass {
	if in.ID <= 0 {
		return nil
	}
	out := &tg.Message{
		Out:      true,
		ID:       in.ID,
		FromID:   &tg.PeerUser{UserID: in.OwnerUserID},
		PeerID:   &tg.PeerUser{UserID: in.OwnerUserID},
		Date:     in.Date,
		Message:  in.Message,
		Entities: tgMessageEntities(in.Entities),
	}
	out.SetQuickReplyShortcutID(in.ShortcutID)
	return out
}

func tgQuickReplyMessages(in []domain.QuickReplyMessage) []tg.MessageClass {
	out := make([]tg.MessageClass, 0, len(in))
	for _, item := range in {
		if msg := tgQuickReplyMessage(item); msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

func tgMessagesQuickReplies(in domain.QuickReplyList) *tg.MessagesQuickReplies {
	return &tg.MessagesQuickReplies{
		QuickReplies: tgQuickReplies(in.QuickReplies),
		Messages:     tgQuickReplyMessages(in.Messages),
		Chats:        []tg.ChatClass{},
		Users:        []tg.UserClass{},
	}
}

func tgMessagesQuickReplyMessages(in domain.QuickReplyMessages) *tg.MessagesMessages {
	return &tg.MessagesMessages{
		Messages: tgQuickReplyMessages(in.Messages),
		Chats:    []tg.ChatClass{},
		Users:    []tg.UserClass{},
	}
}
