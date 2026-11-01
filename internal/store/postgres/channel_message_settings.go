package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ChannelStore) SetPreHistoryHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	var channel domain.Channel
	if err := withTx(ctx, s.db, "toggle channel prehistory", func(tx pgx.Tx) error {
		var err error
		var member domain.ChannelMember
		channel, member, err = s.getChannelForMember(ctx, tx, userID, channelID)
		if err != nil {
			return err
		}
		if member.Role != domain.ChannelRoleCreator {
			return domain.ErrChannelAdminRequired
		}
		prev := channel.PreHistoryHidden
		if _, err := tx.Exec(ctx, `UPDATE channels SET pre_history_hidden = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
			return fmt.Errorf("update channel prehistory: %w", err)
		}
		channel.PreHistoryHidden = enabled
		if prev && !enabled {
			// Disabling "restrict new members from viewing history" must return the member-side read boundary. All writes to available_min_id
			// (upsert / batch / clear history) are monotonically increasing GREATEST, and no write can lower it;
			// without an explicit zeroing here, a member who "joined while history was hidden" would permanently see an empty channel, and rejoining
			// would not fix it either (rejoining computes minID=0, but the upsert takes GREATEST(old value,0)=old value).
			// Only reset the purely prehistory-sourced boundary: history_clear_anchor_id>0 means the boundary came from the member's
			// own "clear history", which has owner-local semantics and must not be rewritten by a group setting.
			if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET available_min_id = 0,
    updated_at = now()
WHERE channel_id = $1
  AND available_min_id > 0
  AND COALESCE(history_clear_anchor_id, 0) = 0`, channelID); err != nil {
				return fmt.Errorf("release prehistory member boundaries: %w", err)
			}
			if _, err := tx.Exec(ctx, `
UPDATE user_channel_member_index
SET available_min_id = 0,
    history_clear_updated_at = extract(epoch from now())::int,
    updated_at = now()
WHERE channel_id = $1
  AND available_min_id > 0
  AND COALESCE(history_clear_anchor_id, 0) = 0`, channelID); err != nil {
				return fmt.Errorf("release prehistory member index boundaries: %w", err)
			}
		}
		if prev != enabled {
			if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
				ChannelID: channelID,
				UserID:    userID,
				Date:      nowUnix(),
				Type:      domain.ChannelAdminLogTogglePreHistoryHidden,
				PrevBool:  prev,
				NewBool:   enabled,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return domain.Channel{}, err
	}
	return channel, nil
}

