package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// registerUpdates registers the updates.* RPC handlers.
func (r *Router) registerUpdates(d *tlprofile.Dispatcher) {
	registerRPC[*tg.UpdatesGetStateRequest](d, tlprofile.SemanticMethodUpdatesGetState, func(ctx context.Context, layerRequest *tg.UpdatesGetStateRequest) (

		// onUpdatesGetState handles updates.getState. The startup paths of TDesktop and
		// DrKLO treat it as an explicit "start syncing from the current snapshot"
		// baseline: it returns the account's current contiguous watermark, and only after
		// the rpc_result is physically delivered does it atomically advance that
		// device's confirmed + observed. For clients whose baseline semantics have not
		// been audited it still returns the same current state, but after delivery only
		// advances confirmed; this preserves the observed/durable difference tail and
		// keeps the TDesktop/DrKLO compatibility exception from spreading into a
		// retention back door through which every client could skip unacknowledged
		// events.
		any, error) {
		return r.onUpdatesGetState(ctx)
	})
	registerRPC[*tg.UpdatesGetDifferenceRequest](d, tlprofile.SemanticMethodUpdatesGetDifference, func(ctx context.Context, layerRequest *tg.UpdatesGetDifferenceRequest) (any, error) {
		return r.onUpdatesGetDifference(ctx, layerRequest)
	})
}

func (r *Router) onUpdatesGetState(ctx context.Context) (*tg.UpdatesState, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, nil, 0, nil, false)
		return &tg.UpdatesState{Date: int(r.clock.Now().Unix()), Qts: r.deviceEncryptedQts(ctx)}, nil
	}
	st, err := r.deps.Updates.CurrentState(ctx, userID)
	mode := domain.UpdateStateCommitDeliveredOnly
	if getStateEstablishesObservedBaseline(ctx) {
		mode = domain.UpdateStateCommitDeliveredAndObservedBaseline
	} else if err == nil {
		// iOS-and-other clients intentionally opt out of the audited baseline
		// policy (see getStateEstablishesObservedBaseline) and are served the
		// plain snapshot. That is the designed behavior, not an anomaly, so it
		// is debug noise rather than a warn.
		r.log.Debug("updates.getState returned current snapshot without advancing observed baseline for client without audited baseline policy",
			r.contextLogFields(ctx)...)
	}
	if err != nil {
		return nil, internalErr()
	}
	// Secret-chat qts is device-level and independent of the account-level pts engine:
	// inject the qts already assigned to the current device (0 for a device with no
	// secret chats).
	st.Qts = r.deviceEncryptedQts(ctx)
	r.stageUpdatesBaselineAfterDelivery(ctx, userID, &st, mode, nil, true)
	out := tgUpdateState(st)
	return ptr(out), nil
}

func getStateEstablishesObservedBaseline(ctx context.Context) bool {
	switch ClientTypeFrom(ctx) {
	case ClientTypeTDesktop, ClientTypeAndroid:
		return true
	default:
		return false
	}
}

