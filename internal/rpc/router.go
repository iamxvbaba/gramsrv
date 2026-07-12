package rpc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"

	"telesrv/internal/compat/layerwire"
	"telesrv/internal/domain"
	"telesrv/internal/observability/dbtrace"
)

// maxWrapperDepth 限制 invokeWithLayer/initConnection/invokeAfter 等 wrapper 的嵌套深度，防御恶意构造。
// 合法客户端的最深包装来自 gotd 数据连接模式的握手初始化：
// invokeWithLayer → invokeWithoutUpdates → initConnection → invokeWithoutUpdates → query（深度 5），
// 官方服务器同样接受。取 8 留余量，仍是小常量、不削弱对无界嵌套的防御。
const maxWrapperDepth = 8

const maxInvokeAfterMsgIDs = 128

// tempResolveResult 是 effectiveAuthKeyID 里 temp→perm 解析经 singleflight 共享的结果。
type tempResolveResult struct {
	perm [8]byte
	ok   bool
}

const (
	authKeyResolveSingleflightPrefix    = "resolve:"
	authClientInfoSingleflightPrefix    = "authinfo:"
	authKeyClientInfoSingleflightPrefix = "authkeyinfo:"
)

var (
	tlTypeNamesOnce sync.Once
	tlTypeNames     map[uint32]string
)

// Config 是 Router 所需的服务端信息。
type Config struct {
	DC                  int
	IP                  string // 对外公布的 DC IP（写入 DCOptions）
	Port                int    // 对外公布的 DC 端口
	InstanceID          string // 进程内唯一标识，用于跨实例 ephemeral push 去重。
	OutboundPushTimeout time.Duration
	SendRateLimit       int
	SendRateWindow      time.Duration
	// AuthCode*RateLimit protects the unauthenticated sendCode/resendCode write path.
	// The phone budget is keyed by SHA-256(normalized phone), never by the plaintext phone;
	// the second budget is keyed by the physical connection's raw auth_key_id.
	AuthCodePhoneRateLimit   int
	AuthCodeAuthKeyRateLimit int
	AuthCodeRateWindow       time.Duration
	// CatchupRateLimit/CatchupRateWindow 限制 difference 类 catch-up RPC（getChannelDifference /
	// getPeerDialogs）的每用户频率（设计 Phase 2 / §10.3）：nudge 被消费后客户端会触发这两类
	// catch-up，放开大群 nudge 全速前需 FLOOD_WAIT 兜底防风暴打爆 PG。两类各自独立计数、共用同一
	// 阈值。<=0 关闭（默认行为不变）。
	CatchupRateLimit  int
	CatchupRateWindow time.Duration
	// ChannelNudgeMaxTargets 是一次 fan-out 的 >cap nudge 目标上限（设计 Phase 0b 限速兜底）；
	// <=0 用内置默认 defaultChannelNudgeMaxTargets。
	ChannelNudgeMaxTargets int
	// CallSignalingMaxBytes 是 phone.sendSignalingData 单条载荷上限；<=0 不限制。
	CallSignalingMaxBytes int
	// CallForceRelay 强制私聊通话 p2p_allowed=false（调试 TURN 中继路径）。
	CallForceRelay bool
	// GroupCallMaxParticipants 是群通话单房间参与者上限；<=0 不限制。
	GroupCallMaxParticipants int
	// RtmpIngestURL 是 getGroupCallStreamRtmpUrl 返回给推流端（OBS）的服务器地址，
	// 形如 "rtmp://<host>:<port>/live"。为空时回落 "rtmp://<AdvertiseIP>:2400/live"。
	RtmpIngestURL string
	// PublicBaseURL 是所有客户端可见 telesrv 链接的公开根 URL。
	PublicBaseURL string
	// TempKeyResolveCacheTTL 是 PFS temp→perm auth key 解析的进程内缓存有效期。>0 时同一 temp key
	// 在 TTL 内复用上次解析、跳过每帧 ResolveAuthKey 的 PG 查询；0（默认/测试）关闭=每帧重校验。
	// 显式撤销会删除协议 auth key、清缓存并断开活跃连接；TTL 只影响自然过期或异常路径下的
	// 下一次重新解析。re-bind 由 onAuthBindTempAuthKey 显式失效，避免跨账号串号。
	TempKeyResolveCacheTTL time.Duration
	// TempKeyResolveCacheMaxEntries 是 temp→perm 解析缓存容量；<=0 用内置默认。
	TempKeyResolveCacheMaxEntries int
}

