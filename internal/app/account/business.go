package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

const (
	maxBusinessLocationAddress = 96
	maxBusinessIntroTitle      = 64
	maxBusinessIntroDesc       = 160
)

func (s *Service) GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.BusinessProfile{UserID: userID}, false, nil
	}
	return s.business.GetBusinessProfile(ctx, userID)
}

func (s *Service) UpdateBusinessWorkHours(ctx context.Context, userID int64, hours *domain.BusinessWorkHours) (domain.BusinessProfile, error) {
	profile, err := s.businessProfileForUpdate(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	normalized, err := normalizeBusinessWorkHours(hours)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.WorkHours = normalized
	return s.saveBusinessProfile(ctx, profile)
}

func (s *Service) UpdateBusinessLocation(ctx context.Context, userID int64, location *domain.BusinessLocation) (domain.BusinessProfile, error) {
	profile, err := s.businessProfileForUpdate(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	normalized, err := normalizeBusinessLocation(location)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.Location = normalized
	return s.saveBusinessProfile(ctx, profile)
}

func (s *Service) UpdateBusinessIntro(ctx context.Context, userID int64, intro *domain.BusinessIntro) (domain.BusinessProfile, error) {
	profile, err := s.businessProfileForUpdate(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	normalized, err := normalizeBusinessIntro(intro)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.Intro = normalized
	return s.saveBusinessProfile(ctx, profile)
}

func (s *Service) UpdateBusinessGreetingMessage(ctx context.Context, userID int64, greeting *domain.BusinessGreetingMessage) (domain.BusinessProfile, error) {
	profile, err := s.businessProfileForUpdate(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	normalized, err := normalizeBusinessGreeting(greeting)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.Greeting = normalized
	return s.saveBusinessProfile(ctx, profile)
}

func (s *Service) UpdateBusinessAwayMessage(ctx context.Context, userID int64, away *domain.BusinessAwayMessage) (domain.BusinessProfile, error) {
	profile, err := s.businessProfileForUpdate(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	normalized, err := normalizeBusinessAway(away)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.Away = normalized
	return s.saveBusinessProfile(ctx, profile)
}

func (s *Service) ListBusinessChatLinks(ctx context.Context, userID int64) ([]domain.BusinessChatLink, error) {
	if s == nil || s.business == nil || userID == 0 {
		return nil, nil
	}
	return s.business.ListBusinessChatLinks(ctx, userID)
}

func (s *Service) CreateBusinessChatLink(ctx context.Context, userID int64, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.BusinessChatLink{}, domain.ErrPremiumRequired
	}
	normalized, err := domain.NormalizeBusinessChatLinkInput(input)
	if err != nil {
		return domain.BusinessChatLink{}, err
	}
	now := time.Now().Unix()
	for i := 0; i < 8; i++ {
		slug, err := randomBusinessChatLinkSlug()
		if err != nil {
			return domain.BusinessChatLink{}, err
		}
		link, err := s.business.CreateBusinessChatLink(ctx, domain.BusinessChatLink{
			OwnerUserID: userID,
			Slug:        slug,
			Link:        s.businessChatLinkURL(slug),
			Message:     normalized.Message,
			Entities:    normalized.Entities,
			Title:       normalized.Title,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		if err == nil {
			return link, nil
		}
		if !errors.Is(err, domain.ErrBusinessChatLinkInvalid) {
			return domain.BusinessChatLink{}, err
		}
	}
	return domain.BusinessChatLink{}, domain.ErrBusinessChatLinkInvalid
}

func (s *Service) EditBusinessChatLink(ctx context.Context, userID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.BusinessChatLink{}, domain.ErrPremiumRequired
	}
	normalized, err := domain.NormalizeBusinessChatLinkInput(input)
	if err != nil {
		return domain.BusinessChatLink{}, err
	}
	return s.business.UpdateBusinessChatLink(ctx, userID, strings.TrimSpace(slug), normalized)
}

func (s *Service) DeleteBusinessChatLink(ctx context.Context, userID int64, slug string) (bool, error) {
	if s == nil || s.business == nil || userID == 0 {
		return false, domain.ErrBusinessChatLinkNotFound
	}
	return s.business.DeleteBusinessChatLink(ctx, userID, strings.TrimSpace(slug))
}

func (s *Service) ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error) {
	if s == nil || s.business == nil {
		return domain.BusinessChatLink{}, false, nil
	}
	return s.business.ResolveBusinessChatLink(ctx, strings.TrimSpace(slug), bumpViews)
}

func (s *Service) GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error) {
	if s == nil || s.business == nil || ownerUserID == 0 {
		return domain.ConnectedBusinessBot{}, false, nil
	}
	return s.business.GetConnectedBusinessBot(ctx, ownerUserID)
}

func (s *Service) SaveConnectedBusinessBot(ctx context.Context, ownerUserID int64, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error) {
	if s == nil || s.business == nil || ownerUserID == 0 || bot.BotUserID == 0 || bot.BotUserID == ownerUserID {
		return domain.ConnectedBusinessBot{}, domain.ErrBotBusinessMissing
	}
	recipients, err := normalizeBusinessBotRecipients(bot.Recipients)
	if err != nil {
		return domain.ConnectedBusinessBot{}, err
	}
	now := time.Now().Unix()
	bot.OwnerUserID = ownerUserID
	bot.Recipients = recipients
	if bot.CreatedAtUnix == 0 {
		bot.CreatedAtUnix = now
	}
	bot.UpdatedAtUnix = now
	return s.business.SaveConnectedBusinessBot(ctx, bot)
}

func (s *Service) DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error) {
	if s == nil || s.business == nil || ownerUserID == 0 || botUserID == 0 {
		return false, domain.ErrBotBusinessMissing
	}
	return s.business.DeleteConnectedBusinessBot(ctx, ownerUserID, botUserID)
}

func (s *Service) SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error) {
	if s == nil || s.business == nil || ownerUserID == 0 || peerUserID == 0 || ownerUserID == peerUserID {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	if _, ok, err := s.business.GetConnectedBusinessBot(ctx, ownerUserID); err != nil {
		return domain.ConnectedBusinessBotPeerState{}, err
	} else if !ok {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	state, err := s.business.SetConnectedBusinessBotPaused(ctx, ownerUserID, peerUserID, paused)
	if err != nil {
		return domain.ConnectedBusinessBotPeerState{}, err
	}
	if state.UpdatedAtUnix == 0 {
		state.UpdatedAtUnix = time.Now().Unix()
	}
	return state, nil
}

func (s *Service) DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error) {
	if s == nil || s.business == nil || ownerUserID == 0 || peerUserID == 0 || ownerUserID == peerUserID {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	if _, ok, err := s.business.GetConnectedBusinessBot(ctx, ownerUserID); err != nil {
		return domain.ConnectedBusinessBotPeerState{}, err
	} else if !ok {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	state, err := s.business.DisableConnectedBusinessBotForPeer(ctx, ownerUserID, peerUserID)
	if err != nil {
		return domain.ConnectedBusinessBotPeerState{}, err
	}
	if state.UpdatedAtUnix == 0 {
		state.UpdatedAtUnix = time.Now().Unix()
	}
	return state, nil
}

func (s *Service) GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error) {
	if s == nil || s.business == nil || ownerUserID == 0 || peerUserID == 0 {
		return domain.ConnectedBusinessBotPeerState{}, false, nil
	}
	return s.business.GetConnectedBusinessBotPeerState(ctx, ownerUserID, peerUserID)
}

func (s *Service) ListQuickReplies(ctx context.Context, userID int64) (domain.QuickReplyList, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyList{OwnerUserID: userID}, nil
	}
	return s.business.ListQuickReplies(ctx, userID, true)
}

func (s *Service) CheckQuickReplyShortcut(ctx context.Context, userID int64, shortcut string) (bool, error) {
	if s == nil || s.business == nil || userID == 0 {
		if _, err := domain.NormalizeQuickReplyShortcut(shortcut); err != nil {
			return false, err
		}
		return true, nil
	}
	return s.business.CheckQuickReplyShortcut(ctx, userID, shortcut)
}

func (s *Service) SaveQuickReplyText(ctx context.Context, userID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMutation{}, domain.ErrPremiumRequired
	}
	if msg.Message == "" || utf8.RuneCountInString(msg.Message) > domain.MaxMessageTextLength || len(msg.Entities) > domain.MaxMessageEntityCount {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	return s.business.SaveQuickReplyText(ctx, userID, shortcut, msg)
}

func (s *Service) GetQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMessages{}, domain.ErrShortcutInvalid
	}
	return s.business.GetQuickReplyMessages(ctx, userID, shortcutID, ids)
}