func (r *Router) onUpdatesGetDifference(ctx context.Context, req *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Updates == nil {
		now := int(r.clock.Now().Unix())
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, nil, 0, nil, false)
		return &tg.UpdatesDifferenceEmpty{Date: now}, nil
	}
	from, err := r.deps.Updates.ObserveDifferenceRequest(ctx, id, userID, domain.UpdateState{
		Pts:  req.Pts,
		Qts:  req.Qts,
		Date: req.Date,
	})
	if err != nil {
		return nil, internalErr()
	}
	// pts_total_limit is the client's explicit fast-skip request: when the gap exceeds
	// the limit it returns differenceTooLong{pts}, and the client resets the whole dialog
	// list instead of serially paging through thousands of slices. A client that does
	// not send it (TDesktop) never receives tooLong.
	if limit, ok := req.GetPtsTotalLimit(); ok && limit > 0 && req.Pts > 0 {
		current, err := r.deps.Updates.CurrentState(ctx, userID)
		if err == nil && current.Pts-from.Pts > limit {
			// differenceTooLong carries only the replacement pts. Preserve the
			// request-proven qts/date instead of over-confirming fields that were
			// not present on wire.
			returnedCursor := from
			returnedCursor.Pts = current.Pts
			r.stageUpdatesBaselineAfterDelivery(ctx, userID, &returnedCursor, domain.UpdateStateCommitDeliveredOnly, nil, false)
			return &tg.UpdatesDifferenceTooLong{Pts: current.Pts}, nil
		}
	}
	st, err := r.deps.Updates.GetDifference(ctx, id, userID, from)
	if err != nil {
		return nil, internalErr()
	}
	st.ChannelNudges = r.accountChannelDifferenceNudges(ctx, userID, req.Date)
	if !st.Partial {
		for _, nudge := range st.ChannelNudges {
			if nudge.AvailableMinID <= 0 {
				continue
			}
			// No-PTS owner-local clear recovery is date-indexed. Advance only
			// on the final account page and never beyond the server clock; the
			// indexed query deliberately overlaps equality, so a same-second
			// reconnect can receive an idempotent duplicate rather than miss.
			if now := int(r.clock.Now().Unix()); now > st.State.Date {
				st.State.Date = now
			}
			break
		}
	}
	// Secret-chat device-level qts messages (independent of account-level pts events):
	// backfilled per the current device's req.Qts.
	encMsgs, newQts, encryptedPartial, err := r.encryptedDifference(ctx, req.Qts)
	if err != nil {
		r.log.Error("load secret chat qts difference", zap.Error(err))
		return nil, internalErr()
	}
	// Secret-chat handshake/read-state events (no qts): backfilled into OtherUpdates
	// from the undelivered markers.
	stateUpdates, statePeerUserIDs, stateEventIDs, encryptedStatePartial, err := r.encryptedStateUpdates(ctx, userID)
	if err != nil {
		r.log.Error("load secret chat state difference", zap.Error(err))
		return nil, internalErr()
	}
	// If any of the account pts, device qts or sequence-less state events is truncated,
	// a differenceSlice must be returned.
	st.Partial = st.Partial || encryptedPartial || encryptedStatePartial
	if !st.Partial && len(st.Events) == 0 && len(st.ChannelNudges) == 0 && len(encMsgs) == 0 && len(stateUpdates) == 0 {
		// differenceEmpty carries no pts/qts. Both audited clients retain their
		// request cursor, so only that normalized cursor is proven delivered.
		emptyCursor := domain.UpdateState{Pts: from.Pts, Qts: from.Qts, Date: st.State.Date, Seq: st.State.Seq}
		// An account-level secret-chat invite, after accept, is a convergence event for
		// the winning auth key that is acknowledgeable but has no visible update; even
		// when differenceEmpty is returned, these event ids must still be registered
		// after the rpc_result is successfully delivered.
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, &emptyCursor, domain.UpdateStateCommitDeliveredOnly, stateEventIDs, true)
		return &tg.UpdatesDifferenceEmpty{Date: st.State.Date, Seq: st.State.Seq}, nil
	}
	peerCache := newViewerPeerCache(r)
	st.Events, err = r.enrichUpdateEventsWithPeerCacheStrict(ctx, userID, st.Events, peerCache)
	if err != nil {
		r.log.Error("project durable account difference users",
			zap.Int64("viewer_user_id", userID),
			zap.Error(err))
		return nil, internalErr()
	}
	diff := r.tgUpdatesDifference(ctx, userID, st)
	diff = injectEncryptedMessages(diff, encMsgs, newQts)
	diff, err = r.injectEncryptedOtherUpdatesStrict(ctx, userID, diff, stateUpdates, statePeerUserIDs, peerCache)
	if err != nil {
		r.log.Error("project durable encrypted difference users",
			zap.Int64("viewer_user_id", userID),
			zap.Error(err))
		return nil, internalErr()
	}
	returnedCursor := st.State
	returnedCursor.Qts = newQts
	r.stageUpdatesBaselineAfterDelivery(ctx, userID, &returnedCursor, domain.UpdateStateCommitDeliveredOnly, stateEventIDs, true)
	return diff, nil
}