// Router 把解密后的 RPC 请求按 TypeID 路由到 typed handler（tg.ServerDispatcher）。
//
// handler 输入输出均为 gotd/td/tg 类型，各业务域的 handler
// 与注册见 help.go / auth.go / users.go / updates.go。Router 本身只负责协议外壳：
// 剥离 invokeWithLayer / initConnection / invokeWithoutUpdates / invokeAfter*，并兜底未注册 RPC。
type Router struct {
	cfg              Config
	log              *zap.Logger
	clock            clock.Clock
	deps             Deps
	dispatcher       *tg.ServerDispatcher
	clientInfoMu     sync.RWMutex
	clientInfo       map[clientInfoSessionKey]clientSessionInfo
	authInfo         map[[8]byte]clientSessionInfo
	authUserMu       sync.RWMutex
	authUsers        map[[8]byte]authUserCacheEntry
	authUserSF       singleflight.Group
	mediaCountSF     singleflight.Group
	dialogsPinnedSF  singleflight.Group
	channelFullBotSF singleflight.Group
	presence         *presenceTracker
	callbacks        *callbackRegistry
	inlines          *inlineRegistry
	webviews         *webViewRegistry
	loginTokens      *loginTokenRegistry
	botAPIUpdates    *botAPIUpdateNotifier
	instanceID       string
	channelFanout    *channelFanoutDispatcher
	// botAPIEnqueueQueue 把 user→bot 私聊消息的 bot_api_updates 写入移出发送者 RPC 同步
	// 路径（性能审计 H2）；队列满同步回退，绝不丢（队列行是 Bot API 投递真值）。
	botAPIEnqueueQueue *botAPIEnqueueDispatcher

	// presenceCandidateCache 缓存 presence fan-out 的候选 peer 集合（联系人 ∪ 私聊对端，
	// online 过滤前），按 userID 短 TTL；零值 sync.Map 即可用，无需构造器初始化。候选集变动
	// 很慢（加好友/开新私聊），短 TTL 内复用避免 updateStatus 每次重跑 ~25-30 条 hydration 查询。
	presenceCandidateCache sync.Map // userID(int64) -> *presenceCandidateEntry

	// botStatus 永久缓存 userID->是否 bot。bot 标志按账号不可变（BotFather 注册即定，普通用户永不变 bot），
	// 故可无 TTL 缓存。userIsBot 在 PFS 连接上被 announceSessionOnline 每 RPC 调用，不缓存则每次一发
	// Users.ByID 重投影——开群洪峰 ~50 并发时退化成 ~300ms herd（既拖尾延迟也飙 PG CPU）。零值即可用。
	botStatus sync.Map // userID(int64) -> bool
	// lastSeenPersist 记录每个 user 最近一次 last_seen 落库时刻（unix），用于写去抖：
	// updateStatus 高频续期时数秒内只落一次 DB。
	lastSeenPersist sync.Map // userID(int64) -> int64(unix)
	// tempKeyResolveCache 缓存 rawTempKeyID -> resolved perm（带过期），容量有界。
	tempKeyResolveCache          *tempKeyResolveCache
	storyProjectionCache         *storyProjectionCache
	storyPinnedCache             *storyPinnedAvailableCache
	storyPinnedListCache         *storyPinnedStoriesCache
	channelFullBotCache          *channelFullBotInfoCache
	userFullProjectionCache      *userFullProjectionCache
	peerSettingsProjectionCache  *peerSettingsProjectionCache
	channelFullProjectionCache   *channelFullProjectionCache
	availableReactionDocuments   availableReactionDocumentMapCache
	emojiStickers                *emojiStickerIndex
	notifySettings               *notifySettingsCache
	stickerCatalog               *stickerCatalogCache
	transientPrivateBigReactions transientPrivateBigReactionCache
	accountSettings              *accountSettingsCache
	// webPageResolveSem 是链接预览异步解析的并发信号量（有界）：发送后把 pending 占位
	// 解析为卡片并就地替换。满则丢弃任务（消息留 pending）。nil=未启用（测试可直接调
	// resolvePendingWebPage 同步验证）。
	webPageResolveSem chan struct{}
	// selfPhotoEchoPushDelay 是头像变更后向当前 session 回显 updateUser 的延迟
	// （见 photos.go pushSelfPhotoUpdateToCurrentSession）；<=0 时同步推送（测试用）。
	selfPhotoEchoPushDelay time.Duration
}

type clientInfoSessionKey struct {
	rawAuthKeyID [8]byte
	sessionID    int64
}

type clientSessionInfo struct {
	layer                int
	clientInfo           ClientInfo
	hasClientInfo        bool
	authKeyInfoChecked   bool
	authorizationChecked bool
}

type authUserCacheEntry struct {
	userID int64
	found  bool
}

// New 创建 Router，由各业务域自行注册其 RPC handler（registerHelp/Auth/Users/Updates）。
func New(cfg Config, deps Deps, log *zap.Logger, clk clock.Clock) *Router {
	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = fmt.Sprintf("%016x", randomNonZeroInt64())
	}
	r := &Router{cfg: cfg, log: log, clock: clk, deps: deps, presence: newPresenceTracker(), callbacks: newCallbackRegistry(), inlines: newInlineRegistry(botInlineQueryTTL, deps.Inline), webviews: newWebViewRegistry(webViewSessionTTL, deps.Inline), loginTokens: newLoginTokenRegistry(), botAPIUpdates: newBotAPIUpdateNotifier(), tempKeyResolveCache: newTempKeyResolveCache(cfg.TempKeyResolveCacheMaxEntries), storyProjectionCache: newStoryProjectionCache(clk.Now), storyPinnedCache: newStoryPinnedAvailableCache(clk.Now), storyPinnedListCache: newStoryPinnedStoriesCache(clk.Now), channelFullBotCache: newChannelFullBotInfoCache(clk.Now), userFullProjectionCache: newUserFullProjectionCache(clk.Now), peerSettingsProjectionCache: newPeerSettingsProjectionCache(clk.Now), channelFullProjectionCache: newChannelFullProjectionCache(clk.Now), emojiStickers: newEmojiStickerIndex(clk.Now), notifySettings: newNotifySettingsCache(clk.Now), stickerCatalog: newStickerCatalogCache(clk.Now), accountSettings: newAccountSettingsCache(clk.Now), instanceID: instanceID}
	r.channelFanout = newChannelFanoutDispatcher(r, defaultChannelFanoutShards, defaultChannelFanoutBuffer)
	r.botAPIEnqueueQueue = newBotAPIEnqueueDispatcher(log, defaultBotAPIEnqueueBuffer)
	r.webPageResolveSem = make(chan struct{}, webPageResolveConcurrency)
	r.selfPhotoEchoPushDelay = defaultSelfPhotoEchoPushDelay
	if cfg.DC > 0 {
		groupCallStreamDCID = cfg.DC
	}
	d := tg.NewServerDispatcher(r.fallback)

	r.registerHelp(d)
	r.registerAuth(d)
	r.registerUsers(d)
	r.registerUpdates(d)
	r.registerAccount(d)
	r.registerMessages(d)
	r.registerStickers(d)
	r.registerChannels(d)
	r.registerUpload(d)
	r.registerPhotos(d)
	r.registerFolders(d)
	r.registerChatlists(d)
	r.registerContacts(d)
	r.registerLangpack(d)
	r.registerStories(d)
	r.registerPhone(d)
	r.registerEncrypted(d)
	r.registerPayments(d)
	r.registerStats(d)
	r.registerPremium(d)
	r.registerAiCompose(d)
	r.registerBots(d)

	r.dispatcher = d
	return r
}

