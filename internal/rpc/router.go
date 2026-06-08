package rpc

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
)

// maxWrapperDepth 限制 invokeWithLayer/initConnection 等 wrapper 的嵌套深度，防御恶意构造。
const maxWrapperDepth = 4

var (
	tlTypeNamesOnce sync.Once
	tlTypeNames     map[uint32]string
)

// Config 是 Router 所需的服务端信息。
type Config struct {
	DC                  int
	IP                  string // 对外公布的 DC IP（写入 DCOptions）
	Port                int    // 对外公布的 DC 端口
	OutboundPushTimeout time.Duration
}

// Router 把解密后的 RPC 请求按 TypeID 路由到 typed handler（tg.ServerDispatcher）。
//
// handler 输入输出均为 gotd/td/tg 类型，各业务域的 handler
// 与注册见 help.go / auth.go / users.go / updates.go。Router 本身只负责协议外壳：
// 剥离 invokeWithLayer / initConnection / invokeWithoutUpdates，并兜底未注册 RPC。
type Router struct {
	cfg          Config
	log          *zap.Logger
	clock        clock.Clock
	deps         Deps
	dispatcher   *tg.ServerDispatcher
	clientInfoMu sync.RWMutex
	clientInfo   map[clientInfoSessionKey]ClientInfo
	authUserMu   sync.RWMutex
	authUsers    map[[8]byte]authUserCacheEntry
	authUserSF   singleflight.Group
	presence     *presenceTracker
}

type clientInfoSessionKey struct {
	rawAuthKeyID [8]byte
	sessionID    int64
}

type authUserCacheEntry struct {
	userID int64
	found  bool
}

// New 创建 Router，由各业务域自行注册其 RPC handler（registerHelp/Auth/Users/Updates）。
func New(cfg Config, deps Deps, log *zap.Logger, clk clock.Clock) *Router {
	r := &Router{cfg: cfg, log: log, clock: clk, deps: deps, presence: newPresenceTracker()}
	d := tg.NewServerDispatcher(r.fallback)

	r.registerHelp(d)
	r.registerAuth(d)
	r.registerUsers(d)
	r.registerUpdates(d)
	r.registerAccount(d)
	r.registerMessages(d)
	r.registerChannels(d)
	r.registerUpload(d)
	r.registerPhotos(d)
	r.registerFolders(d)
	r.registerContacts(d)
	r.registerLangpack(d)
	r.registerStories(d)
	r.registerPayments(d)
	r.registerStats(d)
	r.registerPremium(d)
	r.registerAiCompose(d)

	r.dispatcher = d
	return r
}

// Dispatch 路由一条 RPC 请求：先剥离 invokeWithLayer / initConnection /
// invokeWithoutUpdates 等 wrapper（注入 layer / 客户端信息到 ctx），
// 再按 TypeID 路由到 typed handler。满足 mtprotoedge.RPCHandler。
func (r *Router) Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error) {
	ctx = WithRawAuthKeyID(ctx, authKeyID)
	effectiveAuthKeyID, err := r.effectiveAuthKeyID(ctx, authKeyID, sessionID)
	if err != nil {
		return nil, internalErr()
	}
	ctx = WithAuthKeyID(ctx, effectiveAuthKeyID)
	ctx = WithSessionID(ctx, sessionID)
	userID, hasUserID, err := r.effectiveUserID(ctx, authKeyID, effectiveAuthKeyID, sessionID)
	if err != nil {
		return nil, internalErr()
	}
	if hasUserID {
		ctx = WithUserID(ctx, userID)
	}
	if info, ok := r.clientInfoForSession(ctx); ok {
		ctx = WithClientInfo(ctx, info)
	}
	return r.dispatch(ctx, b, 0)
}

func (r *Router) effectiveAuthKeyID(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) ([8]byte, error) {
	if r.deps.Sessions != nil {
		if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
			if id, ok := scoped.AuthKeyIDForSession(rawAuthKeyID, sessionID); ok {
				return id, nil
			}
		} else if id, ok := r.deps.Sessions.AuthKeyID(sessionID); ok {
			return id, nil
		}
	}
	effective := rawAuthKeyID
	if r.deps.Auth != nil {
		resolved, ok, err := r.deps.Auth.ResolveAuthKey(ctx, rawAuthKeyID)
		if err != nil {
			return [8]byte{}, err
		}
		if ok {
			effective = resolved
		}
	}
	if r.deps.Sessions != nil {
		if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
			scoped.BindAuthKeyForSession(rawAuthKeyID, sessionID, effective)
		} else {
			r.deps.Sessions.BindAuthKey(sessionID, effective)
		}
	}
	return effective, nil
}