func (r *Router) accountChannelDifferenceNudges(ctx context.Context, userID int64, sinceDate int) []domain.ChannelDifferenceNudge {
	if r.deps.Channels == nil || userID == 0 || sinceDate <= 0 {
		return nil
	}
	// Inject nudges for every dirty channel, paged by channel_id (still with a hard
	// total cap), so an account in more than one page of active channels does not miss
	// channels with larger channel_ids when recovering from a long offline period.
	const maxNudges = 500
	out := make([]domain.ChannelDifferenceNudge, 0, domain.MaxChannelDifferenceLimit)
	afterChannelID := int64(0)
	for len(out) < maxNudges {
		dirty, err := r.deps.Channels.DirtyActiveChannelsForUser(ctx, userID, sinceDate, afterChannelID, domain.MaxChannelDifferenceLimit)
		if err != nil || len(dirty) == 0 {
			break
		}
		channelIDs := make([]int64, 0, len(dirty))
		for _, item := range dirty {
			if item.ChannelID != 0 {
				channelIDs = append(channelIDs, item.ChannelID)
			}
		}
		viewsByID := make(map[int64]domain.ChannelView, len(channelIDs))
		if len(channelIDs) != 0 {
			if views, err := r.deps.Channels.GetChannels(ctx, userID, channelIDs); err == nil {
				for _, view := range views {
					if view.Channel.ID != 0 {
						viewsByID[view.Channel.ID] = view
					}
				}
			}
		}
		for _, item := range dirty {
			if len(out) >= maxNudges {
				break
			}
			if item.ChannelID == 0 {
				continue
			}
			nudge := domain.ChannelDifferenceNudge{
				ChannelID:           item.ChannelID,
				Pts:                 item.Pts,
				ChannelUpdatesDirty: item.ChannelUpdatesDirty,
				AvailableMinID:      item.AvailableMinID,
				HistoryClearDate:    item.HistoryClearDate,
			}
			if view, ok := viewsByID[item.ChannelID]; ok {
				nudge.Channel = &view
			}
			out = append(out, nudge)
			if item.ChannelID > afterChannelID {
				afterChannelID = item.ChannelID
			}
		}
		if len(dirty) < domain.MaxChannelDifferenceLimit {
			break
		}
	}
	return out
}

// maybeMarkSessionReceivesUpdates treats a plain RPC sent by a logged-in connection
// (not wrapped in invokeWithoutUpdates) as that session's declaration of receiving
// updates, matching official semantics: the client sends plain requests only on the
// main connection, while media/temp connections always carry the invokeWithoutUpdates
// wrapper. Only the delivery plan is registered here; membership sync,
// SetReceivesUpdates and the pending flush must wait until the successful rpc_result is
// physically delivered. A session already receiving is short-circuited, avoiding a
// channel-membership sync on every RPC.
func (r *Router) maybeMarkSessionReceivesUpdates(ctx context.Context) {
	if invokeWithoutUpdatesFrom(ctx) {
		return
	}
	userID, ok := UserIDFrom(ctx)
	if !ok {
		return
	}
	if provider, ok := r.deps.Sessions.(SessionUpdatesStateProvider); ok {
		rawAuthKeyID, okRaw := RawAuthKeyIDFrom(ctx)
		sessionID, okSess := SessionIDFrom(ctx)
		if okRaw && okSess && provider.ReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID) {
			return
		}
	}
	r.stageSessionUpdatesReadyAfterDelivery(ctx, userID)
}

func (r *Router) markSessionReceivesUpdatesNow(ctx context.Context, userID int64) {
	if r.deps.Sessions == nil {
		return
	}
	r.syncSessionChannelMemberships(ctx, userID)
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	r.deps.Sessions.SetReceivesUpdatesForAuthKey(rawAuthKeyIDForOrigin(ctx), sessionID, true)
}

func ptr[T any](v T) *T { return &v }