// Dispatch 路由一条 RPC 请求：先剥离 invokeWithLayer / initConnection /
// invokeWithoutUpdates / invokeAfter* 等 wrapper（注入 layer / 客户端信息到 ctx），
// 再按 TypeID 路由到 typed handler。满足 mtprotoedge.RPCHandler。
func (r *Router) Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error) {
	preStart := r.clock.Now()
	ctx = withInboundRPCBytes(ctx, b.Len())
	ctx = WithRawAuthKeyID(ctx, authKeyID)
	effectiveAuthKeyID, err := r.effectiveAuthKeyID(ctx, authKeyID, sessionID)
	if err != nil {
		return nil, internalErr()
	}
	tAuth := r.clock.Now()
	ctx = WithAuthKeyID(ctx, effectiveAuthKeyID)
	ctx = WithSessionID(ctx, sessionID)
	userID, hasUserID, err := r.effectiveUserID(ctx, authKeyID, effectiveAuthKeyID, sessionID)
	if err != nil {
		return nil, internalErr()
	}
	if hasUserID {
		ctx = WithUserID(ctx, userID)
	}
	tUser := r.clock.Now()
	info, hasClientMetadata, clientMetadataStored := r.clientSessionInfo(ctx)
	if authInfo, ok := r.clientSessionInfoFromAuthKey(ctx, effectiveAuthKeyID, info); ok {
		info = mergeClientSessionInfo(info, authInfo)
		hasClientMetadata = true
		r.rememberClientSessionInfo(ctx, info)
		clientMetadataStored = true
		if info.layer != 0 {
			if binder, okBinder := r.deps.Sessions.(ClientLayerBinder); okBinder {
				if rawAuthKeyID, okRaw := RawAuthKeyIDFrom(ctx); okRaw {
					binder.SetClientLayerForAuthKey(rawAuthKeyID, sessionID, info.layer)
				}
			}
		}
	}
	if hasUserID {
		if authInfo, ok := r.clientSessionInfoFromAuthorization(ctx, userID, effectiveAuthKeyID, info); ok {
			info = mergeClientSessionInfo(info, authInfo)
			hasClientMetadata = true
			r.rememberClientSessionInfo(ctx, info)
			clientMetadataStored = true
			// 冷启动回填：授权表恢复的 layer 即时下推到连接，防止本条 RPC handler
			// 执行期间的 push 仍按 canonical 227 发给老客户端（该分支只在元数据
			// 尚未入缓存时走到，稳态 RPC 不经过这里）。
			if info.layer != 0 {
				if binder, okBinder := r.deps.Sessions.(ClientLayerBinder); okBinder {
					if rawAuthKeyID, okRaw := RawAuthKeyIDFrom(ctx); okRaw {
						binder.SetClientLayerForAuthKey(rawAuthKeyID, sessionID, info.layer)
					}
				}
			}
		}
	}
	// 前置鉴权阶段（auth key 解析 / user 重校验 / client info）慢路径告警：超阈值才记，避免刷屏。
	// 常驻观测——正常应 ≪50ms；再现 >50ms 时按 auth_resolve / user_resolve / client_info 三段拆分
	// 即可定位（历史上 client_info ~1s 的根因是 DSN 用 localhost 触发 IPv6 连接回退，已改 127.0.0.1）。
	if r.log != nil {
		if tInfo := r.clock.Now(); tInfo.Sub(preStart) > 50*time.Millisecond {
			id, _ := b.PeekID()
			r.log.Info("slow pre-handler",
				zap.String("method", tlTypeName(id)),
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
		if info.layer != 0 {
			ctx = WithLayer(ctx, info.layer)
		}
		if info.hasClientInfo {
			ctx = WithClientInfo(ctx, info.clientInfo)
		}
	}
	return r.dispatch(ctx, b, 0)
}

func (r *Router) effectiveAuthKeyID(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64) ([8]byte, error) {
	var (
		cached    [8]byte
		hasCached bool
	)
	if r.deps.Sessions != nil {
		if id, ok := r.deps.Sessions.AuthKeyIDForSession(rawAuthKeyID, sessionID); ok {
			cached = id
			hasCached = true
		}
	}
	if hasCached {
		if cached == rawAuthKeyID || r.deps.Auth == nil {
			return cached, nil
		}
		// temp→perm 解析缓存：PFS 连接每帧都要解析一次 temp key（ResolveAuthKey 打 PG）。TTL 内复用
		// 上次解析、跳过 DB。仅当缓存的 perm 仍等于 session binder 当前 perm 才用（rebind 会改 binder
		// 且 onAuthBindTempAuthKey / 授权撤销都会显式 Delete 缓存，双保险防跨账号串号和被踢滞后）。
		ttl := r.cfg.TempKeyResolveCacheTTL
		if ttl > 0 {
			if perm, ok := r.tempKeyResolveCache.Get(rawAuthKeyID, cached, r.clock.Now()); ok {
				return perm, nil
			}
		}
		// cold burst 下并发 temp-key 解析用 singleflight 合并：同一 temp key 的 N 个并发 RPC 只打
		// 1 次 PG ResolveAuthKey（其余共享），避免开群/重连首帧 ~50 并发 herd（曾让 auth_resolve 飙到
		// ~1s）。解析结果 + 缓存写入在 SF 内（幂等共享）；session 绑定仍每 caller 各自做（按 session）。
		// 顺序调用不合并（SF 仅合并真并发），「每帧重校验」语义与固化测试 resolveCount 不变。
		v, sfErr, _ := r.authUserSF.Do(authKeyResolveSingleflightPrefix+string(rawAuthKeyID[:]), func() (any, error) {
			if ttl > 0 {
				if perm, ok := r.tempKeyResolveCache.Get(rawAuthKeyID, cached, r.clock.Now()); ok {
					return tempResolveResult{perm: perm, ok: true}, nil
				}
			}
			resolved, ok, err := r.deps.Auth.ResolveAuthKey(ctx, rawAuthKeyID)
			if err != nil {
				return tempResolveResult{}, err
			}
			if ok && ttl > 0 {
				r.tempKeyResolveCache.Store(rawAuthKeyID, resolved, r.clock.Now().Add(ttl), r.clock.Now())
			}
			return tempResolveResult{perm: resolved, ok: ok}, nil
		})
		if sfErr != nil {
			return [8]byte{}, sfErr
		}
		out := v.(tempResolveResult)
		if out.ok {
			if out.perm != cached {
				r.bindEffectiveAuthKey(rawAuthKeyID, sessionID, out.perm)
			}
			return out.perm, nil
		}
		r.tempKeyResolveCache.Delete(rawAuthKeyID)
		r.invalidateAuthUserCache(cached)
		r.bindEffectiveAuthKey(rawAuthKeyID, sessionID, rawAuthKeyID)
		return rawAuthKeyID, nil
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
	r.bindEffectiveAuthKey(rawAuthKeyID, sessionID, effective)
	return effective, nil
}

func (r *Router) bindEffectiveAuthKey(rawAuthKeyID [8]byte, sessionID int64, effective [8]byte) {
	if r.deps.Sessions != nil {
		r.deps.Sessions.BindAuthKeyForSession(rawAuthKeyID, sessionID, effective)
	}
}

func (r *Router) effectiveUserID(ctx context.Context, rawAuthKeyID, authKeyID [8]byte, sessionID int64) (int64, bool, error) {
	if userID, ok := UserIDFrom(ctx); ok {
		if r.deps.Sessions != nil {
			r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
		}
		return userID, true, nil
	}
	if r.deps.Sessions != nil {
		if userID, resolved := r.deps.Sessions.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
			if userID == 0 {
				if cachedUserID, ok := r.positiveCachedAuthUser(authKeyID); ok {
					r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, cachedUserID)
					r.announceSessionOnline(ctx, cachedUserID)
					return cachedUserID, true, nil
				}
			}
			return userID, userID != 0, nil
		}
	}
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	var (
		userID int64
		found  bool
		err    error
	)
	userID, found, err = r.lookupAuthUser(ctx, authKeyID)
	if err != nil {
		return 0, false, err
	}
	if r.deps.Sessions != nil {
		if cachedUserID, resolved := r.deps.Sessions.UserIDResolvedForAuthKey(rawAuthKeyID, sessionID); resolved {
			if cachedUserID != 0 || !found {
				return cachedUserID, cachedUserID != 0, nil
			}
		}
		if found {
			r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
			r.announceSessionOnline(ctx, userID)
		} else {
			r.deps.Sessions.BindUserForAuthKey(rawAuthKeyID, sessionID, 0)
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

func (r *Router) positiveCachedAuthUser(authKeyID [8]byte) (int64, bool) {
	userID, found, ok := r.cachedAuthUser(authKeyID)
	if !ok || !found || userID == 0 {
		return 0, false
	}
	return userID, true
}

func (r *Router) setAuthUserCache(authKeyID [8]byte, userID int64, found bool) {
	r.authUserMu.Lock()
	defer r.authUserMu.Unlock()
	if r.authUsers == nil {
		r.authUsers = make(map[[8]byte]authUserCacheEntry)
	}
	if _, exists := r.authUsers[authKeyID]; !exists {
		evictMapEntryIfFullLocked(r.authUsers, maxAuthUsersCached)
	}
	r.authUsers[authKeyID] = authUserCacheEntry{userID: userID, found: found}
}

func (r *Router) invalidateAuthUserCache(authKeyID [8]byte) {
	r.authUserMu.Lock()
	delete(r.authUsers, authKeyID)
	r.authUserMu.Unlock()
	r.clientInfoMu.Lock()
	delete(r.authInfo, authKeyID)
	r.clientInfoMu.Unlock()
	key := string(authKeyID[:])
	r.authUserSF.Forget(key)
	r.authUserSF.Forget(authKeyResolveSingleflightPrefix + key)
	r.authUserSF.Forget(authClientInfoSingleflightPrefix + key)
	r.authUserSF.Forget(authKeyClientInfoSingleflightPrefix + key)
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
		ctx = WithLayer(ctx, layer)
		r.rememberClientLayer(ctx, layer)
		return r.dispatch(ctx, b, depth+1)

	case tg.InvokeWithoutUpdatesRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		return r.dispatch(withInvokeWithoutUpdates(ctx), b, depth+1)

	case tg.InvokeAfterMsgRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		if _, err := b.Long(); err != nil {
			return nil, fmt.Errorf("decode invokeAfterMsg msg_id: %w", err)
		}
		return r.dispatch(ctx, b, depth+1)

	case tg.InvokeAfterMsgsRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		msgIDs, err := b.VectorHeader()
		if err != nil {
			return nil, fmt.Errorf("decode invokeAfterMsgs msg_ids: %w", err)
		}
		if msgIDs < 0 || msgIDs > maxInvokeAfterMsgIDs {
			return nil, fmt.Errorf("decode invokeAfterMsgs msg_ids: invalid count %d", msgIDs)
		}
		for i := 0; i < msgIDs; i++ {
			if _, err := b.Long(); err != nil {
				return nil, fmt.Errorf("decode invokeAfterMsgs msg_ids[%d]: %w", i, err)
			}
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
			zap.String("app_version", req.AppVersion),
			zap.Int("layer", LayerFrom(ctx)),
			zap.String("client_type", string(ClientTypeFrom(ctx))),
		)
		inner, ok := req.Query.(*rawObject)
		if !ok {
			return nil, fmt.Errorf("initConnection query: unexpected type %T", req.Query)
		}
		return r.dispatch(ctx, &bin.Buffer{Buf: inner.data}, depth+1)

	default:
		// 入站兼容统一入口（先于鉴权门/dispatcher）：layerwire 把老客户端请求升级为
		// canonical(227) 形态——①官方层漂移（生成表，flag-gated 新增→换 4 字节 id）
		// ②客户端构造器漂移（client_aliases 纯换 id / client-drift.tl 通用 body 变换：
		// 插 flags、按 kind 补默认、类型转换、改名映射）。替代了原先散落在 rpc 的
		// dispatchCompat + 各 handleLegacy* 解码器。提前到鉴权门之前，使后续一切只面对 227
		// 形态（鉴权白名单、dispatcher 均无需再认旧构造器 id）。
		if id != 0 {
			clientDrift := layerwire.IsClientDrift(id)
			if up, ok, err := layerwire.UpgradeInbound(id, b); ok {
				if err != nil {
					return nil, err
				}
				b = up
				newID, err := b.PeekID()
				if err != nil {
					return nil, err
				}
				id = newID
				if clientDrift {
					// 客户端漂移只能证明这是 Android 兼容路径；layer 仍以
					// invokeWithLayer 或授权记录里的真实观测值为准。
					ctx = r.withClientDriftMetadata(ctx, ClientTypeAndroid)
				}
			}
		}
		knownRequest, structuralErr := layerwire.ValidateRoutableRequest(b.Buf)
		if !knownRequest {
			if structuralErr != nil {
				return nil, mapLayerwirePreflightError(structuralErr)
			}
			// Unknown methods are opaque after the bounded/alignment check above: no generated
			// decoder will touch their body. Route them directly to the compatibility fallback
			// so every unknown constructor is traced, including pre-login probes.
			return r.fallback(ctx, b)
		}
		if r.deps.Auth != nil {
			if _, ok := UserIDFrom(ctx); !ok && !rpcAllowedWithoutAuthorization(id) {
				fields := append([]zap.Field{
					zap.String("method", tlTypeName(id)),
					zap.String("type_id", fmt.Sprintf("%#x", id)),
				}, r.contextLogFields(ctx)...)
				r.log.Info("RPC rejected before authorization", fields...)
				return nil, authKeyUnregisteredErr()
			}
		}
		if err := preflightRPCRequest(id, b); err != nil {
			return nil, err
		}
		// Run the same allocation-free schema walker used by layer aliases/DrKLO transforms on
		// every canonical request before gotd's generated decoder materializes vectors/bytes.
		// Method-specific caps above preserve exact existing RPC errors; this generic budget
		// closes variable-offset/nested-object paths and malformed constructor recursion.
		if structuralErr != nil {
			return nil, mapLayerwirePreflightError(structuralErr)
		}
		// 任何未包 invokeWithoutUpdates 的已登录 RPC 都把当前 session 视为 updates
		// 接收者。仅靠 updates.getState/getDifference 置位会漏掉 DrKLO 热恢复：
		// 它重连后不重建同步基线（pts 在进程内存里），只发普通业务请求，置位
		// 永不发生时主动推送会一直暂存直至超时丢弃，表现为另一端消息不再实时同步。
		r.maybeMarkSessionReceivesUpdates(ctx)
		dbBefore := dbtrace.SnapshotFromContext(ctx)
		start := time.Now()
		enc, err := r.dispatcher.Handle(ctx, b)
		dur := time.Since(start)
		dbDelta := dbtrace.SnapshotFromContext(ctx).Sub(dbBefore)
		fields := append([]zap.Field{
			zap.String("method", tlTypeName(id)),
			zap.String("type_id", fmt.Sprintf("%#x", id)),
			zap.Duration("dur", dur),
		}, r.contextLogFields(ctx)...)
		fields = dbtrace.AppendZapFields(fields, "handler_", dbDelta)
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

func mapLayerwirePreflightError(err error) error {
	if errors.Is(err, layerwire.ErrResourceLimit) {
		return inputRequestTooLongErr()
	}
	return inputRequestInvalidErr()
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

// maxClientInfoEntries / maxAuthInfoEntries 是客户端元数据缓存的容量上限兜底。
// 条目含客户端可控字符串且 session_id 由客户端任意生成，无上限时恶意客户端
// 在单连接上反复换 session_id / 轮换 temp auth key 可线速膨胀直至 OOM。
// 达到上限后驱逐任意旧条目：受害条目只损失 layer/clientType 缓存，
// 下一次 initConnection 或 authorization 回填即恢复。
const (
	maxClientInfoEntries = 1 << 16
	maxAuthInfoEntries   = 1 << 16
	// maxAuthUsersCached 给 authUsers 授权缓存设容量上界，与 clientInfo/authInfo 一致。
	// 原本无任何上限：设备轮换 temp 键而不显式登出时每个新 authKeyID 永久累积一条，
	// 只靠 logout/reset 的显式 invalidate 清理。达上限驱逐任意旧条目，下次按需回查回填。
	maxAuthUsersCached = 1 << 16
)

func (r *Router) rememberClientInfo(ctx context.Context, info ClientInfo) {
	info = normalizeClientInfo(info)
	layer := LayerFrom(ctx)
	r.mutateClientSessionInfo(ctx, func(sessionInfo *clientSessionInfo) {
		sessionInfo.clientInfo = info
		sessionInfo.hasClientInfo = true
		if layer != 0 {
			sessionInfo.layer = layer
		}
	})
	r.persistAuthKeyClientInfo(ctx, clientSessionInfo{layer: layer, clientInfo: info, hasClientInfo: true})
}

func (r *Router) rememberClientAPIID(ctx context.Context, apiID int) {
	if apiID == 0 {
		return
	}
	info := ClientInfo{APIID: apiID, Type: clientTypeFromAPIID(apiID)}
	sessionInfo := clientSessionInfo{clientInfo: info, hasClientInfo: true}
	r.rememberClientSessionInfo(ctx, sessionInfo)
	r.persistAuthKeyClientInfo(ctx, sessionInfo)
}

func (r *Router) rememberClientLayer(ctx context.Context, layer int) {
	if layer <= 0 {
		return
	}
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	persistAuthLayer := false
	r.clientInfoMu.Lock()
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	if hasAuthKeyID {
		if info, ok := r.authInfo[authKeyID]; !ok || info.layer != layer {
			persistAuthLayer = true
		}
	}
	sessionKey := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	sessionInfo, exists := r.clientInfo[sessionKey]
	// session 级记录缺失或 layer 变化时把新值即时下推到连接：invokeWithLayer 在
	// Dispatch 入口被处理（早于鉴权门与 updates 就绪置位），此时下推能保证同一请求
	// handler 执行期间触发的 pending flush / 并发 push 已按正确 layer 降级。记录已
	// 存在且相同（如客户端每条请求都带 wrapper）时跳过——连接侧已由注册播种或此前
	// 下推持有同值。
	notifyConn := !exists || sessionInfo.layer != layer
	sessionInfo.layer = layer
	if !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	r.clientInfo[sessionKey] = sessionInfo
	r.rememberAuthClientLayerLocked(rawAuthKeyID, layer)
	if hasAuthKeyID {
		r.rememberAuthClientLayerLocked(authKeyID, layer)
	}
	r.clientInfoMu.Unlock()
	if notifyConn {
		if binder, ok := r.deps.Sessions.(ClientLayerBinder); ok {
			binder.SetClientLayerForAuthKey(rawAuthKeyID, sessionID, layer)
		}
	}
	r.persistAuthKeyClientInfo(ctx, clientSessionInfo{layer: layer})
	if persistAuthLayer && r.deps.Auth != nil {
		if err := r.deps.Auth.UpdateAuthorizationLayer(ctx, authKeyID, layer); err != nil {
			r.log.Warn("update authorization layer failed",
				zap.Int("layer", layer),
				zap.String("auth_key_id", fmt.Sprintf("%x", authKeyID[:])),
				zap.Error(err))
		}
	}
}

func (r *Router) persistAuthKeyClientInfo(ctx context.Context, info clientSessionInfo) {
	if r.deps.Auth == nil {
		return
	}
	domainInfo := domainAuthKeyClientInfo(info)
	if domainInfo.Layer == 0 && domainInfo.DeviceModel == "" && domainInfo.Platform == "" &&
		domainInfo.SystemVersion == "" && domainInfo.APIID == 0 && domainInfo.AppVersion == "" {
		return
	}
	seen := make(map[[8]byte]struct{}, 2)
	if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok && rawAuthKeyID != ([8]byte{}) {
		seen[rawAuthKeyID] = struct{}{}
		if err := r.deps.Auth.UpdateAuthKeyClientInfo(ctx, rawAuthKeyID, domainInfo); err != nil {
			r.log.Warn("update auth key client info failed",
				zap.String("auth_key_id", fmt.Sprintf("%x", rawAuthKeyID[:])),
				zap.Error(err))
		}
	}
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok && authKeyID != ([8]byte{}) {
		if _, done := seen[authKeyID]; done {
			return
		}
		if err := r.deps.Auth.UpdateAuthKeyClientInfo(ctx, authKeyID, domainInfo); err != nil {
			r.log.Warn("update auth key client info failed",
				zap.String("auth_key_id", fmt.Sprintf("%x", authKeyID[:])),
				zap.Error(err))
		}
	}
}

// NegotiatedLayer returns the TL layer the given session negotiated via
// invokeWithLayer/initConnection. It is keyed first by (auth_key, session) then
// falls back to the stable auth_key — so a reconnect with a new session_id still
// inherits the layer within the process lifetime. ok=false means no layer was
// ever observed (cold connection, or the in-memory entry was evicted): callers
// MUST NOT overwrite a connection's last-known layer in that case, only treat it
// as canonical (227) when no value was ever recorded.
func (r *Router) NegotiatedLayer(authKeyID [8]byte, sessionID int64) (int, bool) {
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	if info, ok := r.clientInfo[clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID}]; ok && info.layer != 0 {
		return info.layer, true
	}
	if info, ok := r.authInfo[authKeyID]; ok && info.layer != 0 {
		return info.layer, true
	}
	return currentClientLayer, false
}

// mutateClientSessionInfo 在单个临界区内完成「读旧值-修改-写回」，避免
// RLock 读出与 Lock 写回之间被并发写覆盖的窗口。
func (r *Router) mutateClientSessionInfo(ctx context.Context, mutate func(*clientSessionInfo)) {
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
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	sessionKey := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	sessionInfo, exists := r.clientInfo[sessionKey]
	mutate(&sessionInfo)
	if !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	r.clientInfo[sessionKey] = sessionInfo
	r.rememberAuthClientInfoLocked(rawAuthKeyID, sessionInfo)
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		r.rememberAuthClientInfoLocked(authKeyID, sessionInfo)
	}
}

func (r *Router) rememberClientSessionInfo(ctx context.Context, sessionInfo clientSessionInfo) {
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
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	sessionKey := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	if _, exists := r.clientInfo[sessionKey]; !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	r.clientInfo[sessionKey] = mergeClientSessionInfo(r.clientInfo[sessionKey], sessionInfo)
	r.rememberAuthClientInfoLocked(rawAuthKeyID, sessionInfo)
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		r.rememberAuthClientInfoLocked(authKeyID, sessionInfo)
	}
}

func (r *Router) rememberClientSessionInfoIfMissing(ctx context.Context, sessionInfo clientSessionInfo) bool {
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return false
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return false
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	if r.clientSessionInfoStored(rawAuthKeyID, sessionID, authKeyID, hasAuthKeyID, sessionInfo) {
		return false
	}
	r.clientInfoMu.Lock()
	defer r.clientInfoMu.Unlock()
	if r.clientSessionInfoStoredLocked(rawAuthKeyID, sessionID, authKeyID, hasAuthKeyID, sessionInfo) {
		return false
	}
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	sessionKey := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	if _, exists := r.clientInfo[sessionKey]; !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	r.clientInfo[sessionKey] = mergeClientSessionInfo(r.clientInfo[sessionKey], sessionInfo)
	r.rememberAuthClientInfoLocked(rawAuthKeyID, sessionInfo)
	if hasAuthKeyID {
		r.rememberAuthClientInfoLocked(authKeyID, sessionInfo)
	}
	return true
}

func (r *Router) clientSessionInfoStored(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte, hasAuthKeyID bool, required clientSessionInfo) bool {
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	return r.clientSessionInfoStoredLocked(rawAuthKeyID, sessionID, authKeyID, hasAuthKeyID, required)
}

func (r *Router) clientSessionInfoStoredLocked(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte, hasAuthKeyID bool, required clientSessionInfo) bool {
	if !clientSessionInfoContains(r.clientInfo[clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}], required) {
		return false
	}
	if !clientSessionInfoContains(r.authInfo[rawAuthKeyID], required) {
		return false
	}
	if hasAuthKeyID && !clientSessionInfoContains(r.authInfo[authKeyID], required) {
		return false
	}
	return true
}

func clientSessionInfoContains(current, required clientSessionInfo) bool {
	if required.layer != 0 && current.layer != required.layer {
		return false
	}
	if required.hasClientInfo && (!current.hasClientInfo || current.clientInfo != required.clientInfo) {
		return false
	}
	if required.authorizationChecked && !current.authorizationChecked {
		return false
	}
	if required.authKeyInfoChecked && !current.authKeyInfoChecked {
		return false
	}
	return true
}

func (r *Router) rememberAuthClientInfoLocked(authKeyID [8]byte, info clientSessionInfo) {
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	if _, exists := r.authInfo[authKeyID]; !exists {
		evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
	}
	current := r.authInfo[authKeyID]
	r.authInfo[authKeyID] = mergeClientSessionInfo(current, info)
}

func (r *Router) rememberAuthClientLayerLocked(authKeyID [8]byte, layer int) {
	if layer <= 0 {
		return
	}
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	if _, exists := r.authInfo[authKeyID]; !exists {
		evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
	}
	info := r.authInfo[authKeyID]
	info.layer = layer
	r.authInfo[authKeyID] = info
}

// forgetClientSessionInfo 随连接下线移除该 session 的元数据缓存条目，并清掉以该 raw
// auth_key 为键的 authInfo 兜底条目，使 authInfo 收敛到活跃 raw auth key（主导的单
// session/key 场景下严格回收）。共享同一 raw auth_key 的其它 session 若仍在线，会在下一次
// initConnection/authorization 回填——authInfo 只是廉价的元数据兜底。temp→perm 解析后以
// 业务 perm key 为键的条目仍靠容量上限兜底（SessionOffline 不带业务 key，无法在此精确清理）。
func (r *Router) forgetClientSessionInfo(rawAuthKeyID [8]byte, sessionID int64) {
	r.clientInfoMu.Lock()
	delete(r.clientInfo, clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID})
	delete(r.authInfo, rawAuthKeyID)
	r.clientInfoMu.Unlock()
}

func evictMapEntryIfFullLocked[K comparable, V any](m map[K]V, limit int) {
	if len(m) < limit {
		return
	}
	for k := range m {
		delete(m, k)
		return
	}
}

func (r *Router) clientSessionInfo(ctx context.Context) (clientSessionInfo, bool, bool) {
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return clientSessionInfo{}, false, false
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return clientSessionInfo{}, false, false
	}
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	info, ok := r.clientInfo[clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}]
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	if authInfo, authOK := r.authInfo[rawAuthKeyID]; authOK {
		info = mergeClientSessionInfo(info, authInfo)
		ok = true
	}
	if hasAuthKeyID {
		if authInfo, authOK := r.authInfo[authKeyID]; authOK {
			info = mergeClientSessionInfo(info, authInfo)
			ok = true
		}
	}
	if !ok {
		return info, false, false
	}
	return info, true, r.clientSessionInfoStoredLocked(rawAuthKeyID, sessionID, authKeyID, hasAuthKeyID, info)
}

