package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/domain"
)

const suggestedPostSettlementAge = 24 * 60 * 60

type memorySuggestedPostKey struct {
	monoforumID int64
	messageID   int
}

type memorySuggestedPostApproval struct {
	actorUserID        int64
	parentID           int64
	savedPeer          domain.Peer
	state              domain.SuggestedPostLifecycleState
	price              *domain.SuggestedPostPrice
	scheduleDate       int
	publishedMessageID int
	settlementDue      int
	lastResult         domain.ToggleSuggestedPostApprovalResult
}

func (s *ChannelStore) ToggleSuggestedPostApproval(_ context.Context, req domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error) {
	if req.UserID == 0 || req.MonoforumID == 0 || req.MessageID <= 0 || (!req.Reject && strings.TrimSpace(req.RejectComment) != "") {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toggleSuggestedPostApprovalLocked(req)
}

func (s *ChannelStore) toggleSuggestedPostApprovalLocked(req domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error) {
	mono, ok := s.channels[req.MonoforumID]
	if !ok || mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	parent, ok := s.channels[mono.LinkedMonoforumID]
	if !ok || parent.Deleted || !parent.Broadcast || parent.LinkedMonoforumID != mono.ID {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	idx := -1
	var original domain.ChannelMessage
	for i := range s.messages[mono.ID] {
		candidate := s.messages[mono.ID][i]
		if candidate.ID == req.MessageID && !candidate.Deleted {
			idx, original = i, cloneChannelMessage(candidate)
			break
		}
	}
	if idx < 0 || original.SavedPeer.Type != domain.PeerTypeUser || original.SavedPeer.ID == 0 || original.SuggestedPost == nil {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	manager := s.members[parent.ID][req.UserID]
	fromSubscriber := original.From.Type == domain.PeerTypeUser
	if fromSubscriber {
		if !manager.CanManageDirectMessages() || (!req.Reject && !manager.CanPostChannelMessages()) {
			return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostApprovalForbidden
		}
	} else if req.UserID != original.SavedPeer.ID {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostApprovalForbidden
	}
	key := memorySuggestedPostKey{monoforumID: mono.ID, messageID: original.ID}
	approval, exists := s.suggestedPostApprovals[key]
	if exists && approval.state != domain.SuggestedPostStateBalanceLow {
		out := cloneSuggestedPostResult(approval.lastResult)
		out.Duplicate = true
		return out, nil
	}
	if original.SuggestedPost.Accepted || original.SuggestedPost.Rejected {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostAlreadyHandled
	}
	price := cloneSuggestedPostPrice(original.SuggestedPost.Price)
	scheduleDate := original.SuggestedPost.ScheduleDate
	if req.ScheduleDate > 0 {
		scheduleDate = req.ScheduleDate
	}
	if !req.Reject && scheduleDate > 0 && (scheduleDate < req.Date+5*60 || scheduleDate > req.Date+31*24*60*60) {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	recipients := s.monoforumRecipientsLocked(parent.ID, original.SavedPeer.ID)
	base := domain.ToggleSuggestedPostApprovalResult{
		Monoforum: cloneChannel(mono), Parent: cloneChannel(parent), SavedPeer: original.SavedPeer,
		State: domain.SuggestedPostStateBalanceLow, Recipients: recipients,
	}
	if req.Reject {
		original.SuggestedPost.Rejected = true
		original.SuggestedPost.Accepted = false
		original.Pts = s.nextChannelPtsLocked(mono.ID)
		s.messages[mono.ID][idx] = cloneChannelMessage(original)
		edit := domain.ChannelUpdateEvent{ChannelID: mono.ID, Type: domain.ChannelUpdateEditMessage, Pts: original.Pts, PtsCount: 1, Date: req.Date, Message: cloneChannelMessage(original), SenderUserID: req.UserID}
		s.appendChannelEventLocked(edit)
		service, serviceEvent := s.appendSuggestedPostServiceLocked(mono, parent, req.UserID, original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{
			Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostRejected: true,
			SuggestedPostRejectComment: strings.TrimSpace(req.RejectComment), SuggestedPostPrice: price,
		})
		mono = s.channels[mono.ID]
		base.Monoforum, base.State = cloneChannel(mono), domain.SuggestedPostStateRejected
		base.OriginalMessage, base.OriginalEvent = cloneChannelMessage(original), cloneChannelEvent(edit)
		base.ServiceMessage, base.ServiceEvent = cloneChannelMessage(service), cloneChannelEvent(serviceEvent)
		approval = memorySuggestedPostApproval{actorUserID: req.UserID, parentID: parent.ID, savedPeer: original.SavedPeer, state: base.State, price: price, lastResult: cloneSuggestedPostResult(base)}
		s.suggestedPostApprovals[key] = approval
		return base, nil
	}

	starsBalance, tonBalance, enough := s.reserveSuggestedPostPaymentLocked(original.SavedPeer.ID, parent.ID, price)
	if !enough {
		if exists {
			out := cloneSuggestedPostResult(approval.lastResult)
			out.PayerStarsBalance, out.PayerTONBalance = starsBalance, tonBalance
			out.Duplicate = true
			return out, nil
		}
		service, serviceEvent := s.appendSuggestedPostServiceLocked(mono, parent, req.UserID, original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{
			Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostBalanceTooLow: true,
			SuggestedPostScheduleDate: scheduleDate, SuggestedPostPrice: price,
		})
		base.Monoforum = cloneChannel(s.channels[mono.ID])
		base.ServiceMessage, base.ServiceEvent = cloneChannelMessage(service), cloneChannelEvent(serviceEvent)
		base.PayerStarsBalance, base.PayerTONBalance = starsBalance, tonBalance
		approval = memorySuggestedPostApproval{actorUserID: req.UserID, parentID: parent.ID, savedPeer: original.SavedPeer, state: base.State, price: price, scheduleDate: scheduleDate, lastResult: cloneSuggestedPostResult(base)}
		s.suggestedPostApprovals[key] = approval
		return base, nil
	}

	original.SuggestedPost.Accepted = true
	original.SuggestedPost.Rejected = false
	effectivePublishDate := scheduleDate
	if effectivePublishDate == 0 {
		// TDesktop deliberately omits schedule_date for "Publish Now", but
		// renders the approval service action as an absolute date.  Persist one
		// effective publication timestamp across the edited suggestion, action
		// and approval record instead of leaking an accepted zero date.
		effectivePublishDate = req.Date
	}
	original.SuggestedPost.ScheduleDate = effectivePublishDate
	original.Pts = s.nextChannelPtsLocked(mono.ID)
	s.messages[mono.ID][idx] = cloneChannelMessage(original)
	edit := domain.ChannelUpdateEvent{ChannelID: mono.ID, Type: domain.ChannelUpdateEditMessage, Pts: original.Pts, PtsCount: 1, Date: req.Date, Message: cloneChannelMessage(original), SenderUserID: req.UserID}
	s.appendChannelEventLocked(edit)
	service, serviceEvent := s.appendSuggestedPostServiceLocked(mono, parent, req.UserID, original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{
		Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostScheduleDate: effectivePublishDate, SuggestedPostPrice: price,
	})
	base.Monoforum, base.OriginalMessage, base.OriginalEvent = cloneChannel(s.channels[mono.ID]), cloneChannelMessage(original), cloneChannelEvent(edit)
	base.ServiceMessage, base.ServiceEvent = cloneChannelMessage(service), cloneChannelEvent(serviceEvent)
	base.PayerStarsBalance, base.PayerTONBalance = starsBalance, tonBalance
	base.State = domain.SuggestedPostStateScheduled
	approval = memorySuggestedPostApproval{actorUserID: req.UserID, parentID: parent.ID, savedPeer: original.SavedPeer, state: base.State, price: price, scheduleDate: effectivePublishDate}
	if effectivePublishDate <= req.Date {
		published := s.publishSuggestedPostLocked(parent, original, req.UserID, req.Date)
		base.Published = &published
		approval.publishedMessageID = published.Message.ID
		if price == nil {
			base.State = domain.SuggestedPostStateCompleted
		} else {
			base.State = domain.SuggestedPostStatePublished
			approval.settlementDue = req.Date + suggestedPostSettlementAge
		}
		approval.state = base.State
	}
	approval.lastResult = cloneSuggestedPostResult(base)
	s.suggestedPostApprovals[key] = approval
	return base, nil
}

func (s *ChannelStore) ProcessSuggestedPostLifecycle(_ context.Context, req domain.SuggestedPostLifecycleRequest) ([]domain.ToggleSuggestedPostApprovalResult, error) {
	if req.Now == 0 {
		req.Now = int(time.Now().Unix())
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ToggleSuggestedPostApprovalResult, 0)
	for key, approval := range s.suggestedPostApprovals {
		if len(out) >= req.Limit {
			break
		}
		if approval.state != domain.SuggestedPostStateScheduled && approval.state != domain.SuggestedPostStatePublished {
			continue
		}
		mono, monoOK := s.channels[key.monoforumID]
		parent, parentOK := s.channels[approval.parentID]
		if !monoOK || !parentOK {
			return out, fmt.Errorf("suggested post lifecycle invariant: missing monoforum %d or parent %d", key.monoforumID, approval.parentID)
		}
		if mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID != parent.ID || parent.Deleted || !parent.Broadcast || parent.LinkedMonoforumID != mono.ID {
			return out, fmt.Errorf("suggested post lifecycle invariant: broken monoforum link %d <-> %d", mono.ID, parent.ID)
		}
		if approval.scheduleDate <= 0 {
			return out, fmt.Errorf("suggested post lifecycle invariant: state %s has zero publish date", approval.state)
		}
		var original domain.ChannelMessage
		originalFound := false
		for _, message := range s.messages[mono.ID] {
			if message.ID == key.messageID {
				original = cloneChannelMessage(message)
				originalFound = true
				break
			}
		}
		if !originalFound || original.SuggestedPost == nil || !original.SuggestedPost.Accepted || original.SuggestedPost.Rejected {
			return out, fmt.Errorf("suggested post lifecycle invariant: missing or invalid accepted suggestion %d/%d", mono.ID, key.messageID)
		}
		result := domain.ToggleSuggestedPostApprovalResult{Monoforum: cloneChannel(mono), Parent: cloneChannel(parent), SavedPeer: approval.savedPeer, State: approval.state, Recipients: s.monoforumRecipientsLocked(parent.ID, approval.savedPeer.ID)}
		changed := false
		if approval.state == domain.SuggestedPostStateScheduled && original.Deleted {
			if approval.price != nil {
				s.refundSuggestedPostPaymentLocked(approval.savedPeer.ID, approval.price)
				service, event := s.appendSuggestedPostServiceLocked(mono, parent, approval.actorUserID, approval.savedPeer, key.messageID, req.Now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund})
				result.ServiceMessage, result.ServiceEvent = service, event
			}
			approval.state, result.State, changed = domain.SuggestedPostStateRefunded, domain.SuggestedPostStateRefunded, true
		}
		if approval.state == domain.SuggestedPostStateScheduled && approval.scheduleDate <= req.Now {
			published := s.publishSuggestedPostLocked(parent, original, approval.actorUserID, req.Now)
			result.Published = &published
			approval.publishedMessageID = published.Message.ID
			if approval.price == nil {
				approval.state = domain.SuggestedPostStateCompleted
			} else {
				approval.state = domain.SuggestedPostStatePublished
				approval.settlementDue = req.Now + suggestedPostSettlementAge
			}
			result.State, changed = approval.state, true
		}
		if approval.state == domain.SuggestedPostStatePublished {
			if approval.price == nil || approval.publishedMessageID <= 0 || approval.settlementDue <= 0 {
				return out, fmt.Errorf("suggested post lifecycle invariant: incomplete published state %d/%d", mono.ID, key.messageID)
			}
			deleted := false
			publishedFound := false
			for _, message := range s.messages[parent.ID] {
				if message.ID == approval.publishedMessageID {
					deleted = message.Deleted
					publishedFound = true
					break
				}
			}
			if !publishedFound {
				return out, fmt.Errorf("suggested post lifecycle invariant: missing published message %d/%d", parent.ID, approval.publishedMessageID)
			}
			deleteDate := s.channelMessageDeleteDateLocked(parent.ID, approval.publishedMessageID)
			if deleted && (deleteDate == 0 || deleteDate < approval.settlementDue) {
				s.refundSuggestedPostPaymentLocked(approval.savedPeer.ID, approval.price)
				service, event := s.appendSuggestedPostServiceLocked(mono, parent, approval.actorUserID, approval.savedPeer, key.messageID, req.Now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund})
				result.ServiceMessage, result.ServiceEvent = service, event
				approval.state, result.State, changed = domain.SuggestedPostStateRefunded, domain.SuggestedPostStateRefunded, true
			} else if approval.settlementDue <= req.Now {
				s.settleSuggestedPostPaymentLocked(parent.ID, approval.price)
				service, event := s.appendSuggestedPostServiceLocked(mono, parent, approval.actorUserID, approval.savedPeer, key.messageID, req.Now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostSuccess, SuggestedPostPrice: cloneSuggestedPostPrice(approval.price)})
				result.ServiceMessage, result.ServiceEvent = service, event
				approval.state, result.State, changed = domain.SuggestedPostStateCompleted, domain.SuggestedPostStateCompleted, true
			}
		}
		if changed {
			result.Monoforum, result.Parent = cloneChannel(s.channels[mono.ID]), cloneChannel(s.channels[parent.ID])
			approval.lastResult = cloneSuggestedPostResult(result)
			s.suggestedPostApprovals[key] = approval
			out = append(out, result)
		}
	}
	return out, nil
}

func (s *ChannelStore) channelMessageDeleteDateLocked(channelID int64, messageID int) int {
	for i := len(s.events[channelID]) - 1; i >= 0; i-- {
		event := s.events[channelID][i]
		if event.Type != domain.ChannelUpdateDeleteMessages {
			continue
		}
		for _, id := range event.MessageIDs {
			if id == messageID {
				return event.Date
			}
		}
	}
	return 0
}

func (s *ChannelStore) reserveSuggestedPostPaymentLocked(payerID, parentID int64, price *domain.SuggestedPostPrice) (*domain.StarsBalance, *int64, bool) {
	if price == nil {
		return nil, nil, true
	}
	switch price.Kind {
	case domain.SuggestedPostPriceStars:
		current, ok := s.starsBalances[payerID]
		if !ok {
			current = domain.DefaultStarsStartingGrant
		}
		balance := &domain.StarsBalance{UserID: payerID, Balance: current, Granted: true}
		if price.Nanos != 0 || current < price.Amount {
			return balance, nil, false
		}
		current -= price.Amount
		s.starsBalances[payerID] = current
		balance.Balance = current
		return balance, nil, true
	case domain.SuggestedPostPriceTON:
		current := s.tonBalances[payerID]
		balance := current
		if current < price.Amount {
			return nil, &balance, false
		}
		current -= price.Amount
		s.tonBalances[payerID] = current
		balance = current
		return nil, &balance, true
	default:
		return nil, nil, false
	}
}

func (s *ChannelStore) refundSuggestedPostPaymentLocked(payerID int64, price *domain.SuggestedPostPrice) {
	if price == nil {
		return
	}
	if price.Kind == domain.SuggestedPostPriceStars {
		s.starsBalances[payerID] += price.Amount
	} else if price.Kind == domain.SuggestedPostPriceTON {
		s.tonBalances[payerID] += price.Amount
	}
}

func (s *ChannelStore) settleSuggestedPostPaymentLocked(parentID int64, price *domain.SuggestedPostPrice) {
	if price == nil {
		return
	}
	credit := price.Amount * paidMessageChannelCommissionPermille / 1000
	if price.Kind == domain.SuggestedPostPriceStars {
		s.channelStarsBalances[parentID] += credit
	} else if price.Kind == domain.SuggestedPostPriceTON {
		s.channelTONBalances[parentID] += credit
	}
}

func (s *ChannelStore) appendSuggestedPostServiceLocked(mono, parent domain.Channel, actor int64, saved domain.Peer, replyID, date int, action domain.ChannelMessageAction) (domain.ChannelMessage, domain.ChannelUpdateEvent) {
	pts := s.nextChannelPtsLocked(mono.ID)
	from := domain.Peer{Type: domain.PeerTypeUser, ID: actor}
	if member, ok := s.members[parent.ID][actor]; ok && member.CanManageDirectMessages() {
		from = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}
	}
	msg := domain.ChannelMessage{ChannelID: mono.ID, ID: s.nextChannelMessageIDLocked(mono.ID), SenderUserID: actor, From: from, SavedPeer: saved, Date: date, Action: cloneChannelMessageAction(&action), ReplyTo: &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: mono.ID}, MessageID: replyID}, Pts: pts}
	event := domain.ChannelUpdateEvent{ChannelID: mono.ID, Type: domain.ChannelUpdateNewMessage, Pts: pts, PtsCount: 1, Date: date, Message: cloneChannelMessage(msg), SenderUserID: actor}
	s.messages[mono.ID] = append(s.messages[mono.ID], cloneChannelMessage(msg))
	s.appendChannelEventLocked(event)
	mono.TopMessageID, mono.Pts = msg.ID, pts
	s.channels[mono.ID] = mono
	return cloneChannelMessage(msg), cloneChannelEvent(event)
}

