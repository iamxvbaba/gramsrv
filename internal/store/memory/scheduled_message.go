package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	memoryScheduledPageLimit  = 100
	memoryScheduledRetryDelay = 30
)

type memoryScheduledMessage struct {
	message    domain.ScheduledMessage
	leaseUntil int
	lastError  string
}

func (s *MessageStore) CreateScheduledMessage(ctx context.Context, req domain.ScheduleMessageRequest, effects store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	if s == nil || req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ScheduleDate <= 0 || !domain.ValidScheduleRepeat(req.ScheduleRepeatPeriod) ||
		(req.ScheduleDate == domain.ScheduleWhenOnline && (req.ScheduleRepeatPeriod != 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == req.OwnerUserID)) {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid request")
	}
	if req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: invalid peer")
	}
	if req.Message == "" && req.Media.IsZero() && req.RichMessage.IsZero() {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: empty message")
	}
	if effects == nil {
		return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
	}
	if req.Peer.Type == domain.PeerTypeUser && s.dialogs == nil {
		return domain.ScheduledMessage{}, fmt.Errorf("create scheduled message: dialog store is required")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.scheduledMessages[req.OwnerUserID]
	if req.RandomID != 0 {
		for _, item := range owner {
			if item.message.RandomID == req.RandomID && memoryScheduledActive(item.message.State) {
				return cloneScheduledMessage(item.message), nil
			}
		}
	}
	var aggregateDialogs *DialogStore
	var aggregateOutbox *DeliveryOutboxStore
	var aggregateEvents *UpdateEventStore
	if req.ClearDraft {
		aggregateDialogs, aggregateOutbox, aggregateEvents = s.dialogs, s.deliveryOutbox, s.updateEvents
		if aggregateDialogs == nil || aggregateOutbox == nil || aggregateEvents == nil {
			return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
		}
		aggregateDialogs.mu.Lock()
		defer aggregateDialogs.mu.Unlock()
		aggregateOutbox.mu.Lock()
		defer aggregateOutbox.mu.Unlock()
		aggregateEvents.mu.Lock()
		defer aggregateEvents.mu.Unlock()
	}
	pending := 0
	for _, item := range owner {
		if item.message.Peer == req.Peer && memoryScheduledActive(item.message.State) {
			pending++
		}
	}
	if pending >= memoryScheduledPageLimit {
		return domain.ScheduledMessage{}, domain.ErrScheduledLimit
	}
	nextID, err := nextMemoryScheduledID(owner)
	if err != nil {
		return domain.ScheduledMessage{}, err
	}
	msg := domain.ScheduledMessage{
		OwnerUserID: req.OwnerUserID, ID: nextID, Peer: req.Peer, RandomID: req.RandomID,
		Message: req.Message, Entities: append([]domain.MessageEntity(nil), req.Entities...),
		Media: cloneRequestedPeerMedia(req.Media), RichMessage: cloneRichMessage(req.RichMessage),
		Silent: req.Silent, NoForwards: req.NoForwards, ReplyTo: cloneMessageReply(req.ReplyTo),
		Forward: cloneMessageForward(req.Forward), SendAs: clonePeerPtr(req.SendAs),
		ScheduleDate: req.ScheduleDate, ScheduleRepeatPeriod: req.ScheduleRepeatPeriod,
		CreatedAt: req.Date, UpdatedAt: req.Date, State: "pending",
	}
	intents, err := effects(cloneScheduledMessage(msg))
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("build create scheduled delivery effects: %w", err)
	}
	if len(intents) == 0 {
		return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
	}
	var draftSnapshot store.DialogAccountMutationSnapshot
	if req.ClearDraft {
		topMessageID := 0
		if req.ReplyTo != nil && req.ReplyTo.TopMessageID > 0 {
			topMessageID = req.ReplyTo.TopMessageID
		}
		mutation := store.DialogAccountMutation{
			Kind: store.DialogAccountDeleteDraft, UserID: req.OwnerUserID, Date: req.Date,
			Peer: req.Peer, TopMessageID: topMessageID,
		}
		_, changed := aggregateDialogs.drafts[mutation.UserID][draftKey(mutation.Peer, mutation.TopMessageID)]
		draftSnapshot = store.DialogAccountMutationSnapshot{Mutation: mutation, Changed: changed}
		draftEffects, err := sendDraftClearEffects(draftSnapshot)
		if err != nil {
			return domain.ScheduledMessage{}, err
		}
		if err := store.ValidateDialogAccountEffects(draftSnapshot, draftEffects); err != nil {
			return domain.ScheduledMessage{}, fmt.Errorf("validate memory scheduled draft effects: %w", err)
		}
		intents = append(intents, draftEffects...)
		if err := s.applyScheduledCreateEffectsLocked(intents, aggregateOutbox, aggregateEvents); err != nil {
			return domain.ScheduledMessage{}, fmt.Errorf("apply create scheduled delivery effects: %w", err)
		}
	} else if _, err := applyDeliveryEffects(ctx, intents, s.deliveryOutbox, nil); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("apply create scheduled delivery effects: %w", err)
	}
	if owner == nil {
		owner = make(map[int]memoryScheduledMessage)
		s.scheduledMessages[req.OwnerUserID] = owner
	}
	owner[nextID] = memoryScheduledMessage{message: cloneScheduledMessage(msg)}
	if req.ClearDraft && draftSnapshot.Changed {
		delete(aggregateDialogs.drafts[req.OwnerUserID], draftKey(req.Peer, draftSnapshot.Mutation.TopMessageID))
	}
	if req.ClearDraft {
		s.setMemoryHasScheduledStateLocked(req.OwnerUserID, req.Peer, true)
	} else {
		s.setMemoryHasScheduledLocked(req.OwnerUserID, req.Peer, true)
	}
	return cloneScheduledMessage(msg), nil
}