func (r *Router) cachedResolvedAuthClientInfo(authKeyID [8]byte) (clientSessionInfo, bool) {
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	info, ok := r.authInfo[authKeyID]
	if !ok || clientSessionInfoNeedsAuthorization(info) {
		return clientSessionInfo{}, false
	}
	return info, true
}

func (r *Router) cachedResolvedAuthKeyClientInfo(authKeyID [8]byte) (clientSessionInfo, bool) {
	r.clientInfoMu.RLock()
	defer r.clientInfoMu.RUnlock()
	info, ok := r.authInfo[authKeyID]
	if !ok || clientSessionInfoNeedsAuthKeyInfo(info) {
		return clientSessionInfo{}, false
	}
	return info, true
}

func (r *Router) clientSessionInfoFromAuthKey(ctx context.Context, authKeyID [8]byte, current clientSessionInfo) (clientSessionInfo, bool) {
	if !clientSessionInfoNeedsAuthKeyInfo(current) || r.deps.Auth == nil || authKeyID == ([8]byte{}) {
		return clientSessionInfo{}, false
	}
	v, err, _ := r.authUserSF.Do(authKeyClientInfoSingleflightPrefix+string(authKeyID[:]), func() (any, error) {
		if cached, ok := r.cachedResolvedAuthKeyClientInfo(authKeyID); ok {
			return cached, nil
		}
		info, found, err := r.deps.Auth.AuthKeyClientInfo(ctx, authKeyID)
		if err != nil {
			return clientSessionInfo{}, err
		}
		if !found {
			return clientSessionInfo{authKeyInfoChecked: true}, nil
		}
		return clientSessionInfoFromAuthKeyClientInfo(info, current), nil
	})
	if err != nil {
		return clientSessionInfo{}, false
	}
	info := v.(clientSessionInfo)
	if info.layer == 0 && !info.hasClientInfo && !info.authKeyInfoChecked {
		return clientSessionInfo{}, false
	}
	return info, true
}