func (s *Service) RenameQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMutation{}, domain.ErrPremiumRequired
	}
	return s.business.RenameQuickReplyShortcut(ctx, userID, shortcutID, shortcut)
}

func (s *Service) ReorderQuickReplies(ctx context.Context, userID int64, order []int) (domain.QuickReplyMutation, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMutation{}, domain.ErrPremiumRequired
	}
	return s.business.ReorderQuickReplies(ctx, userID, append([]int(nil), order...))
}

func (s *Service) DeleteQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int) (domain.QuickReplyMutation, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	return s.business.DeleteQuickReplyShortcut(ctx, userID, shortcutID)
}

func (s *Service) DeleteQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	return s.business.DeleteQuickReplyMessages(ctx, userID, shortcutID, append([]int(nil), ids...))
}

func (s *Service) businessProfileForUpdate(ctx context.Context, userID int64) (domain.BusinessProfile, error) {
	if s == nil || s.business == nil || userID == 0 {
		return domain.BusinessProfile{}, domain.ErrPremiumRequired
	}
	profile, _, err := s.business.GetBusinessProfile(ctx, userID)
	if err != nil {
		return domain.BusinessProfile{}, err
	}
	profile.UserID = userID
	return profile, nil
}

func (s *Service) saveBusinessProfile(ctx context.Context, profile domain.BusinessProfile) (domain.BusinessProfile, error) {
	profile.UpdatedAtUnix = time.Now().Unix()
	if err := s.business.SaveBusinessProfile(ctx, profile); err != nil {
		return domain.BusinessProfile{}, err
	}
	return profile, nil
}

