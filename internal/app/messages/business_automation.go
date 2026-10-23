package messages

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type BusinessAutomationOnlineChecker interface {
	IsUserOnline(userID int64) bool
}

type BusinessAutomationReplyProvider interface {
	BusinessAutomationReplies(ctx context.Context, input BusinessAutomationReplyInput) ([]domain.QuickReplyMessage, error)
}

type BusinessAutomationReplyInput struct {
	Kind           domain.BusinessAutomationKind
	OwnerUserID    int64
	CustomerUserID int64
	Profile        domain.BusinessProfile
	TriggerMessage domain.Message
	Templates      []domain.QuickReplyMessage
	Now            int
}

type businessAutomationConfig struct {
	store         store.BusinessAutomationStore
	online        BusinessAutomationOnlineChecker
	replyProvider BusinessAutomationReplyProvider
}

type BusinessAutomationOption func(*businessAutomationConfig)

func WithBusinessAutomation(business store.BusinessAutomationStore, opts ...BusinessAutomationOption) Option {
	return func(s *Service) {
		cfg := &businessAutomationConfig{store: business}
		for _, opt := range opts {
			opt(cfg)
		}
		s.business = cfg
	}
}

func WithBusinessAutomationOnlineChecker(online BusinessAutomationOnlineChecker) BusinessAutomationOption {
	return func(cfg *businessAutomationConfig) {
		cfg.online = online
	}
}

func WithBusinessAutomationReplyProvider(provider BusinessAutomationReplyProvider) BusinessAutomationOption {
	return func(cfg *businessAutomationConfig) {
		cfg.replyProvider = provider
	}
}

type businessAutomationContext struct {
	ownerUserID      int64
	customerUserID   int64
	existingChat     bool
	lastActivityDate int
	isContact        bool
}

func (s *Service) prepareBusinessAutomation(ctx context.Context, req domain.SendPrivateTextRequest) (businessAutomationContext, bool) {
	if !s.shouldConsiderBusinessAutomation(req) {
		return businessAutomationContext{}, false
	}
	hasAutomation, err := s.business.store.HasBusinessAutomation(ctx, req.RecipientUserID)
	if err != nil || !hasAutomation {
		return businessAutomationContext{}, false
	}
	out := businessAutomationContext{
		ownerUserID:    req.RecipientUserID,
		customerUserID: req.SenderUserID,
	}
	if s.dialogs != nil {
		list, err := s.dialogs.ListByPeers(ctx, req.RecipientUserID, []domain.Peer{{Type: domain.PeerTypeUser, ID: req.SenderUserID}})
		if err != nil {
			return businessAutomationContext{}, false
		}
		if len(list.Dialogs) > 0 && list.Dialogs[0].TopMessage > 0 {
			out.existingChat = true
			out.lastActivityDate = list.Dialogs[0].TopMessageDate
		}
	}
	if s.contacts != nil {
		_, ok, err := s.contacts.Get(ctx, req.RecipientUserID, req.SenderUserID)
		if err != nil {
			return businessAutomationContext{}, false
		}
		out.isContact = ok
	}
	return out, true
}

func (s *Service) shouldConsiderBusinessAutomation(req domain.SendPrivateTextRequest) bool {
	if s == nil || s.business == nil || s.business.store == nil {
		return false
	}
	if req.BusinessAutomationKind != "" || req.RecipientBlocked {
		return false
	}
	if req.SenderUserID == 0 || req.RecipientUserID == 0 || req.SenderUserID == req.RecipientUserID {
		return false
	}
	if s.botResponder != nil && (s.botResponder.HandlesBot(req.SenderUserID) || s.botResponder.HandlesBot(req.RecipientUserID)) {
		return false
	}
	return true
}

