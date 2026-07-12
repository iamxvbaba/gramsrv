package rpc

import (
	"context"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"go.uber.org/zap"
)

func (r *Router) pushUserMessage(ctx context.Context, userID int64, logMessage string, msg bin.Encoder) int {
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

// pushUserMessageTransient 推送 transient（typing/presence）update：未就绪的 session 直接
// 跳过、不进 pending。实现未提供 TransientSessionBinder 能力时回退到普通 pushUserMessage
// （退化为旧行为：会进 pending，但仍不影响 durable 正确性）。
func (r *Router) pushUserMessageTransient(ctx context.Context, userID int64, logMessage string, msg bin.Encoder) int {
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

func (r *Router) pushCurrentSessionMessage(ctx context.Context, logMessage string, msg bin.Encoder) {
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
