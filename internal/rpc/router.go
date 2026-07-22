package rpc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	compatandroid "telesrv/internal/compat/android"
	"telesrv/internal/domain"
	"telesrv/internal/links"
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
	// PublicAppScheme/PublicAppLinkBase 控制客户端 deep link；base 为空时
	// 保持 <scheme>://<route>，非空时生成 <base>/<route>。
	PublicAppScheme   string
	PublicAppLinkBase string
	// TempKeyResolveCacheTTL 是 PFS temp→perm auth key 解析的进程内缓存有效期。>0 时同一 temp key
	// 在 TTL 内复用上次解析、跳过每帧 ResolveAuthKey 的 PG 查询；0（默认/测试）关闭=每帧重校验。
	// 显式撤销会删除协议 auth key、清缓存并断开活跃连接；TTL 只影响自然过期或异常路径下的
	// 下一次重新解析。re-bind 由 onAuthBindTempAuthKey 显式失效，避免跨账号串号。
	TempKeyResolveCacheTTL time.Duration
	// TempKeyResolveCacheMaxEntries 是 temp→perm 解析缓存容量；<=0 用内置默认。
	TempKeyResolveCacheMaxEntries int
}

// Router 把解密后的 RPC 请求按 semantic method 路由到 typed handler（tlprofile.Dispatcher）。
//
// handler 输入输出均为 gotd/td/tg 类型，各业务域的 handler
// 与注册见 help.go / auth.go / users.go / updates.go。Router 本身只负责协议外壳：
// 剥离 invokeWithLayer / initConnection / invokeWithoutUpdates / invokeAfter*，并兜底未注册 RPC。
type Router struct {
	cfg               Config
	appLinks          links.AppLinkBuilder
	log               *zap.Logger
	clock             clock.Clock
	deps              Deps
	dispatcher        *tlprofile.Dispatcher
	clientInfoMu      sync.RWMutex
	clientInfo        map[clientInfoSessionKey]clientSessionInfo
	authInfo          map[[8]byte]clientSessionInfo
	authLayerEvidence map[[8]byte]authLayerDefaultEvidence
	authLayerCommit   [authLayerCommitStripes]sync.Mutex
	// authLayerSafeEvictionFloor is the monotonic lower bound supplied by the
	// edge-owned exact admission tracker. Evidence below this sequence has no
	// live owner that can still enter publication and is therefore capacity-
	// evictable. clientInfoMu protects it together with authLayerEvidence.
	authLayerSafeEvictionFloor uint64
	exactProfileMu             sync.RWMutex
	exactProfiles              map[clientInfoSessionKey]exactSessionProfileEntry
	// exactProfileEarliestExpiry is a conservative lower bound. Refresh/deletes
	// may leave it earlier than the true minimum (safe); a capacity scan
	// recomputes it exactly. This makes the normal all-live full-capacity reject
	// O(1) without a heap on every refresh.
	exactProfileEarliestExpiry time.Time
	authUserMu                 sync.RWMutex
	authUsers                  map[[8]byte]authUserCacheEntry
	authUserSF                 singleflight.Group
	mediaCountSF               singleflight.Group
	dialogsPinnedSF            singleflight.Group
	channelFullBotSF           singleflight.Group
	presence                   *presenceTracker
	callbacks                  *callbackRegistry
	inlines                    *inlineRegistry
	webviews                   *webViewRegistry
	loginTokens                *loginTokenRegistry
	botAPIUpdates              *botAPIUpdateNotifier
	instanceID                 string
	channelFanout              *channelFanoutDispatcher
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
	accountFreezeWake            chan struct{}
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
	layer int
	// layerObservationID is the durable cross-restart ordering token from
	// auth_keys. It is independent from process-local admission sequence.
	layerObservationID int64
	// layerAdmissionSeq is the server-assigned admission order of explicit
	// invokeWithLayer evidence. It is retained on auth-key defaults and on the
	// exact raw-session metadata shadow written by admission publication. Client
	// msg_id is deliberately not used because its ordering is session-local.
	layerAdmissionSeq      uint64
	wrapperMsgID           int64
	clientInfoAdmissionSeq uint64
	clientInfo             ClientInfo
	hasClientInfo          bool
	authKeyInfoChecked     bool
	authorizationChecked   bool
	layerBlocked           bool
	layerBlockedByAuthKey  bool
}

type exactSessionProfileEntry struct {
	layer         int
	msgID         int64
	observationID int64
	expiresAt     time.Time
}

// ExactSessionProfileCapacityError reports that the bounded replay/profile
// registry contains only still-live logical sessions. Callers must fail closed
// for shared profile mutation; no unexpired msg_id watermark was evicted.
// The marker method lets mtprotoedge recognize the condition without importing
// the higher-level rpc package.
type ExactSessionProfileCapacityError struct {
	Limit int
}

func (e *ExactSessionProfileCapacityError) Error() string {
	return fmt.Sprintf("exact session profile capacity exhausted (limit %d)", e.Limit)
}

func (*ExactSessionProfileCapacityError) ExactSessionProfileCapacity() {}

type authLayerDefaultEvidence struct {
	layer    int
	sequence uint64
	durable  bool
}

type authUserCacheEntry struct {
	userID int64
	found  bool
}