func (s *Service) runBusinessAutomation(ctx context.Context, req domain.SendPrivateTextRequest, res domain.SendPrivateTextResult, automation businessAutomationContext) {
	now := req.Date
	if now == 0 {
		now = res.RecipientMessage.Date
	}
	if now == 0 {
		now = int(time.Now().Unix())
	}
	trigger := res.RecipientMessage
	if trigger.ID == 0 {
		return
	}
	delivered, err := s.deliverConnectedBusinessBotAutomation(ctx, trigger, automation, now)
	if err != nil || delivered {
		return
	}
	profile, ok, err := s.business.store.GetBusinessProfile(ctx, automation.ownerUserID)
	if err != nil || !ok {
		return
	}
	profile.UserID = automation.ownerUserID
	if profile.Greeting != nil && s.businessGreetingEligible(*profile.Greeting, automation, now) {
		_ = s.deliverBusinessAutomation(ctx, profile, trigger, automation.customerUserID, domain.BusinessAutomationGreeting, profile.Greeting.ShortcutID, now)
		return
	}
	if profile.Away != nil && s.businessAwayEligible(ctx, profile, *profile.Away, automation, now) {
		_ = s.deliverBusinessAutomation(ctx, profile, trigger, automation.customerUserID, domain.BusinessAutomationAway, profile.Away.ShortcutID, now)
	}
}

func (s *Service) businessGreetingEligible(greeting domain.BusinessGreetingMessage, automation businessAutomationContext, now int) bool {
	if !businessRecipientsMatch(greeting.Recipients, automation.existingChat, automation.isContact, automation.customerUserID) {
		return false
	}
	if !automation.existingChat {
		return true
	}
	if automation.lastActivityDate <= 0 {
		return false
	}
	return now-automation.lastActivityDate >= greeting.NoActivityDays*24*60*60
}

func (s *Service) businessAwayEligible(ctx context.Context, profile domain.BusinessProfile, away domain.BusinessAwayMessage, automation businessAutomationContext, now int) bool {
	if !businessRecipientsMatch(away.Recipients, automation.existingChat, automation.isContact, automation.customerUserID) {
		return false
	}
	if away.OfflineOnly && s.business.online != nil && s.business.online.IsUserOnline(automation.ownerUserID) {
		return false
	}
	if !businessAwayScheduleActive(profile.WorkHours, away.Schedule, now) {
		return false
	}
	last, ok, err := s.business.store.LastBusinessAutomationDelivery(ctx, automation.ownerUserID, automation.customerUserID, domain.BusinessAutomationAway)
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	if away.Schedule.Kind == domain.BusinessAwayScheduleCustom && last.SentAt < away.Schedule.StartDate {
		return true
	}
	return now-last.SentAt >= domain.BusinessAwayCooldownSeconds
}