func (s *MessageStore) applyScheduledCreateEffectsLocked(effects []store.DeliveryEffect, outbox *DeliveryOutboxStore, events *UpdateEventStore) error {
	absolute := make([]store.DeliveryEffect, 0, len(effects))
	account := make([]store.DeliveryEffect, 0, 1)
	for i := range effects {
		if err := effects[i].Validate(); err != nil {
			return fmt.Errorf("delivery effect %d: %w", i, err)
		}
		switch effects[i].Kind {
		case store.DeliveryEffectAbsolute:
			absolute = append(absolute, effects[i])
		case store.DeliveryEffectAccountPTS:
			account = append(account, effects[i])
		default:
			return fmt.Errorf("unsupported scheduled delivery effect kind %d", effects[i].Kind)
		}
	}
	if len(absolute) == 0 {
		return store.ErrDeliveryOutboxRequired
	}
	if err := appendAbsoluteDeliveryEffectsLocked(outbox, absolute, time.Now()); err != nil {
		return err
	}
	for _, effect := range account {
		event := events.appendLocked(effect.TargetUserID, effect.Event, true)
		events.dispatches[effect.TargetUserID] = append(events.dispatches[effect.TargetUserID], memoryUpdateDispatch{
			Pts: event.Pts, ExcludeAuthKeyID: effect.ExcludeAuthKeyID, ExcludeSessionID: effect.ExcludeSessionID,
		})
		if event.Pts > s.nextPts[effect.TargetUserID] {
			s.nextPts[effect.TargetUserID] = event.Pts
		}
	}
	return nil
}

func (s *MessageStore) EditScheduledMessage(ctx context.Context, req domain.EditScheduledMessageRequest, effects store.DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error) {
	if s == nil || req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ID <= 0 || req.ScheduleDate <= 0 ||
		(req.Peer.Type != domain.PeerTypeUser && req.Peer.Type != domain.PeerTypeChannel) {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	if effects == nil {
		return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.scheduledMessages[req.OwnerUserID][req.ID]
	if !ok || item.message.Peer != req.Peer || item.message.State != "pending" {
		return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
	}
	msg := cloneScheduledMessage(item.message)
	if req.SetMessage {
		msg.Message = req.Message
		msg.Entities = append([]domain.MessageEntity(nil), req.Entities...)
	}
	if req.SetRichMessage {
		msg.RichMessage = cloneRichMessage(req.RichMessage)
	}
	if msg.Message == "" && msg.Media.IsZero() && msg.RichMessage.IsZero() {
		return domain.ScheduledMessage{}, domain.ErrMessageEmpty
	}
	msg.ScheduleDate = req.ScheduleDate
	msg.UpdatedAt = req.Date
	intents, err := effects(cloneScheduledMessage(msg))
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("build edit scheduled delivery effects: %w", err)
	}
	if len(intents) == 0 {
		return domain.ScheduledMessage{}, store.ErrDeliveryOutboxRequired
	}
	if _, err := applyDeliveryEffects(ctx, intents, s.deliveryOutbox, nil); err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("apply edit scheduled delivery effects: %w", err)
	}
	item.message = cloneScheduledMessage(msg)
	s.scheduledMessages[req.OwnerUserID][req.ID] = item
	return cloneScheduledMessage(msg), nil
}

