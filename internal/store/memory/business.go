package memory

import (
	"context"
	"hash/fnv"
	"sort"
	"strings"

	"telesrv/internal/domain"
)

func (s *PasswordStore) HasBusinessAutomation(_ context.Context, userID int64) (bool, error) {
	s.mu.RLock()
	profile, hasProfile := s.businessProfiles[userID]
	bot, hasBot := s.connectedBusinessBots[userID]
	s.mu.RUnlock()
	return hasProfile && (profile.Greeting != nil || profile.Away != nil) || hasBot && bot.BotUserID != 0, nil
}

func (s *PasswordStore) GetBusinessProfile(_ context.Context, userID int64) (domain.BusinessProfile, bool, error) {
	s.mu.RLock()
	profile, ok := s.businessProfiles[userID]
	s.mu.RUnlock()
	return cloneBusinessProfile(profile), ok, nil
}

func (s *PasswordStore) SaveBusinessProfile(_ context.Context, profile domain.BusinessProfile) error {
	if profile.UserID == 0 {
		return domain.ErrBusinessProfileInvalid
	}
	s.mu.Lock()
	s.businessProfiles[profile.UserID] = cloneBusinessProfile(profile)
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) ListBusinessChatLinks(_ context.Context, ownerUserID int64) ([]domain.BusinessChatLink, error) {
	s.mu.RLock()
	slugs := append([]string(nil), s.businessChatLinkSlugs[ownerUserID]...)
	out := make([]domain.BusinessChatLink, 0, len(slugs))
	for _, slug := range slugs {
		if link, ok := s.businessChatLinks[slug]; ok {
			out = append(out, cloneBusinessChatLink(link))
		}
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].Slug < out[j].Slug
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}

func (s *PasswordStore) CreateBusinessChatLink(_ context.Context, link domain.BusinessChatLink) (domain.BusinessChatLink, error) {
	if link.OwnerUserID == 0 || link.Slug == "" || link.Link == "" || link.Message == "" {
		return domain.BusinessChatLink{}, domain.ErrBusinessChatLinkInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.businessChatLinks[link.Slug]; exists {
		return domain.BusinessChatLink{}, domain.ErrBusinessChatLinkInvalid
	}
	if len(s.businessChatLinkSlugs[link.OwnerUserID]) >= domain.MaxBusinessChatLinks {
		return domain.BusinessChatLink{}, domain.ErrBusinessChatLinksTooMuch
	}
	link = cloneBusinessChatLink(link)
	s.businessChatLinks[link.Slug] = link
	s.businessChatLinkSlugs[link.OwnerUserID] = append(s.businessChatLinkSlugs[link.OwnerUserID], link.Slug)
	return cloneBusinessChatLink(link), nil
}

func (s *PasswordStore) UpdateBusinessChatLink(_ context.Context, ownerUserID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.businessChatLinks[slug]
	if !ok || link.OwnerUserID != ownerUserID {
		return domain.BusinessChatLink{}, domain.ErrBusinessChatLinkNotFound
	}
	link.Message = input.Message
	link.Entities = append([]domain.MessageEntity(nil), input.Entities...)
	link.Title = input.Title
	s.businessChatLinks[slug] = cloneBusinessChatLink(link)
	return cloneBusinessChatLink(link), nil
}

func (s *PasswordStore) DeleteBusinessChatLink(_ context.Context, ownerUserID int64, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.businessChatLinks[slug]
	if !ok || link.OwnerUserID != ownerUserID {
		return false, nil
	}
	delete(s.businessChatLinks, slug)
	slugs := s.businessChatLinkSlugs[ownerUserID]
	next := slugs[:0]
	for _, item := range slugs {
		if item != slug {
			next = append(next, item)
		}
	}
	s.businessChatLinkSlugs[ownerUserID] = next
	return true, nil
}

func (s *PasswordStore) ResolveBusinessChatLink(_ context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	link, ok := s.businessChatLinks[slug]
	if !ok {
		return domain.BusinessChatLink{}, false, nil
	}
	if bumpViews {
		link.Views++
		s.businessChatLinks[slug] = link
	}
	return cloneBusinessChatLink(link), true, nil
}

func (s *PasswordStore) ListQuickReplies(_ context.Context, ownerUserID int64, includeTopMessages bool) (domain.QuickReplyList, error) {
	s.mu.RLock()
	out := s.quickReplyListLocked(ownerUserID, includeTopMessages)
	s.mu.RUnlock()
	return out, nil
}

func (s *PasswordStore) CheckQuickReplyShortcut(_ context.Context, ownerUserID int64, shortcut string) (bool, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return false, err
	}
	s.mu.RLock()
	_, ok := s.quickReplyByShortcut[ownerUserID][quickReplyShortcutKey(shortcut)]
	s.mu.RUnlock()
	return !ok, nil
}

