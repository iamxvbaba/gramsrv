package rpc

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// channel fan-out 异步化（设计 docs/channel-fanout-async-design.md Phase 0 / §9 v1 最小实现）。
//
// 目标：把频道 payload fan-out 移出发送者 RPC 同步路径——发送者只等事务 + 自己视角 echo
// （rpc_result），其余在线成员的投递交给本 dispatcher 的后台 worker。
//
// v1（单实例）关键取舍：
//   - durable 真值仍是 channel_update_events；本 dispatcher 只做 best-effort 在线加速，
//     丢失由客户端 getChannelDifference 兜底（设计决策 1）。
//   - 按 channelID 哈希到固定分片，使同一 channel 串行处理（FIFO → 单调 pts），满足
//     DrKLO Android ~1.5s 乱序窗口与 TDesktop PtsWaiter 的连续性期望（设计 §10.2）。
//   - 单实例 + 无 durable 重投队列 + 同 channel 串行 → 无乱序、无自重复，故 v1 不需要
//     per-session at-most-once 双水位（那是 Phase 3 跨实例的事，设计 §9/§10.1）。
//   - 有界队列满时丢弃完整 payload，但立即给在线成员发 UpdateChannelTooLong{pts} nudge，
//     促使客户端 getChannelDifference 收敛，避免必须等该 channel 下一条消息。

const (
	defaultChannelFanoutShards = 64
	defaultChannelFanoutBuffer = 2048
)

// channelFanoutBuilder 按 viewer 构建该 viewer 视角的 channel updates。与同步
// channelUpdatesBuilder 不同，它接受 worker 提供的后台 ctx（请求 ctx 在 fan-out 异步
// 执行时已被取消），且实现不得从 ctx 读取 viewer/auth 派生数据（viewerUserID 显式传入）。
type channelFanoutBuilder func(ctx context.Context, viewerUserID int64) *tg.Updates

// channelFanoutJob 是一条频道 payload fan-out 任务。Pts 仅用于日志/折叠语义；真值仍是
// channel_update_events，worker 只做在线投递。originAuthKeyID 是业务视角 auth key
// （与 SessionManager.shouldExcludeSession 的比较侧一致），用于显式排除发起设备——异步
// 执行时请求 ctx 已失效，不能再靠 ctx 派生排除。
type channelFanoutJob struct {
	scope           channelFanoutScope
	originUserID    int64
	channelID       int64
	pts             int
	recipients      []int64
	originAuthKeyID [8]byte
	originSessionID int64
	prefetch        channelFanoutPrefetch
	build           channelFanoutBuilder
}

// channelFanoutPrefetch 在 worker 解析出最终 recipient 集合后、逐 viewer build 之前调用一次，
// 用于跨全部 recipient 一次性预热每 viewer 的用户投影（fan-out 模板化，O(owner)）。可选：为 nil
// 时 build 仍逐 viewer 解析（行为不变）。在 worker goroutine 内串行执行，与 build 共享同一
// viewerPeerCache，无跨 goroutine 竞态。
type channelFanoutPrefetch func(ctx context.Context, viewers []int64)

// channelFanoutDispatcher 把频道 payload fan-out 移出发送者 RPC，按 channelID 分片串行处理。
type channelFanoutDispatcher struct {
	r       *Router
	log     *zap.Logger
	shards  []chan channelFanoutJob
	started atomic.Bool
	dropped atomic.Int64
}

// enqueueChannelFanout 把一条 channel-payload-pts 的 fan-out 投入异步 dispatcher。
// 从请求 ctx 抓取发起设备的业务 auth key + session_id 显式带入 job，使异步 worker 仍能
// 排除发起设备回显（请求 ctx 异步时已失效）。仅用于会推进客户端 channel PtsWaiter 的真实
// payload（新消息/编辑/删除/pin）；reaction/poll（viewer-only 零 pts）、participant/TTL/
// channel state（无 channel pts）、typing（transient）不走此路径（设计 §2.1/§5 分类）。
func (r *Router) enqueueChannelFanout(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, pts int, recipients []int64, build channelFanoutBuilder) {
	r.enqueueChannelFanoutWithPrefetch(ctx, scope, originUserID, channelID, pts, recipients, nil, build)
}