func normalizeBusinessWorkHours(in *domain.BusinessWorkHours) (*domain.BusinessWorkHours, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	out.TimezoneID = strings.TrimSpace(out.TimezoneID)
	out.OpenNow = false
	if out.TimezoneID == "" || len(out.WeeklyOpen) == 0 || len(out.WeeklyOpen) > domain.MaxBusinessWorkHourIntervals {
		return nil, domain.ErrBusinessProfileInvalid
	}
	out.WeeklyOpen = append([]domain.BusinessWeeklyOpen(nil), out.WeeklyOpen...)
	for _, item := range out.WeeklyOpen {
		if item.StartMinute < 0 || item.EndMinute <= item.StartMinute || item.EndMinute > 8*24*60 {
			return nil, domain.ErrBusinessProfileInvalid
		}
	}
	return &out, nil
}

func normalizeBusinessLocation(in *domain.BusinessLocation) (*domain.BusinessLocation, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	out.Address = strings.TrimSpace(out.Address)
	if out.Address == "" || utf8.RuneCountInString(out.Address) > maxBusinessLocationAddress {
		return nil, domain.ErrBusinessProfileInvalid
	}
	if out.Geo != nil {
		geo := *out.Geo
		if geo.Lat < -90 || geo.Lat > 90 || geo.Long < -180 || geo.Long > 180 {
			return nil, domain.ErrBusinessProfileInvalid
		}
		out.Geo = &geo
	}
	return &out, nil
}

func normalizeBusinessIntro(in *domain.BusinessIntro) (*domain.BusinessIntro, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	out.Title = strings.TrimSpace(out.Title)
	out.Description = strings.TrimSpace(out.Description)
	if out.Title == "" && out.Description == "" && out.StickerDocumentID == 0 {
		return nil, nil
	}
	if utf8.RuneCountInString(out.Title) > maxBusinessIntroTitle || utf8.RuneCountInString(out.Description) > maxBusinessIntroDesc {
		return nil, domain.ErrBusinessProfileInvalid
	}
	return &out, nil
}