func (s *ChannelStore) publishSuggestedPostLocked(parent domain.Channel, original domain.ChannelMessage, actor int64, date int) domain.SendChannelMessageResult {
	msg := cloneChannelMessage(original)
	msg.ChannelID, msg.ID, msg.RandomID, msg.SenderUserID = parent.ID, s.nextChannelMessageIDLocked(parent.ID), 0, actor
	msg.From, msg.SavedPeer, msg.Date, msg.EditDate, msg.Post = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}, domain.Peer{}, date, 0, true
	msg.ReplyTo, msg.PaidMessageStars, msg.Pts, msg.Deleted = nil, 0, s.nextChannelPtsLocked(parent.ID), false
	event := domain.ChannelUpdateEvent{ChannelID: parent.ID, Type: domain.ChannelUpdateNewMessage, Pts: msg.Pts, PtsCount: 1, Date: date, Message: cloneChannelMessage(msg), SenderUserID: actor}
	s.messages[parent.ID] = append(s.messages[parent.ID], cloneChannelMessage(msg))
	s.appendChannelEventLocked(event)
	parent.TopMessageID, parent.Pts = msg.ID, msg.Pts
	s.channels[parent.ID] = parent
	recipients := make([]int64, 0, len(s.members[parent.ID]))
	for id, member := range s.members[parent.ID] {
		if member.Status == domain.ChannelMemberActive {
			recipients = append(recipients, id)
		}
	}
	return domain.SendChannelMessageResult{Channel: cloneChannel(parent), Message: cloneChannelMessage(msg), Event: cloneChannelEvent(event), Recipients: uniqueNonZero(recipients, 0)}
}

