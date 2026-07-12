package rpc

import (
	"context"
	"telesrv/internal/domain"
)

// authzFromCtx 从连接上下文组装一条待绑定的设备授权（UserID 由业务层填充）。
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
	return a
}

// currentUserID 返回当前连接已登录的 user_id。
//
// 优先使用 active session 缓存；若新连接尚未绑定但 auth_key 已授权，则只在这里
// 查询一次授权表并回填 session，避免各业务 service 每个 RPC 重复 authKey→userID。
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