// New 创建 Router，由各业务域自行注册其 RPC handler（registerHelp/Auth/Users/Updates）。
func New(cfg Config, deps Deps, log *zap.Logger, clk clock.Clock) *Router {
	assertNoTypedNilDeps(deps)
	appLinks, err := links.NewAppLinkBuilder(cfg.PublicAppScheme, cfg.PublicAppLinkBase)
	if err != nil {
		panic(fmt.Sprintf("initialize public app links: %v", err))
	}
	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = fmt.Sprintf("%016x", randomNonZeroInt64())
	}
	r := &Router{cfg: cfg, appLinks: appLinks, log: log, clock: clk, deps: deps, exactProfiles: make(map[clientInfoSessionKey]exactSessionProfileEntry), authLayerEvidence: make(map[[8]byte]authLayerDefaultEvidence), presence: newPresenceTracker(), callbacks: newCallbackRegistry(deps.BotCallbacks), inlines: newInlineRegistry(botInlineQueryTTL, deps.Inline), webviews: newWebViewRegistry(webViewSessionTTL, deps.Inline), loginTokens: newLoginTokenRegistry(), botAPIUpdates: newBotAPIUpdateNotifier(), tempKeyResolveCache: newTempKeyResolveCache(cfg.TempKeyResolveCacheMaxEntries), storyProjectionCache: newStoryProjectionCache(clk.Now), storyPinnedCache: newStoryPinnedAvailableCache(clk.Now), storyPinnedListCache: newStoryPinnedStoriesCache(clk.Now), channelFullBotCache: newChannelFullBotInfoCache(clk.Now), userFullProjectionCache: newUserFullProjectionCache(clk.Now), peerSettingsProjectionCache: newPeerSettingsProjectionCache(clk.Now), channelFullProjectionCache: newChannelFullProjectionCache(clk.Now), emojiStickers: newEmojiStickerIndex(clk.Now), notifySettings: newNotifySettingsCache(clk.Now), stickerCatalog: newStickerCatalogCache(clk.Now), accountSettings: newAccountSettingsCache(clk.Now), accountFreezeWake: make(chan struct{}, 1), instanceID: instanceID}
	r.channelFanout = newChannelFanoutDispatcher(r, defaultChannelFanoutShards, defaultChannelFanoutBuffer)
	r.botAPIEnqueueQueue = newBotAPIEnqueueDispatcher(log, defaultBotAPIEnqueueBuffer)
	r.webPageResolveSem = make(chan struct{}, webPageResolveConcurrency)
	r.selfPhotoEchoPushDelay = defaultSelfPhotoEchoPushDelay
	if cfg.DC > 0 {
		groupCallStreamDCID = cfg.DC
	}
	d := tlprofile.NewDispatcher()
	if err := registerLayerRPCAdmissionFieldPreflights(d); err != nil {
		panic(fmt.Sprintf("register exact layer RPC admission policy: %v", err))
	}
	r.registerAndroidLayerRPCAdapter(d)
	d.OnWrappers(r.consumeLayerRPCWrappers)

	r.registerHelp(d)
	r.registerAuth(d)
	r.registerUsers(d)
	r.registerUpdates(d)
	r.registerAccount(d)
	r.registerMessages(d)
	r.registerStickers(d)
	r.registerChannels(d)
	r.registerCommunities(d)
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
	r.registerEphemeral(d)

	r.dispatcher = d
	return r
}

// assertNoTypedNilDeps rejects partially constructed optional dependencies at
// the composition boundary. A Go interface containing a nil concrete pointer
// is not equal to nil, so handler-level availability checks would otherwise
// admit it and panic only when the first method is invoked.
//
// This is an invariant check, not a compatibility fallback: callers must
// either inject a fully constructed implementation or leave the interface nil.
func assertNoTypedNilDeps(deps Deps) {
	value := reflect.ValueOf(deps)
	typeOfDeps := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Interface || field.IsNil() {
			continue
		}
		implementation := field.Elem()
		switch implementation.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if implementation.IsNil() {
				panic(fmt.Sprintf("rpc: dependency %s is a typed nil %s", typeOfDeps.Field(i).Name, implementation.Type()))
			}
		}
	}
}

func registerRPC[T bin.Object](d *tlprofile.Dispatcher, method tlprofile.SemanticID, handler func(context.Context, T) (any, error)) {
	if d == nil || handler == nil {
		panic("rpc: register nil canonical RPC handler or dispatcher")
	}
	err := d.Register(method, func(ctx context.Context, object bin.Object) (any, error) {
		request, ok := object.(T)
		if !ok {
			return nil, fmt.Errorf("rpc: semantic %#016x decoded unexpected canonical request %T", uint64(method), object)
		}
		return handler(ctx, request)
	})
	if err != nil {
		panic(fmt.Sprintf("rpc: register canonical RPC %#016x: %v", uint64(method), err))
	}
}

// Dispatch routes one RPC and preserves the historical two-value API used by
// domain/RPC tests. The MTProto edge uses DispatchWithMethod so outbound
// scheduling sees the exact innermost method rather than an invoke wrapper.
func (r *Router) Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error) {
	enc, _, err := r.DispatchWithMethod(ctx, authKeyID, sessionID, b)
	return enc, err
}