func (r *Router) effectiveUserID(ctx context.Context, rawAuthKeyID, authKeyID [8]byte, sessionID int64) (int64, bool, error) {
	if userID, ok := UserIDFrom(ctx); ok {
		if scoped, ok := r.scopedSessions(); ok {
			scoped.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
		} else if r.deps.Sessions != nil {
			r.deps.Sessions.BindUser(sessionID, userID)
		}
		return userID, true, nil
	}
	if r.deps.Sessions != nil {
		if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
			if userID, resolved := scoped.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
				return userID, userID != 0, nil
			}
		} else if userID, resolved := r.deps.Sessions.UserIDResolved(sessionID); resolved {
			return userID, userID != 0, nil
		}
	}
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	userID, found, err := r.lookupAuthUser(ctx, authKeyID)
	if err != nil {
		return 0, false, err
	}
	if r.deps.Sessions != nil {
		if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
			if cachedUserID, resolved := scoped.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
				return cachedUserID, cachedUserID != 0, nil
			}
		} else if cachedUserID, resolved := r.deps.Sessions.UserIDResolved(sessionID); resolved {
			return cachedUserID, cachedUserID != 0, nil
		}
		if found {
			if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
				scoped.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
			} else {
				r.deps.Sessions.BindUser(sessionID, userID)
			}
			r.announceSessionOnline(ctx, userID)
		} else {
			if scoped, ok := r.deps.Sessions.(ScopedSessionBinder); ok {
				scoped.BindUserForAuthKey(rawAuthKeyID, sessionID, 0)
			} else {
				r.deps.Sessions.BindUser(sessionID, 0)
			}
		}
	}
	return userID, found, nil
}

func (r *Router) lookupAuthUser(ctx context.Context, authKeyID [8]byte) (int64, bool, error) {
	if userID, found, ok := r.cachedAuthUser(authKeyID); ok {
		return userID, found, nil
	}
	key := string(authKeyID[:])
	v, err, _ := r.authUserSF.Do(key, func() (any, error) {
		if userID, found, ok := r.cachedAuthUser(authKeyID); ok {
			return authUserCacheEntry{userID: userID, found: found}, nil
		}
		userID, found, err := r.deps.Auth.UserID(ctx, authKeyID)
		if err != nil {
			return authUserCacheEntry{}, err
		}
		r.setAuthUserCache(authKeyID, userID, found)
		return authUserCacheEntry{userID: userID, found: found}, nil
	})
	if err != nil {
		return 0, false, err
	}
	entry := v.(authUserCacheEntry)
	return entry.userID, entry.found, nil
}

func (r *Router) cachedAuthUser(authKeyID [8]byte) (int64, bool, bool) {
	r.authUserMu.RLock()
	defer r.authUserMu.RUnlock()
	entry, ok := r.authUsers[authKeyID]
	if !ok {
		return 0, false, false
	}
	return entry.userID, entry.found, true
}

func (r *Router) setAuthUserCache(authKeyID [8]byte, userID int64, found bool) {
	r.authUserMu.Lock()
	defer r.authUserMu.Unlock()
	if r.authUsers == nil {
		r.authUsers = make(map[[8]byte]authUserCacheEntry)
	}
	r.authUsers[authKeyID] = authUserCacheEntry{userID: userID, found: found}
}

func (r *Router) invalidateAuthUserCache(authKeyID [8]byte) {
	r.authUserMu.Lock()
	delete(r.authUsers, authKeyID)
	r.authUserMu.Unlock()
	r.authUserSF.Forget(string(authKeyID[:]))
}

func (r *Router) scopedSessions() (ScopedSessionBinder, bool) {
	if r.deps.Sessions == nil {
		return nil, false
	}
	scoped, ok := r.deps.Sessions.(ScopedSessionBinder)
	return scoped, ok
}