func (s *Service) deliverConnectedBusinessBotAutomation(ctx context.Context, trigger domain.Message, automation businessAutomationContext, now int) (bool, error) {
	if s.business.replyProvider == nil {
		return false, nil
	}
	connected, ok, err := s.business.store.GetConnectedBusinessBot(ctx, automation.ownerUserID)
	if err != nil || !ok || connected.BotUserID == 0 || !connected.Rights.Reply {
		return false, err
	}
	state, stateFound, err := s.business.store.GetConnectedBusinessBotPeerState(ctx, automation.ownerUserID, automation.customerUserID)
	if err != nil {
		return false, err
	}
	if stateFound && (state.Paused || state.Disabled) {
		return false, nil
	}
	if !domain.BusinessBotRecipientsMatch(connected.Recipients, automation.existingChat, automation.isContact, automation.customerUserID) {
		return false, nil
	}
	profile := domain.BusinessProfile{UserID: automation.ownerUserID}
	msgs, err := s.businessAutomationMessages(ctx, profile, trigger, automation.customerUserID, domain.BusinessAutomationAI, 0, now)
	if err != nil || len(msgs) == 0 {
		return false, err
	}
	if s.contacts != nil {
		blocked, err := s.contacts.IsBlocked(ctx, automation.customerUserID, automation.ownerUserID)
		if err != nil || blocked {
			return false, err
		}
	}
	reserved, err := s.business.store.ReserveBusinessAutomationDelivery(ctx, domain.BusinessAutomationDelivery{
		OwnerUserID:      automation.ownerUserID,
		PeerUserID:       automation.customerUserID,
		Kind:             domain.BusinessAutomationAI,
		TriggerMessageID: trigger.ID,
		ShortcutID:       0,
		SentAt:           now,
	})
	if err != nil || !reserved {
		return false, err
	}
	for i, msg := range msgs {
		_, err := s.SendPrivateText(ctx, automation.ownerUserID, domain.SendPrivateTextRequest{
			SenderUserID:           automation.ownerUserID,
			RecipientUserID:        automation.customerUserID,
			RandomID:               businessAutomationRandomID(domain.BusinessAutomationAI, automation.ownerUserID, automation.customerUserID, trigger.ID, msg.ID, i),
			Message:                msg.Message,
			Entities:               append([]domain.MessageEntity(nil), msg.Entities...),
			Date:                   now,
			ViaBotID:               connected.BotUserID,
			BusinessAutomationKind: domain.BusinessAutomationAI,
		})
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Service) deliverBusinessAutomation(ctx context.Context, profile domain.BusinessProfile, trigger domain.Message, customerUserID int64, kind domain.BusinessAutomationKind, shortcutID int, now int) error {
	if shortcutID <= 0 {
		return nil
	}
	msgs, err := s.businessAutomationMessages(ctx, profile, trigger, customerUserID, kind, shortcutID, now)
	if err != nil || len(msgs) == 0 {
		return err
	}
	if s.contacts != nil {
		blocked, err := s.contacts.IsBlocked(ctx, customerUserID, profile.UserID)
		if err != nil || blocked {
			return err
		}
	}
	reserved, err := s.business.store.ReserveBusinessAutomationDelivery(ctx, domain.BusinessAutomationDelivery{
		OwnerUserID:      profile.UserID,
		PeerUserID:       customerUserID,
		Kind:             kind,
		TriggerMessageID: trigger.ID,
		ShortcutID:       shortcutID,
		SentAt:           now,
	})
	if err != nil || !reserved {
		return err
	}
	for i, msg := range msgs {
		var groupedID int64
		if msg.GroupedID != 0 {
			// A fresh album id per delivery, stable for retries of the same trigger.
			groupedID = businessAutomationRandomID(kind, profile.UserID, customerUserID, trigger.ID, int(msg.GroupedID&0x7fffffff), -1)
		}
		_, err := s.SendPrivateText(ctx, profile.UserID, domain.SendPrivateTextRequest{
			SenderUserID:           profile.UserID,
			RecipientUserID:        customerUserID,
			RandomID:               businessAutomationRandomID(kind, profile.UserID, customerUserID, trigger.ID, msg.ID, i),
			Message:                msg.Message,
			Entities:               append([]domain.MessageEntity(nil), msg.Entities...),
			Media:                  msg.Media,
			GroupedID:              groupedID,
			Date:                   now,
			BusinessAutomationKind: kind,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) businessAutomationMessages(ctx context.Context, profile domain.BusinessProfile, trigger domain.Message, customerUserID int64, kind domain.BusinessAutomationKind, shortcutID int, now int) ([]domain.QuickReplyMessage, error) {
	var templateMessages []domain.QuickReplyMessage
	templates, err := s.business.store.GetQuickReplyMessages(ctx, profile.UserID, shortcutID, nil)
	if err != nil {
		if s.business.replyProvider == nil || !errors.Is(err, domain.ErrShortcutInvalid) {
			return nil, err
		}
	} else {
		templateMessages = templates.Messages
	}
	msgs := cloneBusinessAutomationMessages(templateMessages)
	if s.business.replyProvider != nil {
		msgs, err = s.business.replyProvider.BusinessAutomationReplies(ctx, BusinessAutomationReplyInput{
			Kind:           kind,
			OwnerUserID:    profile.UserID,
			CustomerUserID: customerUserID,
			Profile:        profile,
			TriggerMessage: trigger,
			Templates:      cloneBusinessAutomationMessages(templateMessages),
			Now:            now,
		})
		if err != nil {
			return nil, err
		}
		msgs = cloneBusinessAutomationMessages(msgs)
	}
	out := make([]domain.QuickReplyMessage, 0, len(msgs))
	for _, msg := range msgs {
		if (msg.Message == "" && msg.Media.IsZero()) || utf8.RuneCountInString(msg.Message) > domain.MaxMessageTextLength || len(msg.Entities) > domain.MaxMessageEntityCount {
			continue
		}
		out = append(out, msg)
		if len(out) >= domain.MaxQuickReplyMessages {
			break
		}
	}
	return out, nil
}

func businessRecipientsMatch(recipients domain.BusinessRecipients, existingChat, isContact bool, userID int64) bool {
	selected := false
	if existingChat && recipients.ExistingChats {
		selected = true
	}
	if !existingChat && recipients.NewChats {
		selected = true
	}
	if isContact && recipients.Contacts {
		selected = true
	}
	if !isContact && recipients.NonContacts {
		selected = true
	}
	for _, id := range recipients.Users {
		if id == userID {
			selected = true
			break
		}
	}
	if recipients.ExcludeSelected {
		return !selected
	}
	return selected
}

func businessAwayScheduleActive(hours *domain.BusinessWorkHours, schedule domain.BusinessAwaySchedule, now int) bool {
	switch schedule.Kind {
	case domain.BusinessAwayScheduleAlways:
		return true
	case domain.BusinessAwayScheduleCustom:
		return now >= schedule.StartDate && now < schedule.EndDate
	case domain.BusinessAwayScheduleOutsideWorkHours:
		open, ok := businessWorkHoursOpen(hours, now)
		return ok && !open
	default:
		return false
	}
}

func businessWorkHoursOpen(hours *domain.BusinessWorkHours, now int) (bool, bool) {
	if hours == nil || hours.TimezoneID == "" || len(hours.WeeklyOpen) == 0 {
		return false, false
	}
	loc, err := time.LoadLocation(hours.TimezoneID)
	if err != nil {
		return false, false
	}
	local := time.Unix(int64(now), 0).In(loc)
	weekday := (int(local.Weekday()) + 6) % 7
	minute := weekday*24*60 + local.Hour()*60 + local.Minute()
	const weekMinutes = 7 * 24 * 60
	for _, item := range hours.WeeklyOpen {
		if item.StartMinute < 0 || item.EndMinute <= item.StartMinute || item.EndMinute > 8*24*60 {
			continue
		}
		if item.EndMinute <= weekMinutes {
			if minute >= item.StartMinute && minute < item.EndMinute {
				return true, true
			}
			continue
		}
		if minute >= item.StartMinute || minute+weekMinutes < item.EndMinute {
			return true, true
		}
	}
	return false, true
}

func businessAutomationRandomID(kind domain.BusinessAutomationKind, ownerUserID, customerUserID int64, triggerMessageID, templateMessageID, index int) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	writeBusinessAutomationHashInt64(h, ownerUserID)
	writeBusinessAutomationHashInt64(h, customerUserID)
	writeBusinessAutomationHashInt64(h, int64(triggerMessageID))
	writeBusinessAutomationHashInt64(h, int64(templateMessageID))
	writeBusinessAutomationHashInt64(h, int64(index))
	id := int64(h.Sum64() & 0x7fffffffffffffff)
	if id == 0 {
		return 1
	}
	return id
}

func writeBusinessAutomationHashInt64(h interface{ Write([]byte) (int, error) }, v int64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	_, _ = h.Write(buf[:])
}

func cloneBusinessAutomationMessages(in []domain.QuickReplyMessage) []domain.QuickReplyMessage {
	out := make([]domain.QuickReplyMessage, 0, len(in))
	for _, msg := range in {
		msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
		out = append(out, msg)
	}
	return out
}