func (s *PasswordStore) SaveQuickReplyText(_ context.Context, ownerUserID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	if (msg.Message == "" && msg.Media.IsZero()) || len(msg.Entities) > domain.MaxMessageEntityCount {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureQuickReplyMapsLocked(ownerUserID)
	key := quickReplyShortcutKey(shortcut)
	replyID, found := s.quickReplyByShortcut[ownerUserID][key]
	created := false
	if !found {
		if len(s.quickReplies[ownerUserID]) >= domain.MaxQuickReplies {
			return domain.QuickReplyMutation{}, domain.ErrQuickRepliesTooMuch
		}
		replyID = s.nextQuickReplyID[ownerUserID] + 1
		s.nextQuickReplyID[ownerUserID] = replyID
		s.quickReplyByShortcut[ownerUserID][key] = replyID
		s.quickReplies[ownerUserID][replyID] = domain.QuickReply{
			OwnerUserID: ownerUserID,
			ID:          replyID,
			Shortcut:    shortcut,
			SortOrder:   len(s.quickReplies[ownerUserID]) + 1,
		}
		s.quickReplyMessages[ownerUserID][replyID] = make(map[int]domain.QuickReplyMessage)
		created = true
	}
	if len(s.quickReplyMessages[ownerUserID][replyID]) >= domain.MaxQuickReplyMessages {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	msg.OwnerUserID = ownerUserID
	msg.ShortcutID = replyID
	msg.ID = s.nextQuickReplyMessageID[ownerUserID] + 1
	s.nextQuickReplyMessageID[ownerUserID] = msg.ID
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	s.quickReplyMessages[ownerUserID][replyID][msg.ID] = msg
	s.refreshQuickReplyLocked(ownerUserID, replyID)
	reply := cloneQuickReply(s.quickReplies[ownerUserID][replyID])
	kind := domain.QuickReplyMutationMessage
	if created {
		kind = domain.QuickReplyMutationNew
	}
	return domain.QuickReplyMutation{
		Kind:       kind,
		List:       s.quickReplyListLocked(ownerUserID, true),
		QuickReply: reply,
		ShortcutID: replyID,
		Message:    cloneQuickReplyMessage(msg),
	}, nil
}

func (s *PasswordStore) GetQuickReplyMessages(_ context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.quickReplies[ownerUserID][shortcutID]; !ok {
		return domain.QuickReplyMessages{}, domain.ErrShortcutInvalid
	}
	msgs := s.quickReplyMessages[ownerUserID][shortcutID]
	selected := make([]domain.QuickReplyMessage, 0, len(msgs))
	if len(ids) == 0 {
		for _, msg := range msgs {
			selected = append(selected, cloneQuickReplyMessage(msg))
		}
	} else {
		for _, id := range ids {
			msg, ok := msgs[id]
			if !ok {
				return domain.QuickReplyMessages{}, domain.ErrShortcutInvalid
			}
			selected = append(selected, cloneQuickReplyMessage(msg))
		}
	}
	sortQuickReplyMessages(selected)
	return domain.QuickReplyMessages{
		OwnerUserID: ownerUserID,
		ShortcutID:  shortcutID,
		Messages:    selected,
		Count:       len(msgs),
		Hash:        quickReplyMessagesHash(selected),
	}, nil
}

func (s *PasswordStore) RenameQuickReplyShortcut(_ context.Context, ownerUserID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reply, ok := s.quickReplies[ownerUserID][shortcutID]
	if !ok {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	key := quickReplyShortcutKey(shortcut)
	if existingID, exists := s.quickReplyByShortcut[ownerUserID][key]; exists && existingID != shortcutID {
		return domain.QuickReplyMutation{}, domain.ErrShortcutOccupied
	}
	delete(s.quickReplyByShortcut[ownerUserID], quickReplyShortcutKey(reply.Shortcut))
	reply.Shortcut = shortcut
	s.quickReplyByShortcut[ownerUserID][key] = shortcutID
	s.quickReplies[ownerUserID][shortcutID] = reply
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationList, List: s.quickReplyListLocked(ownerUserID, true)}, nil
}

func (s *PasswordStore) ReorderQuickReplies(_ context.Context, ownerUserID int64, order []int) (domain.QuickReplyMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replies := s.quickReplies[ownerUserID]
	if len(order) != len(replies) {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	seen := make(map[int]struct{}, len(order))
	for i, id := range order {
		reply, ok := replies[id]
		if !ok {
			return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
		}
		if _, dup := seen[id]; dup {
			return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
		}
		seen[id] = struct{}{}
		reply.SortOrder = i + 1
		replies[id] = reply
	}
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationList, List: s.quickReplyListLocked(ownerUserID, true)}, nil
}

func (s *PasswordStore) DeleteQuickReplyShortcut(_ context.Context, ownerUserID int64, shortcutID int) (domain.QuickReplyMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply, ok := s.quickReplies[ownerUserID][shortcutID]
	if !ok {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	delete(s.quickReplyByShortcut[ownerUserID], quickReplyShortcutKey(reply.Shortcut))
	delete(s.quickReplies[ownerUserID], shortcutID)
	delete(s.quickReplyMessages[ownerUserID], shortcutID)
	normalizeQuickReplyOrderLocked(s.quickReplies[ownerUserID])
	return domain.QuickReplyMutation{
		Kind:       domain.QuickReplyMutationDelete,
		List:       s.quickReplyListLocked(ownerUserID, true),
		ShortcutID: shortcutID,
	}, nil
}

func (s *PasswordStore) DeleteQuickReplyMessages(_ context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error) {
	if len(ids) == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.quickReplies[ownerUserID][shortcutID]; !ok {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	msgs := s.quickReplyMessages[ownerUserID][shortcutID]
	deleted := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := msgs[id]; !ok {
			return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
		}
		delete(msgs, id)
		deleted = append(deleted, id)
	}
	s.refreshQuickReplyLocked(ownerUserID, shortcutID)
	return domain.QuickReplyMutation{
		Kind:       domain.QuickReplyMutationIDs,
		List:       s.quickReplyListLocked(ownerUserID, true),
		ShortcutID: shortcutID,
		MessageIDs: deleted,
	}, nil
}

func (s *PasswordStore) ReserveBusinessAutomationDelivery(_ context.Context, delivery domain.BusinessAutomationDelivery) (bool, error) {
	if delivery.OwnerUserID == 0 || delivery.PeerUserID == 0 || delivery.Kind == "" || delivery.TriggerMessageID == 0 {
		return false, domain.ErrBusinessProfileInvalid
	}
	key := businessAutomationDeliveryKey{
		ownerUserID:      delivery.OwnerUserID,
		peerUserID:       delivery.PeerUserID,
		kind:             delivery.Kind,
		triggerMessageID: delivery.TriggerMessageID,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.businessDeliveries[key]; ok {
		return false, nil
	}
	s.businessDeliveries[key] = delivery
	return true, nil
}

func (s *PasswordStore) LastBusinessAutomationDelivery(_ context.Context, ownerUserID, peerUserID int64, kind domain.BusinessAutomationKind) (domain.BusinessAutomationDelivery, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out domain.BusinessAutomationDelivery
	found := false
	for _, delivery := range s.businessDeliveries {
		if delivery.OwnerUserID != ownerUserID || delivery.PeerUserID != peerUserID || delivery.Kind != kind {
			continue
		}
		if !found || delivery.SentAt > out.SentAt || delivery.SentAt == out.SentAt && delivery.TriggerMessageID > out.TriggerMessageID {
			out = delivery
			found = true
		}
	}
	return out, found, nil
}

func (s *PasswordStore) GetConnectedBusinessBot(_ context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error) {
	s.mu.RLock()
	bot, ok := s.connectedBusinessBots[ownerUserID]
	s.mu.RUnlock()
	return cloneConnectedBusinessBot(bot), ok, nil
}

func (s *PasswordStore) SaveConnectedBusinessBot(_ context.Context, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error) {
	if bot.OwnerUserID == 0 || bot.BotUserID == 0 {
		return domain.ConnectedBusinessBot{}, domain.ErrBotBusinessMissing
	}
	s.mu.Lock()
	if prev, ok := s.connectedBusinessBots[bot.OwnerUserID]; ok && bot.CreatedAtUnix == 0 {
		bot.CreatedAtUnix = prev.CreatedAtUnix
	}
	s.connectedBusinessBots[bot.OwnerUserID] = cloneConnectedBusinessBot(bot)
	s.mu.Unlock()
	return cloneConnectedBusinessBot(bot), nil
}

func (s *PasswordStore) DeleteConnectedBusinessBot(_ context.Context, ownerUserID, botUserID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bot, ok := s.connectedBusinessBots[ownerUserID]
	if !ok || bot.BotUserID != botUserID {
		return false, nil
	}
	delete(s.connectedBusinessBots, ownerUserID)
	for key := range s.connectedBusinessBotPeerStates {
		if key.ownerUserID == ownerUserID {
			delete(s.connectedBusinessBotPeerStates, key)
		}
	}
	return true, nil
}

func (s *PasswordStore) SetConnectedBusinessBotPaused(_ context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error) {
	if ownerUserID == 0 || peerUserID == 0 {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	key := connectedBusinessBotPeerKey{ownerUserID: ownerUserID, peerUserID: peerUserID}
	s.mu.Lock()
	state := s.connectedBusinessBotPeerStates[key]
	state.OwnerUserID = ownerUserID
	state.PeerUserID = peerUserID
	state.Paused = paused
	s.connectedBusinessBotPeerStates[key] = state
	s.mu.Unlock()
	return state, nil
}

func (s *PasswordStore) DisableConnectedBusinessBotForPeer(_ context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error) {
	if ownerUserID == 0 || peerUserID == 0 {
		return domain.ConnectedBusinessBotPeerState{}, domain.ErrBotBusinessMissing
	}
	key := connectedBusinessBotPeerKey{ownerUserID: ownerUserID, peerUserID: peerUserID}
	s.mu.Lock()
	state := s.connectedBusinessBotPeerStates[key]
	state.OwnerUserID = ownerUserID
	state.PeerUserID = peerUserID
	state.Paused = false
	state.Disabled = true
	s.connectedBusinessBotPeerStates[key] = state
	s.mu.Unlock()
	return state, nil
}

func (s *PasswordStore) GetConnectedBusinessBotPeerState(_ context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error) {
	key := connectedBusinessBotPeerKey{ownerUserID: ownerUserID, peerUserID: peerUserID}
	s.mu.RLock()
	state, ok := s.connectedBusinessBotPeerStates[key]
	s.mu.RUnlock()
	return state, ok, nil
}

func (s *PasswordStore) ensureQuickReplyMapsLocked(ownerUserID int64) {
	if s.quickReplies[ownerUserID] == nil {
		s.quickReplies[ownerUserID] = make(map[int]domain.QuickReply)
	}
	if s.quickReplyByShortcut[ownerUserID] == nil {
		s.quickReplyByShortcut[ownerUserID] = make(map[string]int)
	}
	if s.quickReplyMessages[ownerUserID] == nil {
		s.quickReplyMessages[ownerUserID] = make(map[int]map[int]domain.QuickReplyMessage)
	}
	for id := range s.quickReplies[ownerUserID] {
		if s.quickReplyMessages[ownerUserID][id] == nil {
			s.quickReplyMessages[ownerUserID][id] = make(map[int]domain.QuickReplyMessage)
		}
	}
}

func (s *PasswordStore) refreshQuickReplyLocked(ownerUserID int64, shortcutID int) {
	s.ensureQuickReplyMapsLocked(ownerUserID)
	reply := s.quickReplies[ownerUserID][shortcutID]
	reply.Count = len(s.quickReplyMessages[ownerUserID][shortcutID])
	reply.TopMessage = 0
	for id := range s.quickReplyMessages[ownerUserID][shortcutID] {
		if id > reply.TopMessage {
			reply.TopMessage = id
		}
	}
	s.quickReplies[ownerUserID][shortcutID] = reply
}

func (s *PasswordStore) quickReplyListLocked(ownerUserID int64, includeTopMessages bool) domain.QuickReplyList {
	replies := make([]domain.QuickReply, 0, len(s.quickReplies[ownerUserID]))
	for id := range s.quickReplies[ownerUserID] {
		s.refreshQuickReplyLocked(ownerUserID, id)
		replies = append(replies, cloneQuickReply(s.quickReplies[ownerUserID][id]))
	}
	sortQuickReplies(replies)
	messages := make([]domain.QuickReplyMessage, 0, len(replies))
	if includeTopMessages {
		for _, reply := range replies {
			if reply.TopMessage == 0 {
				continue
			}
			if msg, ok := s.quickReplyMessages[ownerUserID][reply.ID][reply.TopMessage]; ok {
				messages = append(messages, cloneQuickReplyMessage(msg))
			}
		}
		sortQuickReplyMessages(messages)
	}
	return domain.QuickReplyList{
		OwnerUserID:  ownerUserID,
		QuickReplies: replies,
		Messages:     messages,
		Hash:         quickReplyListHash(replies),
	}
}

func normalizeQuickReplyOrderLocked(replies map[int]domain.QuickReply) {
	list := make([]domain.QuickReply, 0, len(replies))
	for _, reply := range replies {
		list = append(list, reply)
	}
	sortQuickReplies(list)
	for i, reply := range list {
		reply.SortOrder = i + 1
		replies[reply.ID] = reply
	}
}

func sortQuickReplies(items []domain.QuickReply) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].SortOrder == items[j].SortOrder {
			return items[i].ID < items[j].ID
		}
		return items[i].SortOrder < items[j].SortOrder
	})
}

func sortQuickReplyMessages(items []domain.QuickReplyMessage) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})
}