func (s *ChannelStore) SetPaidMessagesPrice(ctx context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error) {
	if userID == 0 || channelID == 0 || stars < 0 {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelInvalid
	}
	var channel domain.Channel
	services := make([]domain.SendChannelMessageResult, 0, 2)
	if err := withTx(ctx, s.db, "update channel paid messages price", func(tx pgx.Tx) error {
		var err error
		var member domain.ChannelMember
		channel, member, err = s.getChannelForMember(ctx, tx, userID, channelID)
		if err != nil {
			return err
		}
		if !canChangeChannelInfo(member) {
			return domain.ErrChannelAdminRequired
		}
		prevAllowed := channel.BroadcastMessagesAllowed
		prevStars := channel.SendPaidMessagesStars
		prevLinked := channel.LinkedMonoforumID
		broadcastAllowed := channel.Broadcast && broadcastMessagesAllowed
		if _, err := tx.Exec(ctx, `UPDATE channels SET send_paid_messages_stars = $2, broadcast_messages_allowed = $3, updated_at = now() WHERE id = $1`, channelID, stars, broadcastAllowed); err != nil {
			return fmt.Errorf("update channel paid messages price: %w", err)
		}
		channel.SendPaidMessagesStars = stars
		channel.BroadcastMessagesAllowed = broadcastAllowed
		changed := channel.Broadcast && (prevAllowed != broadcastAllowed || prevStars != stars || (broadcastAllowed && prevLinked == 0))
		// Enabling channel Direct Messages links a monoforum virtual channel to the parent channel; enabling again reuses the existing
		// monoforum and only syncs the price. Disabling (broadcastAllowed=false) keeps the monoforum so it can be reused when re-enabled.
		var mono domain.Channel
		if broadcastAllowed {
			if channel.LinkedMonoforumID == 0 {
				monoID, err := s.allocateFreshChannelID(ctx)
				if err != nil {
					return err
				}
				accessHash, err := randomChannelAccessHash()
				if err != nil {
					return err
				}
				mono = domain.Channel{
					ID:            monoID,
					AccessHash:    accessHash,
					CreatorUserID: channel.CreatorUserID,
					Title:         channel.Title,
					// Keep monoforum as a megagroup-like saved sublist container. TDesktop hangs when
					// a monoforum peer is projected as both broadcast and megagroup.
					Megagroup:             true,
					Monoforum:             true,
					LinkedMonoforumID:     channelID,
					SendPaidMessagesStars: stars,
					Date:                  nowUnix(),
				}
				if err := insertChannelTx(ctx, tx, mono); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE channels SET linked_monoforum_id = $2, updated_at = now() WHERE id = $1`, channelID, monoID); err != nil {
					return fmt.Errorf("link channel monoforum: %w", err)
				}
				channel.LinkedMonoforumID = monoID
			} else {
				// Reusing an existing monoforum only syncs the price. The creation path always builds a mono as a megagroup, and no path flips it
				// into broadcast/forum, so there is no need (and it would be wrong) to re-normalize the kind here—that would be a historical-data
				// repair for a never-released broadcast+monoforum experimental shape, dead code in a project with no legacy data.
				if _, err := tx.Exec(ctx, `UPDATE channels SET send_paid_messages_stars = $2, updated_at = now() WHERE id = $1`, channel.LinkedMonoforumID, stars); err != nil {
					return fmt.Errorf("sync monoforum paid messages price: %w", err)
				}
				mono, err = getChannelByID(ctx, tx, channel.LinkedMonoforumID)
				if err != nil {
					return fmt.Errorf("load monoforum for paid messages service: %w", err)
				}
			}
			// A monoforum's first (and only) service message is its "creation" message (messageActionChannelCreate):
			// for a monoforum, clients specifically render the creation message as "Direct messages were enabled in this channel."
			// (TDesktop lng_action_created_monoforum). The paid_messages_price service message for toggling/price changes
			// goes only into the parent broadcast channel (rendered in broadcast history as "Channel enabled/disabled Direct Messages"),
			// **never into the mono**—the mono is a megagroup (not a broadcast), so the same action would render the wrong text
			// "messages are free / set to N stars". Disabling only shows the disabled footer via the monoforumDisabled state
			// (hiding the parent channel's linked_monoforum_id) and no longer appends a bubble to the mono.
			if mono.ID != 0 && mono.TopMessageID == 0 {
				createMsg, createEvent, err := s.insertServiceMessage(ctx, tx, mono, userID, nowUnix(), domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: mono.Title})
				if err != nil {
					return fmt.Errorf("append monoforum creation service: %w", err)
				}
				mono.TopMessageID = createMsg.ID
				mono.Pts = createEvent.Pts
				services = append(services, domain.SendChannelMessageResult{Channel: mono, Message: createMsg, Event: createEvent})
			}
		}
		// The monoforum mirrors the parent channel's DM enabled state into its own broadcast_messages_allowed column. At projection time this decides
		// whether to send down the mono's own linked_monoforum_id: enabled=send, disabled=hide. When disabled, both the parent channel and the mono
		// hide the link, so when the client opens the monoforum and re-fetches it will not re-call setMonoforumLink(parent) and clear MonoforumDisabled,
		// keeping the disabled footer. The internal association row (linked_monoforum_id) is still kept for reuse when re-enabled.
		// Write the mirror column only when the mono's DM enabled state actually changes: this avoids a pointless rewrite of the mono row on every
		// SetPaidMessagesPrice (including an unchanged re-enable) and the resulting redundant cross-instance LISTEN/NOTIFY invalidation of the channels table. The enable branch already
		// loads the mono via getChannelByID (mono.ID!=0, fields reflect the current DB values); the disable branch does not load the mono (mono.ID==0),
		// so it cannot cheaply compare and keeps writing unconditionally to ensure the disabled footer state is correct.
		if channel.LinkedMonoforumID != 0 && (mono.ID == 0 || mono.BroadcastMessagesAllowed != broadcastAllowed) {
			if _, err := tx.Exec(ctx, `UPDATE channels SET broadcast_messages_allowed = $2, updated_at = now() WHERE id = $1`, channel.LinkedMonoforumID, broadcastAllowed); err != nil {
				return fmt.Errorf("sync monoforum dm-enabled state: %w", err)
			}
			if mono.ID != 0 {
				mono.BroadcastMessagesAllowed = broadcastAllowed
			}
		}
		if changed && channel.LinkedMonoforumID != 0 {
			action := domain.ChannelMessageAction{
				Type:                     domain.ChannelActionPaidMessagesPrice,
				BroadcastMessagesAllowed: broadcastAllowed,
				Stars:                    stars,
			}
			parentMsg, parentEvent, err := s.insertServiceMessage(ctx, tx, channel, userID, nowUnix(), action)
			if err != nil {
				return fmt.Errorf("append parent paid messages price service: %w", err)
			}
			channel.TopMessageID = parentMsg.ID
			channel.Pts = parentEvent.Pts
			services = append(services, domain.SendChannelMessageResult{Channel: channel, Message: parentMsg, Event: parentEvent})
		}
		return nil
	}); err != nil {
		return domain.ChannelPaidMessagesPriceResult{}, err
	}
	var first *domain.SendChannelMessageResult
	if len(services) > 0 {
		first = &services[0]
	}
	return domain.ChannelPaidMessagesPriceResult{Channel: channel, ServiceMessage: first, ServiceMessages: services}, nil
}