func (s *MessageStore) ListScheduledMessages(_ context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return s.listMemoryScheduledMessages(filter, false), nil
}

func (s *MessageStore) GetScheduledMessages(_ context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	return s.listMemoryScheduledMessages(filter, true), nil
}

func (s *MessageStore) listMemoryScheduledMessages(filter domain.ScheduledMessageFilter, exact bool) domain.ScheduledMessageList {
	if s == nil || filter.OwnerUserID == 0 || filter.Peer.ID == 0 || (exact && len(filter.IDs) == 0) {
		return domain.ScheduledMessageList{}
	}
	wanted := make(map[int]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		wanted[id] = struct{}{}
	}
	s.mu.RLock()
	msgs := make([]domain.ScheduledMessage, 0)
	for _, item := range s.scheduledMessages[filter.OwnerUserID] {
		if item.message.Peer != filter.Peer || !memoryScheduledActive(item.message.State) {
			continue
		}
		if exact {
			if _, ok := wanted[item.message.ID]; !ok {
				continue
			}
		}
		msgs = append(msgs, cloneScheduledMessage(item.message))
	}
	s.mu.RUnlock()
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].ScheduleDate != msgs[j].ScheduleDate {
			return msgs[i].ScheduleDate < msgs[j].ScheduleDate
		}
		return msgs[i].ID < msgs[j].ID
	})
	limit := filter.Limit
	if limit <= 0 || limit > memoryScheduledPageLimit {
		limit = memoryScheduledPageLimit
	}
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	out := domain.ScheduledMessageList{Messages: msgs, Count: len(msgs), Hash: memoryScheduledHash(msgs)}
	if filter.Hash != 0 && filter.Hash == out.Hash {
		out.Messages = nil
		out.Count = 0
	}
	return out
}

func (s *MessageStore) DeleteScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter, date int, effects store.DeliveryEffectsBuilder[[]domain.ScheduledMessage]) ([]domain.ScheduledMessage, error) {
	if s == nil || filter.OwnerUserID == 0 || filter.Peer.ID == 0 || len(filter.IDs) == 0 {
		return nil, nil
	}
	if effects == nil {
		return nil, store.ErrDeliveryOutboxRequired
	}
	wanted := make(map[int]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		wanted[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := s.scheduledMessages[filter.OwnerUserID]
	deleted := make([]domain.ScheduledMessage, 0, len(filter.IDs))
	for id, item := range owner {
		if _, ok := wanted[id]; !ok || item.message.Peer != filter.Peer || !memoryScheduledActive(item.message.State) {
			continue
		}
		msg := cloneScheduledMessage(item.message)
		msg.State = "deleted"
		msg.UpdatedAt = date
		deleted = append(deleted, msg)
	}
	if len(deleted) == 0 {
		return nil, nil
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i].ID < deleted[j].ID })
	intents, err := effects(cloneScheduledMessages(deleted))
	if err != nil {
		return nil, fmt.Errorf("build delete scheduled delivery effects: %w", err)
	}
	if len(intents) == 0 {
		return nil, store.ErrDeliveryOutboxRequired
	}
	if _, err := applyDeliveryEffects(ctx, intents, s.deliveryOutbox, nil); err != nil {
		return nil, fmt.Errorf("apply delete scheduled delivery effects: %w", err)
	}
	for _, msg := range deleted {
		item := owner[msg.ID]
		item.message = cloneScheduledMessage(msg)
		owner[msg.ID] = item
	}
	s.refreshMemoryHasScheduledLocked(filter.OwnerUserID, filter.Peer)
	return cloneScheduledMessages(deleted), nil
}