func mergeClientSessionInfo(base, fallback clientSessionInfo) clientSessionInfo {
	if base.layer == 0 {
		base.layer = fallback.layer
	}
	if !base.hasClientInfo && fallback.hasClientInfo {
		base.clientInfo = fallback.clientInfo
		base.hasClientInfo = true
	}
	if fallback.authorizationChecked {
		base.authorizationChecked = true
	}
	if fallback.authKeyInfoChecked {
		base.authKeyInfoChecked = true
	}
	return base
}

func clientSessionInfoFromAuthKeyClientInfo(item domain.AuthKeyClientInfo, current clientSessionInfo) clientSessionInfo {
	info := clientSessionInfo{
		layer:              item.Layer,
		authKeyInfoChecked: true,
		clientInfo: ClientInfo{
			APIID:         item.APIID,
			DeviceModel:   item.DeviceModel,
			SystemVersion: item.SystemVersion,
			AppVersion:    item.AppVersion,
			Type:          ClientType(item.Platform),
		},
	}
	info.clientInfo = normalizeClientInfo(info.clientInfo)
	info.hasClientInfo = info.clientInfo.ClientType() != ClientTypeUnknown ||
		info.clientInfo.DeviceModel != "" ||
		info.clientInfo.SystemVersion != "" ||
		info.clientInfo.AppVersion != "" ||
		info.clientInfo.APIID != 0
	if info.layer == 0 && current.layer != 0 {
		info.layer = current.layer
	}
	return info
}

