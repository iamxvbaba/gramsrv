package rpc

import (
	"context"
	"telesrv/internal/domain"
)

// authzFromCtx assembles a device authorization to be bound from the connection
// context (UserID is filled in by the business layer).
func (r *Router) authzFromCtx(ctx context.Context) domain.Authorization {
	id, _ := AuthKeyIDFrom(ctx)
	a := domain.Authorization{AuthKeyID: id, Layer: LayerFrom(ctx)}
	if ci, ok := ClientInfoFrom(ctx); ok {
		a.DeviceModel = ci.DeviceModel
		a.Platform = string(ci.ClientType())
		a.SystemVersion = ci.SystemVersion
		a.AppVersion = ci.AppVersion
		a.APIID = ci.APIID
	}
	if ip, ok := ClientIPFrom(ctx); ok {
		a.IP = ip
	}
	return a
}

// currentUserID returns the user_id of the currently logged-in connection.
//
// It prefers the active session cache; if a new connection is not yet bound but
// the auth_key is already authorized, it queries the authorization table only
// here once and backfills the session, avoiding a repeated authKey->userID
// lookup per RPC in every business service.
func (r *Router) currentUserID(ctx context.Context) (int64, bool, error) {
	if userID, ok := UserIDFrom(ctx); ok {
		return userID, true, nil
	}
	if r.deps.Sessions != nil {
		if sessionID, ok := SessionIDFrom(ctx); ok {
			rawAuthKeyID := rawAuthKeyIDForOrigin(ctx)
			if userID, resolved := r.deps.Sessions.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
				if userID == 0 {
					if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
						if cachedUserID, ok := r.positiveCachedAuthUser(authKeyID); ok {
							r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, cachedUserID)
							r.announceSessionOnline(ctx, cachedUserID)
							return cachedUserID, true, nil
						}
					}
				}
				return userID, userID != 0, nil
			}
		}
	}
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return 0, false, nil
	}
	userID, found, err := r.lookupAuthUser(ctx, authKeyID)
	if err != nil || !found {
		if err == nil && !found {
			r.bindSessionUser(ctx, 0)
		}
		return 0, found, err
	}
	r.bindSessionUser(ctx, userID)
	return userID, true, nil
}