func (s *MessageStore) ClaimScheduledMessages(_ context.Context, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	if s == nil || claim.OwnerUserID == 0 || claim.Peer.ID == 0 || len(claim.IDs) == 0 {
		return nil, nil
	}
	if claim.Now == 0 {
		claim.Now = int(time.Now().Unix())
	}
	if claim.LeaseUntil == 0 {
		claim.LeaseUntil = claim.Now + 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ScheduledMessage, 0, len(claim.IDs))
	owner := s.scheduledMessages[claim.OwnerUserID]
	for _, id := range claim.IDs {
		item, ok := owner[id]
		if !ok || item.message.Peer != claim.Peer ||
			(item.message.State != "pending" && !(item.message.State == "dispatching" && item.leaseUntil <= claim.Now)) {
			continue
		}
		item.message.State = "dispatching"
		item.message.Generation++
		item.message.ManualDispatch = true
		item.message.UpdatedAt = claim.Now
		item.leaseUntil = claim.LeaseUntil
		item.lastError = ""
		owner[id] = item
		out = append(out, cloneScheduledMessage(item.message))
	}
	return out, nil
}

func (s *MessageStore) ClaimDueScheduledMessages(_ context.Context, now, limit, leaseSeconds int, onlineUserIDs []int64) ([]domain.ScheduledMessage, error) {
	if s == nil || now <= 0 || limit <= 0 {
		return nil, nil
	}
	if limit > memoryScheduledPageLimit {
		limit = memoryScheduledPageLimit
	}
	if leaseSeconds <= 0 {
		leaseSeconds = 60
	}
	type ref struct {
		owner int64
		id    int
	}
	online := make(map[int64]struct{}, len(onlineUserIDs))
	for _, id := range onlineUserIDs {
		online[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]ref, 0)
	for ownerID, owner := range s.scheduledMessages {
		for id, item := range owner {
			msg := item.message
			_, recipientOnline := online[msg.Peer.ID]
			whenOnline := msg.ScheduleDate == domain.ScheduleWhenOnline && msg.Peer.Type == domain.PeerTypeUser && recipientOnline
			if !msg.ManualDispatch && msg.ScheduleDate > now && !whenOnline {
				continue
			}
			pendingDue := item.message.State == "pending" && (item.lastError == "" || item.message.UpdatedAt <= now-memoryScheduledRetryDelay)
			leaseExpired := item.message.State == "dispatching" && item.leaseUntil <= now
			if pendingDue || leaseExpired {
				refs = append(refs, ref{owner: ownerID, id: id})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		a := s.scheduledMessages[refs[i].owner][refs[i].id].message
		b := s.scheduledMessages[refs[j].owner][refs[j].id].message
		if a.ScheduleDate != b.ScheduleDate {
			return a.ScheduleDate < b.ScheduleDate
		}
		if refs[i].owner != refs[j].owner {
			return refs[i].owner < refs[j].owner
		}
		return refs[i].id < refs[j].id
	})
	if len(refs) > limit {
		refs = refs[:limit]
	}
	out := make([]domain.ScheduledMessage, 0, len(refs))
	for _, key := range refs {
		item := s.scheduledMessages[key.owner][key.id]
		item.message.State = "dispatching"
		item.message.Generation++
		item.message.UpdatedAt = now
		item.leaseUntil = now + leaseSeconds
		item.lastError = ""
		s.scheduledMessages[key.owner][key.id] = item
		out = append(out, cloneScheduledMessage(item.message))
	}
	return out, nil
}

func (s *MessageStore) ReleaseScheduledMessage(_ context.Context, ownerUserID int64, id int, generation int64, errText string) error {
	if s == nil || ownerUserID == 0 || id <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.scheduledMessages[ownerUserID][id]
	if !ok || item.message.State != "dispatching" || item.message.Generation != generation {
		return nil
	}
	item.message.State = "pending"
	item.message.UpdatedAt = int(time.Now().Unix())
	item.leaseUntil = 0
	item.lastError = errText
	s.scheduledMessages[ownerUserID][id] = item
	return nil
}

// checkScheduledDeliveryLocked mirrors the PostgreSQL row fence: a cancelled,
// edited, released or reclaimed message cannot be sent by a stale claim.
// Caller holds s.mu.
func (s *MessageStore) checkScheduledDeliveryLocked(claim *domain.ScheduledDelivery, owner int64, peer domain.Peer, randomID int64) error {
	if claim == nil {
		return nil
	}
	if claim.OwnerUserID != owner || claim.ID <= 0 || claim.Generation <= 0 {
		return domain.ErrScheduledClaimLost
	}
	if claim.Payload == nil {
		return store.ErrDeliveryOutboxRequired
	}
	item, ok := s.scheduledMessages[owner][claim.ID]
	if !ok || item.message.State != "dispatching" || item.message.Generation != claim.Generation ||
		item.message.Peer != peer || item.message.RandomID != randomID {
		return domain.ErrScheduledClaimLost
	}
	return nil
}

// memoryScheduledCompletion is staged before the send publishes anything and
// applied with it, like the PostgreSQL send transaction.
type memoryScheduledCompletion struct {
	owner  int64
	sent   memoryScheduledMessage
	next   *memoryScheduledMessage
	effect store.DeliveryEffect
}

// stageScheduledCompletionLocked builds the sent transition, any repeat
// occurrence and the owner's durable delivery. Caller holds s.mu.
func (s *MessageStore) stageScheduledCompletionLocked(claim *domain.ScheduledDelivery, sentID, date int) (*memoryScheduledCompletion, error) {
	if claim == nil {
		return nil, nil
	}
	if sentID <= 0 {
		return nil, fmt.Errorf("scheduled delivery missing sent id")
	}
	owner := s.scheduledMessages[claim.OwnerUserID]
	sent := owner[claim.ID]
	sent.message = cloneScheduledMessage(sent.message)
	sent.message.State = "sent"
	sent.message.SentMessageID = sentID
	sent.message.UpdatedAt = date
	sent.leaseUntil = 0
	stage := &memoryScheduledCompletion{owner: claim.OwnerUserID, sent: sent}
	var next *domain.ScheduledMessage
	if period := sent.message.ScheduleRepeatPeriod; period > 0 {
		if !domain.ValidScheduleRepeat(period) || int64(date)+int64(period) >= domain.ScheduleWhenOnline {
			return nil, domain.ErrScheduleInvalid
		}
		// The memory store does not model Premium expiry; RPC admission already
		// required Premium for a repeating schedule.
		nextID, err := nextMemoryScheduledID(owner)
		if err != nil {
			return nil, err
		}
		msg := cloneScheduledMessage(sent.message)
		msg.ID, msg.RandomID = nextID, memoryScheduledOccurrenceRandomID(claim.OwnerUserID, nextID, date)
		msg.ScheduleDate, msg.State, msg.SentMessageID = date+period, "pending", 0
		msg.Generation, msg.ManualDispatch, msg.CreatedAt, msg.UpdatedAt = 0, false, date, date
		stage.next = &memoryScheduledMessage{message: msg}
		next = &msg
	}
	var nextCopy *domain.ScheduledMessage
	if next != nil {
		copied := cloneScheduledMessage(*next)
		nextCopy = &copied
	}
	payload, err := claim.Payload(domain.ScheduledCompletion{Sent: cloneScheduledMessage(sent.message), Next: nextCopy})
	if err != nil {
		return nil, fmt.Errorf("build scheduled completion delivery: %w", err)
	}
	stage.effect = store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: claim.OwnerUserID, ExcludeAuthKeyID: claim.ExcludeAuthKeyID, ExcludeSessionID: claim.ExcludeSessionID,
		Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})
	if err := stage.effect.Validate(); err != nil {
		return nil, err
	}
	if s.deliveryOutbox == nil {
		return nil, store.ErrDeliveryOutboxRequired
	}
	return stage, nil
}