func domainAuthKeyClientInfo(info clientSessionInfo) domain.AuthKeyClientInfo {
	out := domain.AuthKeyClientInfo{Layer: info.layer}
	if info.hasClientInfo {
		out.APIID = info.clientInfo.APIID
		out.DeviceModel = info.clientInfo.DeviceModel
		out.SystemVersion = info.clientInfo.SystemVersion
		out.AppVersion = info.clientInfo.AppVersion
		out.Platform = string(info.clientInfo.ClientType())
	}
	return out
}

func (r *Router) clientSessionInfoFromAuthorization(ctx context.Context, userID int64, authKeyID [8]byte, current clientSessionInfo) (clientSessionInfo, bool) {
	if !clientSessionInfoNeedsAuthorization(current) || r.deps.Auth == nil || userID == 0 {
		return clientSessionInfo{}, false
	}
	v, err, _ := r.authUserSF.Do(authClientInfoSingleflightPrefix+string(authKeyID[:]), func() (any, error) {
		if cached, ok := r.cachedResolvedAuthClientInfo(authKeyID); ok {
			return cached, nil
		}
		item, found, err := r.deps.Auth.Authorization(ctx, authKeyID)
		if err != nil {
			return clientSessionInfo{}, err
		}
		if !found || item.UserID != userID || item.PasswordPending {
			return clientSessionInfo{authorizationChecked: true}, nil
		}
		return clientSessionInfoFromAuthorizationRecord(item, current), nil
	})
	if err != nil {
		return clientSessionInfo{}, false
	}
	return v.(clientSessionInfo), true
}