// DispatchWithMethod 路由一条 RPC 请求：先剥离 invokeWithLayer / initConnection /
// invokeWithoutUpdates / invokeAfter* 等 wrapper（注入 layer / 客户端信息到 ctx），
// 再按 TypeID 路由到 typed handler，并返回 exact innermost method。
func (r *Router) DispatchWithMethod(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, string, error) {
	if b == nil {
		return nil, "", inputRequestInvalidErr()
	}
	method := "unknown"
	if id, peekErr := b.PeekID(); peekErr == nil {
		method = tlTypeName(id)
	}
	ctx, updatesDelivery, err := r.prepareRPCDispatchContext(ctx, authKeyID, sessionID, b.Len(), method)
	if err != nil {
		return nil, "", err
	}
	meta := rpcDispatchMetadata{}
	enc, err := r.dispatch(ctx, b, 0, &meta)
	if err == nil && enc != nil {
		r.registerUpdatesDeliveryPlan(ctx, updatesDelivery)
	}
	return enc, meta.method, err
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
		if r.deps.Auth == nil {
			return cached, nil
		}
		if cached == rawAuthKeyID {
			// raw==business is a permanent-key fast path only when the edge confirms
			// the raw key has no protocol expiry. A temporary session may have cached
			// raw before another concurrent session completes auth.bindTempAuthKey;
			// it must keep resolving until the durable binding becomes visible.
			metadata, ok := r.deps.Sessions.(RawAuthKeyMetadataProvider)
			if ok {
				expiresAt, found := metadata.AuthKeyExpiresAtForSession(rawAuthKeyID, sessionID)
				if found && expiresAt == 0 {
					return cached, nil
				}
			}
			// Missing metadata and a session lookup miss both fail closed to the
			// durable resolver. Treating either as proof of permanence recreates the
			// raw-temp identity split when an alternate SessionBinder is installed.
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
	if info, ok := r.authInfo[authKeyID]; ok {
		// Authorization membership can change independently of protocol
		// metadata. Preserve the Layer/default ordering watermark so an older
		// in-flight publisher cannot roll durable state back after login/logout.
		info.authorizationChecked = false
		if !info.layerBlockedByAuthKey {
			info.layerBlocked = false
		}
		r.authInfo[authKeyID] = info
	}
	r.clientInfoMu.Unlock()
	key := string(authKeyID[:])
	r.authUserSF.Forget(key)
	r.authUserSF.Forget(authKeyResolveSingleflightPrefix + key)
	r.authUserSF.Forget(authClientInfoSingleflightPrefix + key)
	r.authUserSF.Forget(authKeyClientInfoSingleflightPrefix + key)
	r.authUserSF.Forget(inheritedAuthKeyLayerSingleflightPrefix + key)
	r.authUserSF.Forget(durableInheritedAuthKeyLayerSingleflightPrefix + key)
}

type rpcDispatchMetadata struct {
	method string
}

func (r *Router) dispatch(ctx context.Context, b *bin.Buffer, depth int, meta *rpcDispatchMetadata) (bin.Encoder, error) {
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
		return r.dispatch(ctx, b, depth+1, meta)

	case tg.InvokeWithoutUpdatesRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		return r.dispatch(withInvokeWithoutUpdates(ctx), b, depth+1, meta)

	case tg.InvokeAfterMsgRequestTypeID:
		if err := b.ConsumeID(id); err != nil {
			return nil, err
		}
		if _, err := b.Long(); err != nil {
			return nil, fmt.Errorf("decode invokeAfterMsg msg_id: %w", err)
		}
		return r.dispatch(ctx, b, depth+1, meta)

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
		return r.dispatch(ctx, b, depth+1, meta)

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
		return r.dispatch(ctx, &bin.Buffer{Buf: inner.data}, depth+1, meta)

	default:
		// Router.Dispatch is a legacy test seam. Production uses generated exact
		// Layer admission, whose unknown-method view invokes the same static DrKLO
		// overlay while sharing the outer request budget.
		profile := tlprofile.ProfileCanonical
		if selected, ok := tlprofile.ResolveProfile(LayerFrom(ctx)); ok {
			profile = selected
		}
		if id != 0 {
			_, official := tlprofile.SemanticForWireID(profile, id)
			if !official {
				if up, ok, err := compatandroid.UpgradePrivateLayerRPC(profile, b, tlprofile.Limits{}); ok {
					if err != nil {
						return nil, inputRequestInvalidErr()
					}
					b = up
					newID, err := b.PeekID()
					if err != nil {
						return nil, err
					}
					id = newID
					// A private constructor identifies the DrKLO compatibility seam but
					// never selects or changes the connection's Layer profile.
					ctx = r.withClientDriftMetadata(ctx, ClientTypeAndroid)
				}
			}
		}
		if meta != nil {
			meta.method = tlTypeName(id)
		}
		semantic, knownRequest := tlprofile.SemanticForWireID(tlprofile.ProfileCanonical, id)
		if knownRequest {
			category, _, named := tlprofile.SemanticName(semantic)
			knownRequest = named && category == "function"
		}
		if !knownRequest {
			// Unknown methods remain opaque and go to the compatibility trace.
			return r.fallback(ctx, b)
		}
		if !r.dispatcher.Has(semantic) {
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
		if err := r.checkFrozenRPC(ctx, tlTypeName(id)); err != nil {
			return nil, err
		}
		// 任何未包 invokeWithoutUpdates 的已登录 RPC 都把当前 session 视为 updates
		// 接收者。仅靠 updates.getState/getDifference 置位会漏掉 DrKLO 热恢复：
		// 它重连后不重建同步基线（pts 在进程内存里），只发普通业务请求，置位
		// 永不发生时主动推送会一直暂存直至超时丢弃，表现为另一端消息不再实时同步。
		r.maybeMarkSessionReceivesUpdates(ctx)
		dbBefore := dbtrace.SnapshotFromContext(ctx)
		start := time.Now()
		admission, err := r.dispatcher.Admit(profile, b, tlprofile.Limits{})
		if err != nil {
			return nil, err
		}
		exact, err := r.dispatcher.Dispatch(ctx, admission)
		var enc bin.Encoder = exact
		if err == nil && exact != nil {
			if canonical, ok := exact.CanonicalValue().(bin.Encoder); ok {
				enc = canonical
			}
		}
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
// 达到上限后驱逐任意旧条目：受害条目只损失进程内默认/客户端描述缓存，
// 下一次 auth-key resolver、initConnection 或 authorization 回填即可恢复。
// exact profile 由下方独立的 same-session registry 管理，永远优先于默认。
const (
	maxClientInfoEntries = 1 << 16
	maxAuthInfoEntries   = 1 << 16
	// exactSessionProfileLegacyTTL covers the normal 300-second client msg_id
	// window plus the allowed 30-second future skew and a one-second equality
	// margin. Ordered evidence uses its own msg_id timestamp instead of blindly
	// refreshing this TTL on duplicate replay.
	exactSessionProfileLegacyTTL = 331 * time.Second
	// exactSessionProfileTTL remains the maximum/legacy force-path retention
	// used by focused registry tests. Real ordered evidence uses the expiry
	// derived above from its msg_id.
	exactSessionProfileTTL        = exactSessionProfileLegacyTTL
	maxExactSessionProfileEntries = 1 << 16
	// maxAuthUsersCached 给 authUsers 授权缓存设容量上界，与 clientInfo/authInfo 一致。
	// 原本无任何上限：设备轮换 temp 键而不显式登出时每个新 authKeyID 永久累积一条，
	// 只靠 logout/reset 的显式 invalidate 清理。达上限驱逐任意旧条目，下次按需回查回填。
	maxAuthUsersCached = 1 << 16
)

func (r *Router) rememberClientInfo(ctx context.Context, info ClientInfo) {
	r.rememberClientInfoAt(ctx, info, 0)
}

func (r *Router) rememberClientInfoAt(ctx context.Context, info ClientInfo, admissionSeq uint64) {
	info = normalizeClientInfo(info)
	rawAuthKeyID, hasRawAuthKeyID := RawAuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasRawAuthKeyID || !hasSessionID {
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	if !hasAuthKeyID {
		authKeyID = rawAuthKeyID
	}
	unlockCommit := r.lockAuthLayerCommit(rawAuthKeyID, authKeyID)
	defer unlockCommit()

	r.clientInfoMu.Lock()
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	sessionInfo, exists := r.clientInfo[key]
	if !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	sessionInfo.clientInfo = info
	sessionInfo.hasClientInfo = true
	if admissionSeq > 0 {
		sessionInfo.clientInfoAdmissionSeq = admissionSeq
	}
	r.clientInfo[key] = sessionInfo

	publishAuthMetadata := true
	for _, id := range [][8]byte{rawAuthKeyID, authKeyID} {
		current := r.authInfo[id]
		if current.clientInfoAdmissionSeq > admissionSeq ||
			(admissionSeq == 0 && current.clientInfoAdmissionSeq > 0) {
			publishAuthMetadata = false
			break
		}
		if admissionSeq > 0 && current.clientInfoAdmissionSeq == admissionSeq &&
			current.hasClientInfo && current.clientInfo != info {
			publishAuthMetadata = false
			break
		}
	}
	if publishAuthMetadata {
		for _, id := range [][8]byte{rawAuthKeyID, authKeyID} {
			if id == ([8]byte{}) {
				continue
			}
			if r.authInfo == nil {
				r.authInfo = make(map[[8]byte]clientSessionInfo)
			}
			if _, exists := r.authInfo[id]; !exists {
				evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
			}
			current := r.authInfo[id]
			current.clientInfo = info
			current.hasClientInfo = true
			if admissionSeq > 0 {
				current.clientInfoAdmissionSeq = admissionSeq
			}
			r.authInfo[id] = current
		}
	}
	r.clientInfoMu.Unlock()
	// initConnection metadata is not fresh Layer evidence when admitted under an
	// inherited default. Only rememberClientLayer (explicit invokeWithLayer)
	// advances the auth-key-wide durable default.
	if publishAuthMetadata {
		r.persistAuthKeyClientInfo(ctx, clientSessionInfo{clientInfo: info, hasClientInfo: true})
	}
}

func (r *Router) rememberClientAPIID(ctx context.Context, apiID int) {
	if apiID == 0 {
		return
	}
	info := normalizeClientInfo(ClientInfo{APIID: apiID, Type: clientTypeFromAPIID(apiID)})
	sessionInfo := clientSessionInfo{clientInfo: info, hasClientInfo: true}
	r.rememberClientSessionInfo(ctx, sessionInfo)
	// api_id is weak evidence. If initConnection (or durable restoration) has
	// already supplied stronger client metadata, persist that effective session
	// fact instead of letting auth.sendCode overwrite it on an id collision.
	if effective, ok, _ := r.clientSessionInfo(ctx); ok {
		sessionInfo = effective
	}
	sessionInfo.layer = 0
	r.persistAuthKeyClientInfo(ctx, sessionInfo)
}

func (r *Router) rememberClientLayer(ctx context.Context, layer int) {
	r.rememberClientLayerAt(ctx, layer, 0)
}

func (r *Router) rememberClientLayerAt(ctx context.Context, layer int, msgID int64) {
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
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawAuthKeyID, sessionID, layer, msgID); err != nil {
		// Only invalid identities and profiles not generated into this binary can
		// fail. Never clamp a future persisted/wire Layer to canonical.
		if r.log != nil {
			r.log.Warn("refuse unsupported exact session layer",
				zap.Int("layer", layer),
				zap.Int64("session_id", sessionID),
				zap.String("auth_key_id", fmt.Sprintf("%x", rawAuthKeyID[:])),
				zap.Error(err))
		}
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	if !hasAuthKeyID {
		authKeyID = rawAuthKeyID
	}
	unlockCommit := r.lockAuthLayerCommit(rawAuthKeyID, authKeyID)
	defer unlockCommit()
	if msgID > 0 {
		currentLayer, currentMsgID, exists := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
		if !exists || currentLayer != layer || currentMsgID != msgID {
			// A greater msg_id won after this request was admitted or while it
			// waited for the auth-key commit stripe. The request remains valid,
			// but its mutable default/binder effects are stale.
			return
		}
	}
	r.clientInfoMu.Lock()
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
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
	publishDefault := r.authInfo[rawAuthKeyID].layerAdmissionSeq == 0
	if _, ordered := r.authLayerEvidence[rawAuthKeyID]; ordered {
		publishDefault = false
	}
	if hasAuthKeyID && r.authInfo[authKeyID].layerAdmissionSeq != 0 {
		publishDefault = false
	}
	if hasAuthKeyID {
		if _, ordered := r.authLayerEvidence[authKeyID]; ordered {
			publishDefault = false
		}
	}
	defaultChanged := false
	if publishDefault {
		defaultChanged = r.authClientLayerLocked(rawAuthKeyID) != layer
		r.rememberAuthClientLayerLocked(rawAuthKeyID, layer)
		if hasAuthKeyID {
			defaultChanged = defaultChanged || r.authClientLayerLocked(authKeyID) != layer
			r.rememberAuthClientLayerLocked(authKeyID, layer)
		}
	}
	r.clientInfoMu.Unlock()
	if notifyConn {
		if binder, ok := r.deps.Sessions.(ClientLayerBinder); ok {
			binder.SetClientLayerForAuthKey(rawAuthKeyID, sessionID, layer)
		}
	}
	// The newest explicit observation becomes the default for future sessions.
	// A repeat from an older live session may legitimately move that default
	// back without changing any sibling session's already-selected profile.
	if publishDefault && (notifyConn || defaultChanged) {
		r.persistAuthKeyClientInfo(ctx, clientSessionInfo{layer: layer})
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

// NegotiatedLayer is the legacy exact-session lookup. Auth-key-wide inheritance
// is intentionally exposed separately by ResolveInheritedAuthKeyLayer so the
// edge can retain the source distinction and let a later explicit selector
// correct an inherited Conn profile.
func (r *Router) NegotiatedLayer(authKeyID [8]byte, sessionID int64) (int, bool) {
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); ok {
		return layer, true
	}
	return currentClientLayer, false
}

// NegotiatedSessionLayer returns only an exact same-session observation. The
// profile registry is deliberately independent from transient client metadata:
// ordinary transport offline removes presence/device cache entries but must not
// erase the immutable profile needed by same-session naked RPC replay.
func (r *Router) NegotiatedSessionLayer(authKeyID [8]byte, sessionID int64) (int, bool) {
	layer, _, ok := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID)
	return layer, ok
}

// NegotiatedSessionLayerEvidence returns the latest explicit profile together
// with the MTProto message id that linearized it. msgID=0 denotes a legacy
// caller that had no wire-order evidence.
func (r *Router) NegotiatedSessionLayerEvidence(authKeyID [8]byte, sessionID int64) (layer int, msgID int64, ok bool) {
	if r == nil || authKeyID == ([8]byte{}) || sessionID == 0 {
		return 0, 0, false
	}
	key := clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID}
	now := r.clock.Now()
	r.exactProfileMu.RLock()
	entry, ok := r.exactProfiles[key]
	r.exactProfileMu.RUnlock()
	if !ok || entry.layer == 0 {
		return 0, 0, false
	}
	if now.Before(entry.expiresAt) {
		return entry.layer, entry.msgID, true
	}
	// Expiry is a state transition, not a read-time fallback. Delete only the
	// value observed above so a concurrent refresh cannot be lost.
	r.exactProfileMu.Lock()
	if current, exists := r.exactProfiles[key]; exists && current == entry && !now.Before(current.expiresAt) {
		delete(r.exactProfiles, key)
	}
	r.exactProfileMu.Unlock()
	return 0, 0, false
}

// FreezeNegotiatedSessionLayer atomically records the latest explicit profile
// proven for one MTProto (raw auth key, session) identity. A later valid
// invokeWithLayer is an ordered correction, not a connection conflict: replace
// the old value and refresh its retention. Request/result encoding remains
// bound to each admitted request's immutable profile outside this registry.
func (r *Router) FreezeNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64, layer int) error {
	_, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, layer, 0)
	return err
}

// FreezeNegotiatedSessionLayerAt linearizes explicit invokeWithLayer evidence
// by MTProto message id. Older evidence is ignored; the same message id is
// idempotent only for the same profile; a greater id may correct the session.
// msgID=0 is the legacy force API. Repeating the current layer through that API
// refreshes TTL without erasing a positive ordering watermark.
func (r *Router) FreezeNegotiatedSessionLayerAt(authKeyID [8]byte, sessionID int64, layer int, msgID int64) (bool, error) {
	if r == nil || authKeyID == ([8]byte{}) || sessionID == 0 {
		return false, errors.New("invalid exact session profile identity")
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok || int(profile) != layer {
		return false, fmt.Errorf("unsupported exact session profile %d", layer)
	}
	if msgID < 0 {
		return false, fmt.Errorf("invalid exact session profile message id %d", msgID)
	}
	unlockCommit := r.lockAuthLayerCommit(authKeyID)
	defer unlockCommit()
	now := r.clock.Now()
	expiresAt := exactSessionLayerEvidenceExpiry(now, msgID)
	key := clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID}
	r.exactProfileMu.Lock()
	defer r.exactProfileMu.Unlock()
	if r.exactProfiles == nil {
		r.exactProfiles = make(map[clientInfoSessionKey]exactSessionProfileEntry)
	}
	if current, exists := r.exactProfiles[key]; exists && now.Before(current.expiresAt) {
		if msgID == 0 {
			if current.layer == layer {
				current.expiresAt = expiresAt
				r.exactProfiles[key] = current
				r.noteExactSessionProfileExpiryLocked(current.expiresAt)
				return false, nil
			}
			current = exactSessionProfileEntry{layer: layer, expiresAt: expiresAt}
			r.exactProfiles[key] = current
			r.noteExactSessionProfileExpiryLocked(current.expiresAt)
			return true, nil
		}
		if current.msgID > msgID {
			return false, nil
		}
		if current.msgID == msgID {
			if current.layer != layer {
				return false, fmt.Errorf("exact session profile conflict at msg_id %d: current=%d requested=%d", msgID, current.layer, layer)
			}
			// Duplicate replay never extends the original selector's mutable
			// window; otherwise a client could retain exact state forever.
			return false, nil
		}
		current = exactSessionProfileEntry{layer: layer, msgID: msgID, expiresAt: expiresAt}
		r.exactProfiles[key] = current
		r.noteExactSessionProfileExpiryLocked(current.expiresAt)
		return true, nil
	}
	delete(r.exactProfiles, key)
	if len(r.exactProfiles) >= maxExactSessionProfileEntries {
		if r.exactProfileEarliestExpiry.IsZero() || !now.Before(r.exactProfileEarliestExpiry) {
			r.purgeExpiredExactSessionProfilesLocked(now)
		}
		if len(r.exactProfiles) >= maxExactSessionProfileEntries {
			return false, &ExactSessionProfileCapacityError{Limit: maxExactSessionProfileEntries}
		}
	}
	entry := exactSessionProfileEntry{layer: layer, msgID: msgID, expiresAt: expiresAt}
	r.exactProfiles[key] = entry
	if len(r.exactProfiles) == 1 {
		r.exactProfileEarliestExpiry = entry.expiresAt
	} else {
		r.noteExactSessionProfileExpiryLocked(entry.expiresAt)
	}
	return true, nil
}

func exactSessionLayerEvidenceExpiry(now time.Time, msgID int64) time.Time {
	if msgID <= 0 {
		return now.Add(exactSessionProfileLegacyTTL)
	}
	expiresAt := proto.MessageID(msgID).Time().Add(300*time.Second + time.Second)
	if !now.Before(expiresAt) {
		// Production freshness admission prevents this branch. Keep the force
		// API/test seam short-lived instead of manufacturing a long watermark
		// from an ancient or synthetic msg_id.
		return now.Add(time.Second)
	}
	return expiresAt
}

func (r *Router) purgeExpiredExactSessionProfilesLocked(now time.Time) {
	earliest := time.Time{}
	for key, entry := range r.exactProfiles {
		if !now.Before(entry.expiresAt) {
			delete(r.exactProfiles, key)
			continue
		}
		if earliest.IsZero() || entry.expiresAt.Before(earliest) {
			earliest = entry.expiresAt
		}
	}
	r.exactProfileEarliestExpiry = earliest
}

func (r *Router) noteExactSessionProfileExpiryLocked(expiry time.Time) {
	if r.exactProfileEarliestExpiry.IsZero() || expiry.Before(r.exactProfileEarliestExpiry) {
		r.exactProfileEarliestExpiry = expiry
	}
}

// ForgetNegotiatedSessionLayer is reserved for explicit destroy_session.
func (r *Router) ForgetNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64) {
	if r == nil {
		return
	}
	r.exactProfileMu.Lock()
	delete(r.exactProfiles, clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID})
	r.exactProfileMu.Unlock()
	r.clientInfoMu.Lock()
	delete(r.clientInfo, clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID})
	r.clientInfoMu.Unlock()
}