// publishScheduledCompletionLocked applies a staged completion. Caller holds
// s.mu and the dialog lock (private send aggregate) and has already enqueued
// stage.effect.
func (s *MessageStore) publishScheduledCompletionLocked(stage *memoryScheduledCompletion) *domain.ScheduledMessage {
	if stage == nil {
		return nil
	}
	owner := s.scheduledMessages[stage.owner]
	owner[stage.sent.message.ID] = stage.sent
	var next *domain.ScheduledMessage
	if stage.next != nil {
		owner[stage.next.message.ID] = *stage.next
		copied := cloneScheduledMessage(stage.next.message)
		next = &copied
	}
	if peer := stage.sent.message.Peer; s.dialogs != nil && peer.Type == domain.PeerTypeUser {
		s.setMemoryHasScheduledStateLocked(stage.owner, peer, s.hasMemoryScheduledLocked(stage.owner, peer))
	}
	return next
}

func nextMemoryScheduledID(owner map[int]memoryScheduledMessage) (int, error) {
	nextID := 1
	for id := range owner {
		if id >= nextID {
			nextID = id + 1
		}
	}
	if nextID > domain.MaxMessageBoxID {
		return 0, fmt.Errorf("create scheduled message: id space exhausted")
	}
	return nextID, nil
}

