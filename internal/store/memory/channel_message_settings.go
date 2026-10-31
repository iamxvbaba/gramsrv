package memory

import (
	"context"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) SetPreHistoryHidden(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.PreHistoryHidden
	channel.PreHistoryHidden = enabled
	s.channels[channelID] = channel
	if prev && !enabled {
		// Same convention as postgres: disabling hidden history must restore member read
		// bounds. available_min_id only ever increases (upsert/batch/clear history are all
		// GREATEST), so without an explicit reset a member who joined while history was
		// hidden would see an empty channel forever and re-joining cannot rescue them
		// (GREATEST(old, 0) = old). Only recycle bounds that came purely from prehistory:
		// bounds carrying a history_clear_anchor come from the member's own "clear history"
		// and are owner-local semantics, not rewritten by group settings.
		for uid, m := range s.members[channelID] {
			if m.AvailableMinID > 0 && m.HistoryClearAnchorID == 0 {
				m.AvailableMinID = 0
				s.members[channelID][uid] = m
			}
		}
	}
	if prev != enabled {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogTogglePreHistoryHidden,
			PrevBool:  prev,
			NewBool:   enabled,
		})
	}
	return channel, nil
}

func (s *ChannelStore) SetPaidMessagesPrice(_ context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error) {
	if userID == 0 || channelID == 0 || stars < 0 {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.ChannelPaidMessagesPriceResult{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelAdminRequired
	}
	out := domain.ChannelPaidMessagesPriceResult{}
	prevAllowed := channel.BroadcastMessagesAllowed
	prevStars := channel.SendPaidMessagesStars
	prevLinked := channel.LinkedMonoforumID
	channel.SendPaidMessagesStars = stars
	channel.BroadcastMessagesAllowed = channel.Broadcast && broadcastMessagesAllowed
	changed := channel.Broadcast && (prevAllowed != channel.BroadcastMessagesAllowed || prevStars != stars || (channel.BroadcastMessagesAllowed && prevLinked == 0))
	// When enabling channel DMs, associate a monoforum virtual channel (matching postgres); if one already exists, only sync the price.
	if channel.BroadcastMessagesAllowed {
		if channel.LinkedMonoforumID == 0 {
			monoID := s.nextChannelIDLocked()
			s.channels[monoID] = domain.Channel{
				ID:            monoID,
				AccessHash:    s.nextAccessHashLocked(),
				CreatorUserID: channel.CreatorUserID,
				Title:         channel.Title,
				// Keep monoforum as a megagroup-like saved sublist container. TDesktop hangs when
				// a monoforum peer is projected as both broadcast and megagroup.
				Megagroup:             true,
				Monoforum:             true,
				LinkedMonoforumID:     channelID,
				SendPaidMessagesStars: stars,
				Date:                  int(time.Now().Unix()),
			}
			channel.LinkedMonoforumID = monoID
		} else if mono, ok := s.channels[channel.LinkedMonoforumID]; ok {
			// Reusing an existing monoforum only syncs the price. A mono is always created
			// as a megagroup and no path flips it into broadcast/forum, so there is no need
			// to normalize kind again (that is dead code that fixed historical data from a
			// never-shipped experimental broadcast+monoforum shape). Consistent with postgres.
			mono.SendPaidMessagesStars = stars
			s.channels[channel.LinkedMonoforumID] = mono
		}
		// The monoforum's first (and only) service message is the creation message
		// (messageActionChannelCreate), which the client renders for a monoforum as
		// "Direct messages were enabled in this channel.". The paid_messages_price for
		// toggle/price changes goes only into the parent broadcast channel (rendered as
		// "Channel enabled/disabled Direct Messages") and **never into the mono** -- the
		// mono is a megagroup (not a broadcast), so the same action would be rendered as the
		// wrong "messages are free / set to N stars" text; disabling relies solely on the
		// monoforumDisabled state to show the disabled footer. Consistent with postgres.
		if existing := s.channels[channel.LinkedMonoforumID]; existing.ID != 0 && existing.TopMessageID == 0 {
			createMsg, createEvent := s.appendChannelServiceMessageLocked(channel.LinkedMonoforumID, userID, int(time.Now().Unix()), domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: existing.Title})
			mono := s.channels[channel.LinkedMonoforumID]
			mono.TopMessageID = createMsg.ID
			mono.Pts = createEvent.Pts
			s.channels[channel.LinkedMonoforumID] = mono
			out.ServiceMessages = append(out.ServiceMessages, domain.SendChannelMessageResult{Channel: cloneChannel(mono), Message: cloneChannelMessage(createMsg), Event: cloneChannelEvent(createEvent)})
		}
	}
	// The monoforum mirrors the parent channel's DM-enabled state into its own
	// BroadcastMessagesAllowed (consistent with postgres): projection uses it to decide
	// whether to send the mono's linked_monoforum_id, and when disabled both sides hide the
	// link, so re-opening the monoforum and refetching does not clear MonoforumDisabled and
	// the disabled footer persists. The internal association row is kept so re-enabling can reuse it.
	if channel.LinkedMonoforumID != 0 {
		if mono, ok := s.channels[channel.LinkedMonoforumID]; ok {
			mono.BroadcastMessagesAllowed = channel.BroadcastMessagesAllowed
			s.channels[channel.LinkedMonoforumID] = mono
		}
	}
	if changed && channel.LinkedMonoforumID != 0 {
		action := domain.ChannelMessageAction{
			Type:                     domain.ChannelActionPaidMessagesPrice,
			BroadcastMessagesAllowed: channel.BroadcastMessagesAllowed,
			Stars:                    stars,
		}
		parentMsg, parentEvent := s.appendChannelServiceMessageLocked(channelID, userID, int(time.Now().Unix()), action)
		channel.TopMessageID = parentMsg.ID
		channel.Pts = parentEvent.Pts
		out.ServiceMessages = append(out.ServiceMessages, domain.SendChannelMessageResult{Channel: cloneChannel(channel), Message: cloneChannelMessage(parentMsg), Event: cloneChannelEvent(parentEvent)})
	}
	if len(out.ServiceMessages) > 0 {
		out.ServiceMessage = &out.ServiceMessages[0]
	}
	s.channels[channelID] = channel
	out.Channel = cloneChannel(channel)
	return out, nil
}