func clientSessionInfoFromAuthorizationRecord(item domain.Authorization, current clientSessionInfo) clientSessionInfo {
	info := clientSessionInfo{
		layer:                item.Layer,
		authorizationChecked: true,
		clientInfo: ClientInfo{
			APIID:         item.APIID,
			DeviceModel:   item.DeviceModel,
			SystemVersion: item.SystemVersion,
			AppVersion:    item.AppVersion,
			Type:          ClientType(item.Platform),
		},
	}
	info.clientInfo = normalizeClientInfo(info.clientInfo)
	info.hasClientInfo = info.clientInfo.ClientType() != ClientTypeUnknown ||
		info.clientInfo.DeviceModel != "" ||
		info.clientInfo.SystemVersion != "" ||
		info.clientInfo.AppVersion != "" ||
		info.clientInfo.APIID != 0
	if info.layer == 0 {
		if current.layer != 0 {
			info.layer = current.layer
		}
	}
	return info
}

func clientSessionInfoNeedsAuthorization(info clientSessionInfo) bool {
	if info.authorizationChecked {
		return false
	}
	return info.layer == 0 || !info.hasClientInfo || info.clientInfo.ClientType() == ClientTypeUnknown
}

func clientSessionInfoNeedsAuthKeyInfo(info clientSessionInfo) bool {
	if info.authKeyInfoChecked {
		return false
	}
	return info.layer == 0 || !info.hasClientInfo || info.clientInfo.ClientType() == ClientTypeUnknown
}