// enqueueChannelFanoutWithPrefetch 同 enqueueChannelFanout，但额外带一个跨 viewer 用户投影预热钩子
// （fan-out 模板化把每 recipient 的逐 viewer 投影折叠成一次 O(owner) 投影；见 prefetchChannelFanoutUsers）。
func (r *Router) enqueueChannelFanoutWithPrefetch(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, pts int, recipients []int64, prefetch channelFanoutPrefetch, build channelFanoutBuilder) {
	if r.channelFanout == nil || build == nil {
		return
	}
	originAuthKeyID, _ := AuthKeyIDFrom(ctx)
	originSessionID, _ := SessionIDFrom(ctx)
	r.channelFanout.Enqueue(ctx, channelFanoutJob{
		scope:           scope,
		originUserID:    originUserID,
		channelID:       channelID,
		pts:             pts,
		recipients:      recipients,
		originAuthKeyID: originAuthKeyID,
		originSessionID: originSessionID,
		prefetch:        prefetch,
		build:           build,
	})
}

// RunChannelFanout 启动频道 fan-out 后台 worker，由 main 与其它 dispatcher 一同 go 起。
// 阻塞到 ctx 取消；未调用前 fan-out 同步执行（行为同旧版）。
func (r *Router) RunChannelFanout(ctx context.Context) {
	r.channelFanout.Run(ctx)
}

func newChannelFanoutDispatcher(r *Router, shards, buffer int) *channelFanoutDispatcher {
	if shards <= 0 {
		shards = defaultChannelFanoutShards
	}
	if buffer <= 0 {
		buffer = defaultChannelFanoutBuffer
	}
	d := &channelFanoutDispatcher{r: r, log: r.log.Named("channel-fanout"), shards: make([]chan channelFanoutJob, shards)}
	for i := range d.shards {
		d.shards[i] = make(chan channelFanoutJob, buffer)
	}
	return d
}

// Run 启动 worker：每分片一个 goroutine，保证同 channel 串行。阻塞到 ctx 取消。
// 未调用 Run 前 Enqueue 回退为同步执行（保持测试/未装配场景行为不变）。
func (d *channelFanoutDispatcher) Run(ctx context.Context) {
	if d == nil || !d.started.CompareAndSwap(false, true) {
		return
	}
	var wg sync.WaitGroup
	for i := range d.shards {
		wg.Add(1)
		ch := d.shards[i]
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-ch:
					d.r.runChannelFanoutJob(ctx, job)
				}
			}
		}()
	}
	wg.Wait()
}

func (d *channelFanoutDispatcher) shardIndex(channelID int64) int {
	n := int64(len(d.shards))
	idx := channelID % n
	if idx < 0 {
		idx += n
	}
	return int(idx)
}

// Enqueue 投递一条 fan-out 任务。dispatcher 未启动时同步执行（用请求 ctx，保持旧行为）；
// 已启动时投入对应分片，满则丢弃完整 payload + 告警，并对真实 channel pts payload 发
// UpdateChannelTooLong nudge 触发客户端补差异。
func (d *channelFanoutDispatcher) Enqueue(reqCtx context.Context, job channelFanoutJob) {
	if d == nil || job.build == nil {
		return
	}
	if !d.started.Load() {
		d.r.runChannelFanoutJob(reqCtx, job)
		return
	}
	shard := d.shards[d.shardIndex(job.channelID)]
	select {
	case shard <- job:
	default:
		d.dropped.Add(1)
		d.log.Warn("channel fanout queue full, dropped full realtime payload and attempted difference nudge",
			zap.Int64("channel_id", job.channelID), zap.Int("pts", job.pts))
		d.nudgeDroppedFanout(reqCtx, job)
	}
}

func (d *channelFanoutDispatcher) nudgeDroppedFanout(reqCtx context.Context, job channelFanoutJob) {
	if d == nil || d.r == nil || job.scope != channelFanoutMembers || job.channelID == 0 || job.pts <= 0 {
		return
	}
	ctx := context.Background()
	if reqCtx != nil {
		ctx = context.WithoutCancel(reqCtx)
	}
	pushCtx := WithSessionID(WithAuthKeyID(ctx, job.originAuthKeyID), job.originSessionID)
	d.r.nudgeBeyondCapChannelMembers(pushCtx, job.channelID, job.pts, nil)
}

