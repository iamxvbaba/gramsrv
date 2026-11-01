package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
)

// pushUserMessageTransient pushes a transient (typing/presence) update: sessions that are not
// ready are skipped and never enter pending. Production Edge control must implement this
// explicitly; when it is missing this fails closed rather than falling back to ordinary
// durable/pending push semantics.
func (r *Router) pushUserMessageTransient(ctx context.Context, userID int64, logMessage string, msg tg.UpdatesClass) int {
	if r.deps.Sessions == nil || userID == 0 || msg == nil {
		return 0
	}
	if transient, ok := r.deps.Sessions.(TransientSessionPusher); ok {
		sessionID, _ := SessionIDFrom(ctx)
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		sent, err := transient.PushToUserTransientExceptAuthKeySession(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg, r.cfg.OutboundPushTimeout)
		if err != nil {
			r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Error(err))
		}
		return sent
	}
	r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.String("outcome", "transient_pusher_unavailable"))
	return 0
}

// pushCurrentSessionTransient sends an exact-session snapshot outside every
// durable PTS/recovery domain. Callers are restricted to short-lived presence
// or handshake state; reliable business updates must use a transactional
// outbox and must never call this helper.
func (r *Router) pushCurrentSessionTransient(ctx context.Context, logMessage string, msg tg.UpdatesClass) {
	if r.deps.Sessions == nil || msg == nil {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, rawAuthKeyIDForOrigin(ctx), sessionID, proto.MessageFromServer, msg); err != nil {
		r.log.Debug(logMessage, zap.Int64("session_id", sessionID), zap.Error(err))
	}
}