// ForgetNegotiatedAuthKey is reserved for physical auth-key destruction. A
// business authorization logout/revoke must not call it: Layer is protocol
// metadata and survives authorization state. This is intentionally cold
// O(capacity); hot lookup and refresh stay O(1).
func (r *Router) ForgetNegotiatedAuthKey(authKeyID [8]byte) {
	if r == nil || authKeyID == ([8]byte{}) {
		return
	}
	r.exactProfileMu.Lock()
	for key := range r.exactProfiles {
		if key.rawAuthKeyID == authKeyID {
			delete(r.exactProfiles, key)
		}
	}
	r.exactProfileMu.Unlock()
	r.clientInfoMu.Lock()
	delete(r.authInfo, authKeyID)
	delete(r.authLayerEvidence, authKeyID)
	for key := range r.clientInfo {
		if key.rawAuthKeyID == authKeyID {
			delete(r.clientInfo, key)
		}
	}
	r.clientInfoMu.Unlock()
}

// SessionDestroyed is the explicit lifecycle callback. SessionOffline must not
// call this: a physical TCP loss is not destruction of the logical MTProto
// session.
func (r *Router) SessionDestroyed(rawAuthKeyID [8]byte, sessionID int64) {
	r.ForgetNegotiatedSessionLayer(rawAuthKeyID, sessionID)
}

