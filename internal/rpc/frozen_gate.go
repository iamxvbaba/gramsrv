package rpc

import (
	"context"
	"strings"

	"telesrv/internal/domain"
)

// Frozen accounts are read-only. Classifying the finite read vocabulary and
// failing closed for every other semantic method also covers future handlers:
// an unfamiliar mutation cannot silently bypass the account-level gate.
var frozenReadOnlyOperationPrefixes = []string{
	"can", "check", "find", "get", "load", "lookup", "query", "read", "resolve", "search", "translate",
}

var frozenAlwaysBlockedMethods = map[string]struct{}{
	"channels.deleteMessages": {},
	"channels.joinChannel":    {},
	"channels.searchPosts":    {},
}

// These methods are security/session housekeeping, read acknowledgements, or
// lifecycle operations that must remain available in read-only mode. A frozen
// user must be able to log out/delete the account, and clients must be able to
// settle an incoming or already-active private call without entering a broken
// half-state. Starting a new call remains gated through phone.requestCall.
var frozenAllowedMutationNamedMethods = map[string]struct{}{
	"account.changeAuthorizationSettings": {},
	"account.deleteAccount":               {},
	"account.registerDevice":              {},
	"account.resetAuthorization":          {},
	"account.resetAuthorizations":         {},
	"account.unregisterDevice":            {},
	"account.updateDeviceLocked":          {},
	"account.updateStatus":                {},
	"messages.readHistory":                {},
	"messages.readMentions":               {},
	"messages.readMessageContents":        {},
	"messages.readReactions":              {},
	"messages.receivedMessages":           {},
	"messages.receivedQueue":              {},
	"messages.reportMessagesDelivery":     {},
	"messages.viewSponsoredMessage":       {},
	"channels.readHistory":                {},
	"channels.readMessageContents":        {},
	"phone.acceptCall":                    {},
	"phone.confirmCall":                   {},
	"phone.discardCall":                   {},
	"phone.receivedCall":                  {},
	"phone.saveCallDebug":                 {},
	"phone.sendSignalingData":             {},
	"phone.setCallRating":                 {},
	"stories.incrementStoryViews":         {},
}

func frozenMethodRequiresWriteGate(method string) bool {
	if _, blocked := frozenAlwaysBlockedMethods[method]; blocked {
		return true
	}
	if _, allowed := frozenAllowedMutationNamedMethods[method]; allowed {
		return false
	}
	if strings.HasPrefix(method, "auth.") {
		return false
	}
	dot := strings.IndexByte(method, '.')
	if dot < 0 || dot == len(method)-1 {
		return false
	}
	operation := method[dot+1:]
	for _, prefix := range frozenReadOnlyOperationPrefixes {
		if strings.HasPrefix(operation, prefix) {
			return false
		}
	}
	return true
}

func (r *Router) checkFrozenRPC(ctx context.Context, method string) error {
	if r == nil || r.deps.AccountFreeze == nil || !frozenMethodRequiresWriteGate(method) {
		return nil
	}
	userID, authorized := UserIDFrom(ctx)
	if !authorized || userID == 0 {
		return nil
	}
	freeze, found, err := r.deps.AccountFreeze.AccountFreeze(ctx, userID)
	if err != nil {
		return internalErr()
	}
	if found && freeze.Frozen {
		return frozenMethodInvalidErr()
	}
	return nil
}

// checkFrozenChannelParticipants implements Telegram's narrower read rule for
// the methods documented with FROZEN_PARTICIPANT_MISSING: an account freeze
// does not hide joined channels, but it removes public/linked guest preview.
func (r *Router) checkFrozenChannelParticipants(ctx context.Context, userID int64, channelIDs ...int64) error {
	if r == nil || r.deps.AccountFreeze == nil || r.deps.Channels == nil || userID == 0 || len(channelIDs) == 0 {
		return nil
	}
	freeze, found, err := r.deps.AccountFreeze.AccountFreeze(ctx, userID)
	if err != nil {
		return internalErr()
	}
	if !found || !freeze.Frozen {
		return nil
	}
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			continue
		}
		if _, duplicate := seen[channelID]; duplicate {
			continue
		}
		seen[channelID] = struct{}{}
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
		if err != nil {
			return channelInvalidErr(err)
		}
		if view.Self.UserID != userID || view.Self.Status != domain.ChannelMemberActive || view.Self.Guest {
			return frozenParticipantMissingErr()
		}
	}
	return nil
}