// runChannelFanoutJob 执行一条 fan-out：与同步 pushChannelUpdatesWithScope 等价，区别是
// build 接受 ctx、且排除发起设备用 job 显式携带的 (originAuthKeyID, originSessionID)
// 叠加到 ctx 后复用 pushUserUpdates，而非依赖已失效的请求 ctx。
func (r *Router) runChannelFanoutJob(ctx context.Context, job channelFanoutJob) {
	if r.deps.Sessions == nil || job.build == nil {
		return
	}
	pushCtx := WithSessionID(WithAuthKeyID(ctx, job.originAuthKeyID), job.originSessionID)
	recipients := r.channelFanoutRecipients(ctx, job.scope, job.channelID, job.recipients)
	// 预热跨 viewer 用户投影（fan-out 模板化）：在逐 viewer build 之前一次性算好每 recipient 的
	// 投影并预热共享 cache，使 build 只命中缓存、不再 O(viewer) 逐个 ForViewer。覆盖 recipients +
	// 兜底 origin（无在线 recipient 时 build 会回退给 origin）。失败/未实现时静默退化为逐 viewer。
	if job.prefetch != nil {
		viewers := recipients
		if job.originUserID != 0 {
			viewers = append(append(make([]int64, 0, len(recipients)+1), recipients...), job.originUserID)
		}
		job.prefetch(ctx, viewers)
	}
	seen := make(map[int64]struct{}, len(recipients))
	pushed := false
	for _, userID := range recipients {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		updates := job.build(ctx, userID)
		if updates == nil {
			continue
		}
		r.pushUserUpdates(pushCtx, userID, updates)
		pushed = true
	}
	if !pushed && job.originUserID != 0 {
		seen[job.originUserID] = struct{}{}
		if updates := job.build(ctx, job.originUserID); updates != nil {
			r.pushUserUpdates(pushCtx, job.originUserID, updates)
		}
	}
	// P0-8：完整 payload 受 MaxChannelRealtimeFanout 封顶，超出 cap 的在线成员既收不到
	// payload 也收不到任何东西（频道纯拉模型不会自发轮询）。给这些「已在线但未收完整 payload」
	// 的成员发廉价 UpdateChannelTooLong{pts} nudge，促其 getChannelDifference 收敛。
	// 仅对会推进客户端 channel PtsWaiter 的真实 payload（members scope + 带 channel pts）做。
	if job.scope == channelFanoutMembers && job.pts > 0 {
		r.nudgeBeyondCapChannelMembers(pushCtx, job.channelID, job.pts, seen)
	}
}

// prefetchChannelFanoutUsers 跨全部 recipient 一次性投影 owner 用户（fan-out 模板化，O(owner)），
// 把结果按 viewer 预热进共享 cache；之后每 viewer 的 build 只命中缓存，不再逐 viewer ForViewer。
// ownerIDs 由调用方从消息/事件 peer refs 收集。deps.Users 未实现 BatchViewerUsersResolver 或解析
// 失败时静默跳过——build 回退逐 viewer 解析，行为不变，仅退化为旧的 O(viewer) 成本。
func (r *Router) prefetchChannelFanoutUsers(ctx context.Context, cache *viewerPeerCache, viewers, ownerIDs []int64) {
	if cache == nil || len(viewers) == 0 || len(ownerIDs) == 0 || r.deps.Users == nil {
		return
	}
	resolver, ok := r.deps.Users.(BatchViewerUsersResolver)
	if !ok {
		return
	}
	byViewer, err := resolver.ByIDsForViewers(ctx, viewers, ownerIDs)
	if err != nil {
		r.log.Warn("channel fanout user prefetch failed; falling back to per-viewer projection",
			zap.Int("viewers", len(viewers)), zap.Int("owners", len(ownerIDs)), zap.Error(err))
		return
	}
	for viewer, users := range byViewer {
		cache.primeUsers(viewer, users)
	}
}

// channelMessageFanoutOwnerIDs 收集一条频道消息 fan-out 会下发到 Users 数组里的全部 owner 用户 id
// （sender/from/send_as/forward/via_bot/reply/contact/poll 等 peer refs，与
// channelMessagesUpdatesWithPeerCache 的收集口径一致），用于预热跨 viewer 投影。
func channelMessageFanoutOwnerIDs(res domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
	return channelMessagesFanoutOwnerIDs([]domain.SendChannelMessageResult{res}, extraUserIDs)
}