func normalizeBusinessGreeting(in *domain.BusinessGreetingMessage) (*domain.BusinessGreetingMessage, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	if out.ShortcutID <= 0 || !validGreetingNoActivityDays(out.NoActivityDays) {
		return nil, domain.ErrBusinessProfileInvalid
	}
	recipients, err := normalizeBusinessRecipients(out.Recipients)
	if err != nil {
		return nil, err
	}
	out.Recipients = recipients
	return &out, nil
}

func normalizeBusinessAway(in *domain.BusinessAwayMessage) (*domain.BusinessAwayMessage, error) {
	if in == nil {
		return nil, nil
	}
	out := *in
	if out.ShortcutID <= 0 {
		return nil, domain.ErrBusinessProfileInvalid
	}
	switch out.Schedule.Kind {
	case domain.BusinessAwayScheduleAlways, domain.BusinessAwayScheduleOutsideWorkHours:
		out.Schedule.StartDate = 0
		out.Schedule.EndDate = 0
	case domain.BusinessAwayScheduleCustom:
		if out.Schedule.StartDate <= 0 || out.Schedule.EndDate <= out.Schedule.StartDate {
			return nil, domain.ErrBusinessProfileInvalid
		}
	default:
		return nil, domain.ErrBusinessProfileInvalid
	}
	recipients, err := normalizeBusinessRecipients(out.Recipients)
	if err != nil {
		return nil, err
	}
	out.Recipients = recipients
	return &out, nil
}

func normalizeBusinessRecipients(in domain.BusinessRecipients) (domain.BusinessRecipients, error) {
	out := in
	out.Users = append([]int64(nil), in.Users...)
	if len(out.Users) > domain.MaxBusinessRecipientUsers {
		return domain.BusinessRecipients{}, domain.ErrBusinessProfileInvalid
	}
	seen := make(map[int64]struct{}, len(out.Users))
	users := out.Users[:0]
	for _, id := range out.Users {
		if id <= 0 {
			return domain.BusinessRecipients{}, domain.ErrBusinessProfileInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		users = append(users, id)
	}
	out.Users = users
	if !out.ExistingChats && !out.NewChats && !out.Contacts && !out.NonContacts && len(out.Users) == 0 {
		return domain.BusinessRecipients{}, domain.ErrBusinessProfileInvalid
	}
	return out, nil
}

func normalizeBusinessBotRecipients(in domain.BusinessBotRecipients) (domain.BusinessBotRecipients, error) {
	out := in
	users, err := dedupeBusinessUserIDs(in.Users)
	if err != nil {
		return domain.BusinessBotRecipients{}, err
	}
	excluded, err := dedupeBusinessUserIDs(in.ExcludeUsers)
	if err != nil {
		return domain.BusinessBotRecipients{}, err
	}
	if len(users)+len(excluded) > domain.MaxBusinessRecipientUsers {
		return domain.BusinessBotRecipients{}, domain.ErrBusinessProfileInvalid
	}
	if out.ExcludeSelected {
		merged := append(users, excluded...)
		users, err = dedupeBusinessUserIDs(merged)
		if err != nil {
			return domain.BusinessBotRecipients{}, err
		}
		excluded = nil
	}
	out.Users = users
	out.ExcludeUsers = excluded
	if !out.ExcludeSelected && !out.ExistingChats && !out.NewChats && !out.Contacts && !out.NonContacts && len(out.Users) == 0 {
		return domain.BusinessBotRecipients{}, domain.ErrBusinessRecipientsEmpty
	}
	return out, nil
}

func dedupeBusinessUserIDs(in []int64) ([]int64, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, id := range in {
		if id <= 0 {
			return nil, domain.ErrBusinessProfileInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func validGreetingNoActivityDays(days int) bool {
	switch days {
	case 7, 14, 21, 28:
		return true
	default:
		return false
	}
}

func randomBusinessChatLinkSlug() (string, error) {
	value, err := randomInt64()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", value), nil
}

func (s *Service) businessChatLinkURL(slug string) string {
	baseURL := links.DefaultPublicBaseURL
	if s != nil && s.publicBaseURL != "" {
		baseURL = s.publicBaseURL
	}
	return links.Build(baseURL, "m/"+slug, nil)
}
