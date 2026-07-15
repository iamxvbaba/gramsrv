package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// prepareRPCDispatchContext resolves the physical/business auth-key boundary,
// current authorization and persisted client metadata once for both the
// historical canonical dispatcher and the generated exact-layer dispatcher.
// Wire admission stays outside this function so callers can reject malformed
// requests before any store lookup.
func (r *Router) prepareRPCDispatchContext(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	wireBytes int,
	method string,
) (context.Context, *updatesDeliveryPlan, error) {
	preStart := r.clock.Now()
	ctx = withInboundRPCBytes(ctx, wireBytes)
	ctx = WithRawAuthKeyID(ctx, rawAuthKeyID)
	effectiveAuthKeyID, err := r.effectiveAuthKeyID(ctx, rawAuthKeyID, sessionID)
	if err != nil {
		return nil, nil, internalErr()
	}
	tAuth := r.clock.Now()
	ctx = WithAuthKeyID(ctx, effectiveAuthKeyID)
	ctx = WithSessionID(ctx, sessionID)
	// Ignore caller-injected values. Exact same-session evidence wins; otherwise
	// the durable auth-key default restored below initializes this new session.
	// A generated admitted request still owns its immutable request/result
	// profile independently of this mutable session default.
	ctx = WithLayer(ctx, 0)
	if layer, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); ok {
		ctx = WithLayer(ctx, layer)
	}
	userID, hasUserID, err := r.effectiveUserID(ctx, rawAuthKeyID, effectiveAuthKeyID, sessionID)
	if err != nil {
		return nil, nil, internalErr()
	}
	if hasUserID {
		ctx = WithUserID(ctx, userID)
	}
	tUser := r.clock.Now()
	info, hasClientMetadata, clientMetadataStored := r.clientSessionInfo(ctx)
	lookupInfo := info
	if effectiveAuthKeyID != rawAuthKeyID {
		// Check markers and raw shadows belong to the physical temp key. They
		// cannot suppress or outrank lookup of the canonical permanent default.
		lookupInfo.authKeyInfoChecked = false
		lookupInfo.authorizationChecked = false
		if LayerFrom(ctx) == 0 {
			lookupInfo.layer = 0
			info.layer = 0
		}
	}
	if authInfo, ok := r.clientSessionInfoFromAuthKey(ctx, effectiveAuthKeyID, lookupInfo); ok {
		info = mergeClientSessionInfo(info, authInfo)
		hasClientMetadata = true
		r.rememberClientSessionInfo(ctx, info)
		clientMetadataStored = true
	}
	if hasUserID {
		lookupInfo = info
		if effectiveAuthKeyID != rawAuthKeyID {
			lookupInfo.authorizationChecked = false
		}
		if authInfo, ok := r.clientSessionInfoFromAuthorization(ctx, userID, effectiveAuthKeyID, lookupInfo); ok {
			info = mergeClientSessionInfo(info, authInfo)
			hasClientMetadata = true
			r.rememberClientSessionInfo(ctx, info)
			clientMetadataStored = true
		}
	}
	if r.log != nil {
		if tInfo := r.clock.Now(); tInfo.Sub(preStart) > 50*time.Millisecond {
			r.log.Info("slow pre-handler",
				zap.String("method", method),
				zap.Duration("pre_total", tInfo.Sub(preStart)),
				zap.Duration("auth_resolve", tAuth.Sub(preStart)),
				zap.Duration("user_resolve", tUser.Sub(tAuth)),
				zap.Duration("client_info", tInfo.Sub(tUser)),
				zap.Int64("session_id", sessionID),
			)
		}
	}
	if hasClientMetadata {
		if !clientMetadataStored {
			r.rememberClientSessionInfoIfMissing(ctx, info)
		}
		if info.hasClientInfo {
			ctx = WithClientInfo(ctx, info.clientInfo)
		}
		if LayerFrom(ctx) == 0 && isSupportedLayer(info.layer) {
			ctx = WithLayer(ctx, info.layer)
		}
	}
	ctx, updatesDelivery := withUpdatesDeliveryPlan(ctx)
	return ctx, updatesDelivery, nil
}