func quickReplyShortcutKey(shortcut string) string {
	return strings.ToLower(shortcut)
}

func quickReplyListHash(items []domain.QuickReply) int64 {
	h := fnv.New64a()
	for _, item := range items {
		writeHashInt(h, item.ID)
		writeHashString(h, item.Shortcut)
		writeHashInt(h, item.TopMessage)
		writeHashInt(h, item.Count)
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func quickReplyMessagesHash(items []domain.QuickReplyMessage) int64 {
	h := fnv.New64a()
	for _, item := range items {
		writeHashInt(h, item.ID)
		writeHashString(h, item.Message)
		writeHashInt(h, item.Date)
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func writeHashInt(h interface{ Write([]byte) (int, error) }, v int) {
	_, _ = h.Write([]byte{
		byte(v >> 24),
		byte(v >> 16),
		byte(v >> 8),
		byte(v),
	})
}

func writeHashString(h interface{ Write([]byte) (int, error) }, v string) {
	_, _ = h.Write([]byte(v))
	_, _ = h.Write([]byte{0})
}

func cloneBusinessProfile(in domain.BusinessProfile) domain.BusinessProfile {
	out := in
	if in.WorkHours != nil {
		v := *in.WorkHours
		v.WeeklyOpen = append([]domain.BusinessWeeklyOpen(nil), in.WorkHours.WeeklyOpen...)
		out.WorkHours = &v
	}
	if in.Location != nil {
		v := *in.Location
		if in.Location.Geo != nil {
			geo := *in.Location.Geo
			v.Geo = &geo
		}
		out.Location = &v
	}
	if in.Intro != nil {
		v := *in.Intro
		out.Intro = &v
	}
	if in.Greeting != nil {
		v := *in.Greeting
		v.Recipients.Users = append([]int64(nil), in.Greeting.Recipients.Users...)
		out.Greeting = &v
	}
	if in.Away != nil {
		v := *in.Away
		v.Recipients.Users = append([]int64(nil), in.Away.Recipients.Users...)
		out.Away = &v
	}
	return out
}

func cloneBusinessChatLink(in domain.BusinessChatLink) domain.BusinessChatLink {
	out := in
	out.Entities = append([]domain.MessageEntity(nil), in.Entities...)
	return out
}

func cloneQuickReply(in domain.QuickReply) domain.QuickReply {
	return in
}

func cloneQuickReplyMessage(in domain.QuickReplyMessage) domain.QuickReplyMessage {
	out := in
	out.Entities = append([]domain.MessageEntity(nil), in.Entities...)
	return out
}

func cloneConnectedBusinessBot(in domain.ConnectedBusinessBot) domain.ConnectedBusinessBot {
	out := in
	out.Recipients.Users = append([]int64(nil), in.Recipients.Users...)
	out.Recipients.ExcludeUsers = append([]int64(nil), in.Recipients.ExcludeUsers...)
	return out
}

func (s *PasswordStore) EditQuickReplyMessage(_ context.Context, ownerUserID int64, edit domain.QuickReplyEdit) (domain.QuickReplyMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.quickReplyMessages[ownerUserID][edit.ShortcutID]
	msg, ok := msgs[edit.ID]
	if !ok {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	msg.Message = edit.Message
	msg.Entities = append([]domain.MessageEntity(nil), edit.Entities...)
	if edit.Media != nil {
		msg.Media = edit.Media
	}
	msg.EditDate = edit.EditDate
	msgs[edit.ID] = msg
	return domain.QuickReplyMutation{
		Kind:       domain.QuickReplyMutationMessage,
		List:       s.quickReplyListLocked(ownerUserID, true),
		QuickReply: cloneQuickReply(s.quickReplies[ownerUserID][edit.ShortcutID]),
		ShortcutID: edit.ShortcutID,
		Message:    cloneQuickReplyMessage(msg),
	}, nil
}