// channelMessagesFanoutOwnerIDs 同上，但取多条结果（批量转发汇成一个 job）的 owner id 并集。
func channelMessagesFanoutOwnerIDs(results []domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
	userIDs := make(map[int64]struct{}, len(results)+len(extraUserIDs)+4)
	channelIDs := make(map[int64]struct{})
	for _, id := range extraUserIDs {
		if id != 0 {
			userIDs[id] = struct{}{}
		}
	}
	for _, res := range results {
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	}
	return peerIDMapKeys(userIDs)
}

// enqueueChannelMessageFanout 异步 fan-out 单条频道消息并预热跨 viewer 投影（「频道里出现一条新消息」
// 类事件的常见形态：发送/转发单条/讨论组联动/forum topic 消息）。语义与 enqueueChannelFanout 一致，
// 仅多了把每 viewer 投影一次性算好预热进共享 cache（O(owner)），不改变投递/排除/nudge 行为。
func (r *Router) enqueueChannelMessageFanout(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult, extraUserIDs []int64) {
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(res, extraUserIDs)
	skip := skipDeliverySet(res.SkipDeliveryUserIDs)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, res.Channel.ID, res.Event.Pts, res.Recipients,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			// privacy bot 在 send 时被 SkipDeliveryUserIDs 排除（命令/@/回复以外的消息不可见）。
			// channelFanoutRecipients 按「在线活跃成员」重算 recipients 会把它加回，故在线 fanout
			// 必须在此跳过它的直接推送，否则在线 bot 仍能实时收到群里全部消息（持久 history/
			// difference 已正确隐藏，仅此直接推送泄漏内容）。nudge 安全：bot 落在 seen 里不被 nudge，
			// 即便 nudge 也会被 filterBotChannelDifference 过滤掉隐藏消息。
			if _, skipped := skip[viewerUserID]; skipped {
				return nil
			}
			return r.channelMessageUpdatesWithPeerCache(bgCtx, viewerUserID, res, 0, fanoutCache)
		})
}

// skipDeliverySet 把 SkipDeliveryUserIDs 切片转成查找集合（nil 表示无排除）。
func skipDeliverySet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			set[id] = struct{}{}
		}
	}
	return set
}

// channelEditMessageFanoutOwnerIDs 收集一条频道编辑 fan-out 会下发到 Users 数组里的全部 owner 用户 id。
// 严格镜像 channelEditMessageUpdates 的两容器 + pts 门控收集口径（Event/Message 仅 Event.Pts!=0 时收，
// ServiceEvent/ServiceMessage 仅 ServiceEvent.Pts!=0 时收，对应 todo 编辑的服务消息第二容器），使预热
// owner 集与 build 实际下发的 Users 集恰好一致——多收只会无害多预热，但镜像门控让等价测试最紧。
func channelEditMessageFanoutOwnerIDs(res domain.EditChannelMessageResult) []int64 {
	userIDs := make(map[int64]struct{}, 4)
	channelIDs := make(map[int64]struct{})
	if res.Event.Pts != 0 {
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	}
	if res.ServiceEvent.Pts != 0 {
		collectChannelUpdatePeerRefs(res.ServiceEvent, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.ServiceMessage, res.Channel.ID, userIDs, channelIDs)
	}
	return peerIDMapKeys(userIDs)
}

