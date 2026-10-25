package memory

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
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
	if link.OwnerUserID == 0 || link.Slug == "" || link.Link == "" {
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

func (s *PasswordStore) MutateQuickReplies(_ context.Context, mutation store.QuickReplyAccountMutation, effects store.DeliveryEffectsBuilder[store.QuickReplyAccountMutationSnapshot]) (store.QuickReplyAccountMutationSnapshot, error) {
	validated, err := mutation.Normalize()
	if err != nil {
		return store.QuickReplyAccountMutationSnapshot{}, err
	}
	if effects == nil {
		return store.QuickReplyAccountMutationSnapshot{}, store.ErrDeliveryOutboxRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.updateEvents
	if events == nil {
		return store.QuickReplyAccountMutationSnapshot{}, store.ErrDeliveryOutboxRequired
	}
	state := cloneMemoryQuickReplyState(s, validated.UserID)
	result, changed, err := state.apply(validated)
	if err != nil {
		return store.QuickReplyAccountMutationSnapshot{}, err
	}
	snapshot := store.QuickReplyAccountMutationSnapshot{
		Mutation: validated,
		Changed:  changed,
		Result:   result,
	}
	intents, err := effects(store.CloneQuickReplyAccountSnapshot(snapshot))
	if err != nil {
		return store.QuickReplyAccountMutationSnapshot{}, fmt.Errorf("build memory quick reply effects: %w", err)
	}
	if err := store.ValidateQuickReplyAccountEffects(snapshot, intents); err != nil {
		return store.QuickReplyAccountMutationSnapshot{}, fmt.Errorf("validate memory quick reply effects: %w", err)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(intents) == 1 {
		event := events.appendLocked(validated.UserID, intents[0].Event, true)
		events.dispatches[validated.UserID] = append(events.dispatches[validated.UserID], memoryUpdateDispatch{Pts: event.Pts})
		intents[0].Event = event
	}
	state.install(s, validated.UserID)
	snapshot.Effects = intents
	return store.CloneQuickReplyAccountSnapshot(snapshot), nil
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

type memoryQuickReplyState struct {
	replies       map[int]domain.QuickReply
	byShortcut    map[string]int
	messages      map[int]map[int]domain.QuickReplyMessage
	nextReplyID   int
	nextMessageID int
}

func cloneMemoryQuickReplyState(s *PasswordStore, ownerUserID int64) memoryQuickReplyState {
	state := memoryQuickReplyState{
		replies:       make(map[int]domain.QuickReply, len(s.quickReplies[ownerUserID])),
		byShortcut:    make(map[string]int, len(s.quickReplyByShortcut[ownerUserID])),
		messages:      make(map[int]map[int]domain.QuickReplyMessage, len(s.quickReplyMessages[ownerUserID])),
		nextReplyID:   s.nextQuickReplyID[ownerUserID],
		nextMessageID: s.nextQuickReplyMessageID[ownerUserID],
	}
	for id, reply := range s.quickReplies[ownerUserID] {
		state.replies[id] = cloneQuickReply(reply)
	}
	for shortcut, id := range s.quickReplyByShortcut[ownerUserID] {
		state.byShortcut[shortcut] = id
	}
	for shortcutID, messages := range s.quickReplyMessages[ownerUserID] {
		cloned := make(map[int]domain.QuickReplyMessage, len(messages))
		for id, message := range messages {
			cloned[id] = cloneQuickReplyMessage(message)
		}
		state.messages[shortcutID] = cloned
	}
	return state
}

func (s *memoryQuickReplyState) apply(mutation store.QuickReplyAccountMutation) (domain.QuickReplyMutation, bool, error) {
	ownerUserID := mutation.UserID
	result := domain.QuickReplyMutation{Date: mutation.Date}
	switch mutation.Kind {
	case store.QuickReplyAccountSaveText:
		key := quickReplyShortcutKey(mutation.Shortcut)
		replyID, found := s.byShortcut[key]
		created := false
		if !found {
			if len(s.replies) >= domain.MaxQuickReplies {
				return domain.QuickReplyMutation{}, false, domain.ErrQuickRepliesTooMuch
			}
			s.nextReplyID++
			replyID = s.nextReplyID
			s.byShortcut[key] = replyID
			s.replies[replyID] = domain.QuickReply{
				OwnerUserID: ownerUserID, ID: replyID, Shortcut: mutation.Shortcut, SortOrder: len(s.replies) + 1,
			}
			s.messages[replyID] = make(map[int]domain.QuickReplyMessage)
			created = true
		}
		if s.messages[replyID] == nil {
			s.messages[replyID] = make(map[int]domain.QuickReplyMessage)
		}
		if len(s.messages[replyID]) >= domain.MaxQuickReplyMessages {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
		}
		s.nextMessageID++
		message := cloneQuickReplyMessage(mutation.Message)
		message.OwnerUserID, message.ShortcutID, message.ID = ownerUserID, replyID, s.nextMessageID
		s.messages[replyID][message.ID] = message
		s.refresh(replyID)
		result.Kind = domain.QuickReplyMutationMessage
		if created {
			result.Kind = domain.QuickReplyMutationNew
		}
		result.QuickReply = cloneQuickReply(s.replies[replyID])
		result.ShortcutID = replyID
		result.Message = cloneQuickReplyMessage(message)
	case store.QuickReplyAccountRenameShortcut:
		reply, ok := s.replies[mutation.ShortcutID]
		if !ok {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
		}
		key := quickReplyShortcutKey(mutation.Shortcut)
		if existingID, exists := s.byShortcut[key]; exists && existingID != mutation.ShortcutID {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutOccupied
		}
		result.Kind = domain.QuickReplyMutationList
		if reply.Shortcut == mutation.Shortcut {
			result.List = s.list(ownerUserID)
			return result, false, nil
		}
		delete(s.byShortcut, quickReplyShortcutKey(reply.Shortcut))
		reply.Shortcut = mutation.Shortcut
		s.byShortcut[key] = mutation.ShortcutID
		s.replies[mutation.ShortcutID] = reply
	case store.QuickReplyAccountReorder:
		current := s.order()
		if len(mutation.Order) != len(current) {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
		}
		for _, id := range mutation.Order {
			if _, ok := s.replies[id]; !ok {
				return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
			}
		}
		result.Kind = domain.QuickReplyMutationList
		if sameQuickReplyIDs(current, mutation.Order) {
			result.List = s.list(ownerUserID)
			return result, false, nil
		}
		for i, id := range mutation.Order {
			reply := s.replies[id]
			reply.SortOrder = i + 1
			s.replies[id] = reply
		}
	case store.QuickReplyAccountDeleteShortcut:
		reply, ok := s.replies[mutation.ShortcutID]
		if !ok {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
		}
		delete(s.byShortcut, quickReplyShortcutKey(reply.Shortcut))
		delete(s.replies, mutation.ShortcutID)
		delete(s.messages, mutation.ShortcutID)
		normalizeQuickReplyOrderLocked(s.replies)
		result.Kind, result.ShortcutID = domain.QuickReplyMutationDelete, mutation.ShortcutID
	case store.QuickReplyAccountDeleteMessages:
		if _, ok := s.replies[mutation.ShortcutID]; !ok {
			return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
		}
		messages := s.messages[mutation.ShortcutID]
		for _, id := range mutation.MessageIDs {
			if _, ok := messages[id]; !ok {
				return domain.QuickReplyMutation{}, false, domain.ErrShortcutInvalid
			}
		}
		for _, id := range mutation.MessageIDs {
			delete(messages, id)
		}
		s.refresh(mutation.ShortcutID)
		result.Kind, result.ShortcutID = domain.QuickReplyMutationIDs, mutation.ShortcutID
		result.MessageIDs = append([]int(nil), mutation.MessageIDs...)
	}
	result.List = s.list(ownerUserID)
	return result, true, nil
}

func (s *memoryQuickReplyState) refresh(shortcutID int) {
	reply := s.replies[shortcutID]
	reply.Count, reply.TopMessage = len(s.messages[shortcutID]), 0
	for id := range s.messages[shortcutID] {
		if id > reply.TopMessage {
			reply.TopMessage = id
		}
	}
	s.replies[shortcutID] = reply
}

func (s *memoryQuickReplyState) list(ownerUserID int64) domain.QuickReplyList {
	replies := make([]domain.QuickReply, 0, len(s.replies))
	for id := range s.replies {
		s.refresh(id)
		replies = append(replies, cloneQuickReply(s.replies[id]))
	}
	sortQuickReplies(replies)
	return domain.QuickReplyList{OwnerUserID: ownerUserID, QuickReplies: replies, Hash: quickReplyListHash(replies)}
}

func (s *memoryQuickReplyState) order() []int {
	replies := make([]domain.QuickReply, 0, len(s.replies))
	for _, reply := range s.replies {
		replies = append(replies, reply)
	}
	sortQuickReplies(replies)
	order := make([]int, len(replies))
	for i := range replies {
		order[i] = replies[i].ID
	}
	return order
}

func (s *memoryQuickReplyState) install(target *PasswordStore, ownerUserID int64) {
	target.quickReplies[ownerUserID] = s.replies
	target.quickReplyByShortcut[ownerUserID] = s.byShortcut
	target.quickReplyMessages[ownerUserID] = s.messages
	target.nextQuickReplyID[ownerUserID] = s.nextReplyID
	target.nextQuickReplyMessageID[ownerUserID] = s.nextMessageID
}

func sameQuickReplyIDs(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
