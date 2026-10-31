package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
)

func (r *Router) pushUserMessage(ctx context.Context, userID int64, logMessage string, msg tg.UpdatesClass) int {
	if r.deps.Sessions == nil || userID == 0 || msg == nil {
		return 0
	}
	sessionID, _ := SessionIDFrom(ctx)
	if timeout := r.cfg.OutboundPushTimeout; timeout > 0 {
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		if bestEffort, ok := r.deps.Sessions.(BestEffortSessionBinder); ok {
			if sent, err := bestEffort.PushToUserExceptAuthKeySessionBestEffort(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg, timeout); err != nil {
				r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Duration("timeout", timeout), zap.Error(err))
				return sent
			} else {
				return sent
			}
		}
	}
	authKeyID := rawAuthKeyIDForOrigin(ctx)
	if sent, err := r.deps.Sessions.PushToUserExceptAuthKeySession(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg); err != nil {
		r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Error(err))
		return sent
	} else {
		return sent
	}
}

// pushUserMessageTransient pushes a transient (typing/presence) update: a session
// that is not ready is skipped outright and does not enter pending. When the
// implementation does not provide the TransientSessionBinder capability it falls
// back to the normal pushUserMessage (degrading to the old behavior: it enters
// pending, but durable correctness is still unaffected).
func (r *Router) pushUserMessageTransient(ctx context.Context, userID int64, logMessage string, msg tg.UpdatesClass) int {
	if r.deps.Sessions == nil || userID == 0 || msg == nil {
		return 0
	}
	if transient, ok := r.deps.Sessions.(TransientSessionBinder); ok {
		sessionID, _ := SessionIDFrom(ctx)
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		sent, err := transient.PushToUserTransientExceptAuthKeySession(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg, r.cfg.OutboundPushTimeout)
		if err != nil {
			r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Error(err))
		}
		return sent
	}
	return r.pushUserMessage(ctx, userID, logMessage, msg)
}

func (r *Router) pushCurrentSessionMessage(ctx context.Context, logMessage string, msg tg.UpdatesClass) {
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