// fallback 处理未注册的 RPC：记录到 compatibility trace（落兼容矩阵），
// 返回 NOT_IMPLEMENTED rpc_error 让客户端继续运行而非断连。
func (r *Router) fallback(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	// DrKLO 12.8.1 的 theme 方法构造器比 gotd schema 新,dispatcher 匹配不上;
	// 在落到「未实现」前先按 DrKLO 字段序手动解码处理。
	if enc, handled, err := r.tryLegacyThemeRPC(ctx, b); handled {
		return enc, err
	}
	id, _ := b.PeekID()
	fields := append([]zap.Field{
		zap.String("method", tlTypeName(id)),
		zap.String("type_id", fmt.Sprintf("%#x", id)),
	}, r.contextLogFields(ctx)...)
	r.log.Warn("Unhandled RPC (compatibility trace)", fields...)
	return nil, notImplementedErr()
}

func (r *Router) contextLogFields(ctx context.Context) []zap.Field {
	fields := []zap.Field{
		zap.Int("layer", LayerFrom(ctx)),
		zap.String("client_type", string(ClientTypeFrom(ctx))),
	}
	if info, ok := ClientInfoFrom(ctx); ok && info.AppVersion != "" {
		fields = append(fields, zap.String("app_version", info.AppVersion))
	}
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