// memoryScheduledOccurrenceRandomID derives a stable non-zero identity for a
// repeat occurrence; PostgreSQL draws it from crypto/rand.
func memoryScheduledOccurrenceRandomID(owner int64, id, date int) int64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, value := range []int64{owner, int64(id), int64(date)} {
		binary.LittleEndian.PutUint64(buf[:], uint64(value))
		_, _ = h.Write(buf[:])
	}
	if v := int64(h.Sum64() & 0x7fffffffffffffff); v != 0 {
		return v
	}
	return 1
}

func (s *MessageStore) HasScheduledMessages(_ context.Context, ownerUserID int64, peer domain.Peer) (bool, error) {
	if s == nil || ownerUserID == 0 || peer.ID == 0 {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasMemoryScheduledLocked(ownerUserID, peer), nil
}

func (s *MessageStore) hasMemoryScheduledLocked(ownerUserID int64, peer domain.Peer) bool {
	for _, item := range s.scheduledMessages[ownerUserID] {
		if item.message.Peer == peer && memoryScheduledActive(item.message.State) {
			return true
		}
	}
	return false
}

func (s *MessageStore) refreshMemoryHasScheduledLocked(ownerUserID int64, peer domain.Peer) {
	s.setMemoryHasScheduledLocked(ownerUserID, peer, s.hasMemoryScheduledLocked(ownerUserID, peer))
}

func (s *MessageStore) setMemoryHasScheduledLocked(ownerUserID int64, peer domain.Peer, value bool) {
	if s.dialogs == nil || peer.Type != domain.PeerTypeUser {
		return
	}
	s.dialogs.mu.Lock()
	defer s.dialogs.mu.Unlock()
	s.setMemoryHasScheduledStateLocked(ownerUserID, peer, value)
}

func (s *MessageStore) setMemoryHasScheduledStateLocked(ownerUserID int64, peer domain.Peer, value bool) {
	list := s.dialogs.m[ownerUserID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer == peer {
			list.Dialogs[i].HasScheduled = value
			s.dialogs.m[ownerUserID] = list
			return
		}
	}
	if value {
		list.Dialogs = append(list.Dialogs, domain.Dialog{Peer: peer, HasScheduled: true})
		s.dialogs.m[ownerUserID] = list
	}
}

func memoryScheduledActive(state string) bool { return state == "pending" || state == "dispatching" }

func cloneScheduledMessage(msg domain.ScheduledMessage) domain.ScheduledMessage {
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	msg.Media = cloneRequestedPeerMedia(msg.Media)
	msg.RichMessage = cloneRichMessage(msg.RichMessage)
	msg.ReplyTo = cloneMessageReply(msg.ReplyTo)
	msg.Forward = cloneMessageForward(msg.Forward)
	msg.SendAs = clonePeerPtr(msg.SendAs)
	return msg
}

func cloneScheduledMessages(messages []domain.ScheduledMessage) []domain.ScheduledMessage {
	out := make([]domain.ScheduledMessage, len(messages))
	for i := range messages {
		out[i] = cloneScheduledMessage(messages[i])
	}
	return out
}

func clonePeerPtr(peer *domain.Peer) *domain.Peer {
	if peer == nil {
		return nil
	}
	clone := *peer
	return &clone
}

func memoryScheduledHash(messages []domain.ScheduledMessage) int64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, msg := range messages {
		for _, value := range []int64{int64(msg.ID), msg.Peer.ID, int64(msg.ScheduleDate), int64(msg.UpdatedAt)} {
			binary.LittleEndian.PutUint64(buf[:], uint64(value))
			_, _ = h.Write(buf[:])
		}
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}