func (s *ChannelStore) monoforumRecipientsLocked(parentID, subscriberID int64) []int64 {
	ids := []int64{subscriberID}
	for id, member := range s.members[parentID] {
		if member.CanManageDirectMessages() {
			ids = append(ids, id)
		}
	}
	return uniqueNonZero(ids, 0)
}

func cloneSuggestedPostPrice(in *domain.SuggestedPostPrice) *domain.SuggestedPostPrice {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneSuggestedPostResult(in domain.ToggleSuggestedPostApprovalResult) domain.ToggleSuggestedPostApprovalResult {
	in.Monoforum, in.Parent = cloneChannel(in.Monoforum), cloneChannel(in.Parent)
	in.OriginalMessage, in.ServiceMessage = cloneChannelMessage(in.OriginalMessage), cloneChannelMessage(in.ServiceMessage)
	in.OriginalEvent, in.ServiceEvent = cloneChannelEvent(in.OriginalEvent), cloneChannelEvent(in.ServiceEvent)
	in.Recipients = append([]int64(nil), in.Recipients...)
	if in.Published != nil {
		p := *in.Published
		p.Message = cloneChannelMessage(p.Message)
		p.Event = cloneChannelEvent(p.Event)
		p.Recipients = append([]int64(nil), p.Recipients...)
		in.Published = &p
	}
	if in.PayerStarsBalance != nil {
		b := *in.PayerStarsBalance
		in.PayerStarsBalance = &b
	}
	if in.PayerTONBalance != nil {
		b := *in.PayerTONBalance
		in.PayerTONBalance = &b
	}
	return in
}