// ObserveInitConnection records the protocol metadata of an initConnection
// whose inner request was aliased by mtprotoedge to an already-running naked
// request. It deliberately performs no handler dispatch and therefore cannot
// repeat business side effects.
func (r *Router) ObserveInitConnection(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	layer, apiID int,
	deviceModel, systemVersion, appVersion, systemLangCode, langPack, langCode string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = WithRawAuthKeyID(ctx, rawAuthKeyID)
	effectiveAuthKeyID, err := r.effectiveAuthKeyID(ctx, rawAuthKeyID, sessionID)
	if err != nil {
		return err
	}
	ctx = WithAuthKeyID(ctx, effectiveAuthKeyID)
	ctx = WithSessionID(ctx, sessionID)
	ctx = WithLayer(ctx, layer)
	// Exact Layer/default publication belongs to generated admission and has
	// already happened before a rewrap alias can reach this legacy metadata
	// observer. Replaying it here would have no msg_id/admission sequence and
	// could reset a newer exact profile.
	r.rememberClientInfo(ctx, ClientInfo{
		APIID: apiID, DeviceModel: deviceModel, SystemVersion: systemVersion,
		AppVersion: appVersion, SystemLangCode: systemLangCode,
		LangPack: langPack, LangCode: langCode,
	})
	return nil
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
	authInfo := sessionInfo
	authInfo.layer = 0
	authInfo.layerAdmissionSeq = 0
	authInfo.clientInfoAdmissionSeq = 0
	r.rememberAuthClientInfoLocked(rawAuthKeyID, authInfo)
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		r.rememberAuthClientInfoLocked(authKeyID, authInfo)
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
	// authInfo is deliberately auth-key-wide and can only cache the client
	// description/check markers. Exact-session Layer is not a required fact in
	// either auth-key fallback entry.
	authRequired := required
	authRequired.layer = 0
	if !clientSessionInfoContains(r.authInfo[rawAuthKeyID], authRequired) {
		return false
	}
	if hasAuthKeyID && !clientSessionInfoContains(r.authInfo[authKeyID], authRequired) {
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
	if authKeyID == ([8]byte{}) {
		return
	}
	// Wrapper ordering is a logical-session fact and must never leak into an
	// auth-key-wide metadata/default entry. Ordered Layer publication uses
	// rememberAuthClientLayerAtLocked directly.
	info.wrapperMsgID = 0
	info.layerAdmissionSeq = 0
	info.clientInfoAdmissionSeq = 0
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
	r.rememberAuthClientLayerAtLocked(authKeyID, layer, 0)
}

func (r *Router) rememberAuthClientLayerAtLocked(authKeyID [8]byte, layer int, admissionSeq uint64) {
	if authKeyID == ([8]byte{}) || !isSupportedLayer(layer) {
		return
	}
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	if _, exists := r.authInfo[authKeyID]; !exists {
		evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
	}
	info := r.authInfo[authKeyID]
	if admissionSeq == 0 && info.layerObservationID > 0 {
		// An unordered restoration/shadow write can never replace a durable
		// observation. In particular, do not manufacture (new observation,
		// old layer) by changing only the layer field.
		return
	}
	if admissionSeq == 0 && info.layerAdmissionSeq > 0 {
		// Store restoration and legacy callers carry no cross-session order and
		// must not overwrite a default established by a fresh admission.
		return
	}
	if admissionSeq > 0 && info.layerAdmissionSeq > admissionSeq {
		return
	}
	info.layer = layer
	info.layerBlocked = false
	info.layerBlockedByAuthKey = false
	if admissionSeq > 0 {
		info.layerAdmissionSeq = admissionSeq
	}
	r.authInfo[authKeyID] = info
}

func (r *Router) rememberAuthClientLayerObservationLocked(authKeyID [8]byte, layer int, observationID int64) {
	if authKeyID == ([8]byte{}) || !isSupportedLayer(layer) || observationID <= 0 {
		return
	}
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	if _, exists := r.authInfo[authKeyID]; !exists {
		evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
	}
	info := r.authInfo[authKeyID]
	if info.layerObservationID > observationID {
		return
	}
	if info.layerObservationID == observationID && info.layerObservationID > 0 && info.layer != 0 && info.layer != layer {
		return
	}
	info.layer = layer
	info.layerObservationID = observationID
	// Durable database order supersedes any process-local admission order for
	// the shared default. Exact request/session ordering remains in its own
	// msg_id/flight structures.
	info.layerAdmissionSeq = 0
	info.layerBlocked = false
	info.layerBlockedByAuthKey = false
	r.authInfo[authKeyID] = info
}

func (r *Router) authClientLayerLocked(authKeyID [8]byte) int {
	return r.authInfo[authKeyID].layer
}

// forgetClientSessionInfo removes only transport/session-local metadata. The
// auth-key default is durable protocol state and stays cached across ordinary
// disconnects; the bounded authInfo map handles eviction.
func (r *Router) forgetClientSessionInfo(rawAuthKeyID [8]byte, sessionID int64) {
	r.clientInfoMu.Lock()
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	if current := r.clientInfo[key]; current.wrapperMsgID > 0 || current.layerAdmissionSeq > 0 {
		// Preserve only the bounded ordering watermark across a physical
		// reconnect; metadata is restored from the auth-key default/store.
		r.clientInfo[key] = clientSessionInfo{
			layerAdmissionSeq: current.layerAdmissionSeq,
			wrapperMsgID:      current.wrapperMsgID,
		}
	} else {
		delete(r.clientInfo, key)
	}
	r.clientInfoMu.Unlock()
}

func (r *Router) claimSessionWrapperEffects(rawAuthKeyID [8]byte, sessionID, msgID int64) bool {
	if msgID <= 0 {
		return true
	}
	if rawAuthKeyID == ([8]byte{}) || sessionID == 0 {
		return false
	}
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	r.clientInfoMu.Lock()
	defer r.clientInfoMu.Unlock()
	if r.clientInfo == nil {
		r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
	}
	current, exists := r.clientInfo[key]
	if current.wrapperMsgID > msgID {
		return false
	}
	if current.wrapperMsgID == msgID {
		return true
	}
	if !exists {
		evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
	}
	current.wrapperMsgID = msgID
	r.clientInfo[key] = current
	return true
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
	// For a bound temporary key, the permanent/business auth key is the
	// canonical shared default. The raw shadow is only a pre-bind/restart aid.
	if hasAuthKeyID {
		if authInfo, authOK := r.authInfo[authKeyID]; authOK {
			info = mergeClientSessionInfo(info, authInfo)
			ok = true
		}
	}
	if authInfo, authOK := r.authInfo[rawAuthKeyID]; authOK {
		info = mergeClientSessionInfo(info, authInfo)
		ok = true
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
		return clientSessionInfoFromAuthKeyClientInfo(info), nil
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
	switch {
	case fallback.layerObservationID > base.layerObservationID:
		base.layer = fallback.layer
		base.layerObservationID = fallback.layerObservationID
		base.layerBlocked = fallback.layerBlocked
		base.layerBlockedByAuthKey = fallback.layerBlockedByAuthKey
	case fallback.layerObservationID == base.layerObservationID && base.layer == 0:
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
	if fallback.layerBlocked && base.layer == 0 {
		base.layerBlocked = true
	}
	if fallback.layerBlockedByAuthKey && base.layer == 0 {
		base.layerBlockedByAuthKey = true
	}
	if base.layer != 0 {
		base.layerBlocked = false
		base.layerBlockedByAuthKey = false
	}
	if fallback.wrapperMsgID > base.wrapperMsgID {
		base.wrapperMsgID = fallback.wrapperMsgID
	}
	return base
}

func clientSessionInfoFromAuthKeyClientInfo(item domain.AuthKeyClientInfo) clientSessionInfo {
	info := clientSessionInfo{
		authKeyInfoChecked: true,
		layerObservationID: item.LayerObservationID,
		clientInfo: ClientInfo{
			APIID:         item.APIID,
			DeviceModel:   item.DeviceModel,
			SystemVersion: item.SystemVersion,
			AppVersion:    item.AppVersion,
			Type:          ClientType(item.Platform),
		},
	}
	if isSupportedLayer(item.Layer) {
		info.layer = item.Layer
	} else if item.Layer != 0 {
		// A non-zero auth_keys.layer is primary even when this binary has not
		// generated it. Mark the mirror checked so an older authorization.layer
		// cannot silently downgrade the future value.
		info.authorizationChecked = true
		info.layerBlocked = true
		info.layerBlockedByAuthKey = true
	}
	info.clientInfo = restoreClientInfo(info.clientInfo)
	info.hasClientInfo = info.clientInfo.ClientType() != ClientTypeUnknown ||
		info.clientInfo.DeviceModel != "" ||
		info.clientInfo.SystemVersion != "" ||
		info.clientInfo.AppVersion != "" ||
		info.clientInfo.APIID != 0
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
		return clientSessionInfoFromAuthorizationRecord(item), nil
	})
	if err != nil {
		return clientSessionInfo{}, false
	}
	return v.(clientSessionInfo), true
}

func clientSessionInfoFromAuthorizationRecord(item domain.Authorization) clientSessionInfo {
	info := clientSessionInfo{
		authorizationChecked: true,
		clientInfo: ClientInfo{
			APIID:         item.APIID,
			DeviceModel:   item.DeviceModel,
			SystemVersion: item.SystemVersion,
			AppVersion:    item.AppVersion,
			Type:          ClientType(item.Platform),
		},
	}
	// authorizations.layer is only a materialized device-list projection.
	// Protocol inheritance is exclusively sourced from auth_keys.layer with its
	// observation watermark; promoting this mirror would fabricate provenance
	// when the primary is zero or has been repaired independently.
	info.clientInfo = restoreClientInfo(info.clientInfo)
	info.hasClientInfo = info.clientInfo.ClientType() != ClientTypeUnknown ||
		info.clientInfo.DeviceModel != "" ||
		info.clientInfo.SystemVersion != "" ||
		info.clientInfo.AppVersion != "" ||
		info.clientInfo.APIID != 0
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