func (r *Router) dispatch(ctx context.Context, b *bin.Buffer, depth int) (bin.Encoder, error) {
	if depth > maxWrapperDepth {
		return nil, wrapperTooDeepErr()
	}

	id, err := b.PeekID()
	if err != nil {
		return nil, err
	}

	switch id {
	case tg.InvokeWithLayerRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		layer, err := b.Int()
		if err != nil {
			return nil, fmt.Errorf("decode invokeWithLayer layer: %w", err)
		}
		// query 紧跟 layer，buffer 剩余即内层请求。
		return r.dispatch(WithLayer(ctx, layer), b, depth+1)

	case tg.InvokeWithoutUpdatesRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		return r.dispatch(ctx, b, depth+1)

	case tg.InitConnectionRequestTypeID:
		req := &tg.InitConnectionRequest{Query: &rawObject{}}
		if err := req.Decode(b); err != nil {
			return nil, fmt.Errorf("decode initConnection: %w", err)
		}
		info := ClientInfo{
			APIID:          req.APIID,
			DeviceModel:    req.DeviceModel,
			SystemVersion:  req.SystemVersion,
			AppVersion:     req.AppVersion,
			SystemLangCode: req.SystemLangCode,
			LangPack:       req.LangPack,
			LangCode:       req.LangCode,
		}
		ctx = WithClientInfo(ctx, info)
		r.rememberClientInfo(ctx, info)
		r.log.Debug("initConnection",
			zap.Int("api_id", req.APIID),
			zap.String("device", req.DeviceModel),
			zap.String("app", req.AppVersion),
			zap.Int("layer", LayerFrom(ctx)),
		)
		inner, ok := req.Query.(*rawObject)
		if !ok {
			return nil, fmt.Errorf("initConnection query: unexpected type %T", req.Query)
		}
		return r.dispatch(ctx, &bin.Buffer{Buf: inner.data}, depth+1)

	default:
		if enc, ok, err := r.dispatchCompat(ctx, b, id); ok {
			return enc, err
		}
		start := time.Now()
		enc, err := r.dispatcher.Handle(ctx, b)
		dur := time.Since(start)
		fields := append([]zap.Field{
			zap.String("method", tlTypeName(id)),
			zap.String("type_id", fmt.Sprintf("%#x", id)),
			zap.Duration("dur", dur),
		}, r.contextLogFields(ctx)...)
		if err != nil || dur > 100*time.Millisecond {
			if err != nil {
				fields = append(fields, zap.Error(err))
			}
			r.log.Info("RPC inner handled", fields...)
		} else {
			r.log.Debug("RPC inner handled", fields...)
		}
		return enc, err
	}
}

func tlTypeName(id uint32) string {
	tlTypeNamesOnce.Do(func() {
		names := tg.NamesMap()
		tlTypeNames = make(map[uint32]string, len(names))
		for name, typeID := range names {
			tlTypeNames[typeID] = name
		}
	})
	if name, ok := tlTypeNames[id]; ok {
		return name
	}
	return fmt.Sprintf("%#x", id)
}

func (r *Router) rememberClientInfo(ctx context.Context, info ClientInfo) {
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	r.clientInfoMu.Lock()
	defer r.clientInfoMu.Unlock()
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]ClientInfo)
	}
	r.clientInfo[clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}] = info
}

func (r *Router) clientInfoForSession(ctx context.Context) (ClientInfo, bool) {
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return ClientInfo{}, false
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return ClientInfo{}, false
	}
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	info, ok := r.clientInfo[clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}]
	return info, ok
}

// fallback 处理未注册的 RPC：记录到 compatibility trace（落兼容矩阵），
// 返回 NOT_IMPLEMENTED rpc_error 让客户端继续运行而非断连。
func (r *Router) fallback(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	id, _ := b.PeekID()
	fields := append([]zap.Field{zap.String("type_id", fmt.Sprintf("%#x", id))}, r.contextLogFields(ctx)...)
	r.log.Warn("Unhandled RPC (compatibility trace)", fields...)
	return nil, notImplementedErr()
}

func (r *Router) contextLogFields(ctx context.Context) []zap.Field {
	fields := []zap.Field{zap.Int("layer", LayerFrom(ctx))}
	if sessionID, ok := SessionIDFrom(ctx); ok {
		fields = append(fields, zap.Int64("session_id", sessionID))
	}
	if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok {
		fields = append(fields, zap.String("raw_auth_key_id", hex.EncodeToString(rawAuthKeyID[:])))
	}
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		fields = append(fields, zap.String("auth_key_id", hex.EncodeToString(authKeyID[:])))
	}
	if userID, ok := UserIDFrom(ctx); ok {
		fields = append(fields, zap.Int64("user_id", userID))
	}
	return fields
}

// rawObject 在解码 wrapper 时按原样捕获内层 query 的 TL 字节，供递归分发。
// 它实现 bin.Object（Encode/Decode），但只搬运字节、不解释内容。
type rawObject struct {
	data []byte
}

func (o *rawObject) Decode(b *bin.Buffer) error {
	o.data = append(o.data[:0], b.Buf...)
	b.Skip(len(b.Buf))
	return nil
}

func (o *rawObject) Encode(b *bin.Buffer) error {
	b.Put(o.data)
	return nil
}