// enqueueChannelEditMessageFanout 异步 fan-out 一条频道编辑并预热跨 viewer 投影（editMessage/geolive/
// todo/bot-inline edit 的共同形态）。语义与原 enqueueChannelFanout(channelEditMessageUpdates) 一致，
// 仅多了把每 viewer 投影一次性算好预热进共享 cache（O(owner)）。注意 edit 不做 per-viewer mention
// overlay（EditChannelMessageResult 不带 MentionUserIDs，编辑新增 @ 走 durable channel_unread_mentions
// 经 getChannelDifference 自愈），故各 viewer 的 mentioned/media_unread 字节恒等，预热不影响等价。
//
// nudge pts 取两容器较大值：edit 可产两条带 pts 事件（Event=编辑本身、ServiceEvent=如 todo 完成的
// "X completed Y" 服务消息），ServiceEvent.Pts 在后分配恒更大；某些编辑只产 ServiceEvent（Event.Pts==0）。
// nudge 须带 channel 当前最高 pts 才能让 >cap 在线成员的 getChannelDifference 拉齐到末尾——用 Event.Pts
// 会在 Event.Pts==0 时漏发 nudge、或低于真实 pts。max() 在三种形态（仅 Event/仅 ServiceEvent/两者）都正确。
func (r *Router) enqueueChannelEditMessageFanout(ctx context.Context, originUserID int64, res domain.EditChannelMessageResult) {
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelEditMessageFanoutOwnerIDs(res)
	nudgePts := max(res.Event.Pts, res.ServiceEvent.Pts)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, res.Channel.ID, nudgePts, res.Recipients,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelEditMessageUpdatesWithPeerCache(bgCtx, viewerUserID, res, fanoutCache)
		})
}

// enqueueChannelMessagesFanout 同 enqueueChannelMessageFanout，但把多条结果汇成一个 job（批量转发：
// 一个 Updates 内含多条 UpdateNewChannelMessage），peer refs 取全部结果并集预热。channelID/pts/
// recipients 由调用方按批量语义给定（pts 取最后一条；recipients 受大群截断口径影响）。
func (r *Router) enqueueChannelMessagesFanout(ctx context.Context, originUserID, channelID int64, pts int, recipients []int64, results []domain.SendChannelMessageResult, extraUserIDs []int64) {
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessagesFanoutOwnerIDs(results, extraUserIDs)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, channelID, pts, recipients,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelMessagesUpdatesWithPeerCache(bgCtx, viewerUserID, results, nil, false, extraUserIDs, fanoutCache)
		})
}

// defaultChannelNudgeMaxTargets 限一次 fan-out 的 nudge 上限：nudge 是 O(1)/人廉价 push，但 nudge 被
// 消费后客户端会 getChannelDifference（DrKLO 对未加载频道还会先 getPeerDialogs，设计 §10.3），
// 大群高频下可能放大。可经 Config.ChannelNudgeMaxTargets 覆盖；客户端侧由 difference/getPeerDialogs
// 的 FLOOD_WAIT（Phase 2）兜底（见 checkCatchupRateLimit）。
const defaultChannelNudgeMaxTargets = 50000

// channelNudgeMaxTargets 返回生效的 nudge 上限（Config 覆盖，否则默认）。
func (r *Router) channelNudgeMaxTargets() int {
	if r.cfg.ChannelNudgeMaxTargets > 0 {
		return r.cfg.ChannelNudgeMaxTargets
	}
	return defaultChannelNudgeMaxTargets
}

// nudgeBeyondCapChannelMembers 给频道在线成员中未收到完整 payload（不在 delivered 内）的成员发
// UpdateChannelTooLong{pts}。nudge 必须带 pts（flags&1）——DrKLO 对不带 pts 的 tooLong 不触发
// getChannelDifference（设计 §10.3）。走 pushUserUpdates（best-effort、未就绪入 pending、非
// transient），符合设计 §决策4 的 nudge 投递可靠性要求。SessionManager 未实现 ChannelNudgeProvider
// 时（测试/未装配）静默跳过，不影响完整 payload 投递。
func (r *Router) nudgeBeyondCapChannelMembers(ctx context.Context, channelID int64, pts int, delivered map[int64]struct{}) {
	provider, ok := r.deps.Sessions.(ChannelNudgeProvider)
	if !ok || channelID == 0 || pts <= 0 {
		return
	}
	targets := provider.OnlineChannelMemberUserIDsExcluding(channelID, delivered, r.channelNudgeMaxTargets())
	if len(targets) == 0 {
		return
	}
	date := int(r.clock.Now().Unix())
	for _, userID := range targets {
		if userID == 0 {
			continue
		}
		tooLong := &tg.UpdateChannelTooLong{ChannelID: channelID}
		tooLong.SetPts(pts)
		r.pushUserUpdates(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{tooLong},
			Users:   []tg.UserClass{},
			Chats:   []tg.ChatClass{},
			Date:    date,
			Seq:     0,
		})
	}
}
