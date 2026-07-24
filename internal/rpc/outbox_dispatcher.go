package rpc

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	defaultOutboxBatch    = 100
	defaultOutboxInterval = 200 * time.Millisecond
	defaultOutboxWorkers  = 4
	// outboxLogicalShards 是稳定 user→lane 哈希空间。它不随运行时 worker 数变化，
	// worker 只独占其中一组 shard，保证同一用户始终单 lane 串行、不同用户可并行。
	outboxLogicalShards = store.DispatchOutboxLogicalShards
	// defaultOutboxMaxIdleInterval 是空闲退避上界：连续空 claim 时把轮询间隔从 interval
	// 指数退避到此上界，削减「无消息时也每 200ms 查一次 DB」的空转；一旦有活就立刻复位到
	// interval。代价是「长时间静默后的第一条消息」最多多等这个上界——退避只在持续空闲时触及，
	// 会话期间有近期活动即保持快速轮询，故对正常收发延迟无影响。
	defaultOutboxMaxIdleInterval = 1 * time.Second
)

var (
	errMissingOutboxEvent         = errors.New("missing outbox update event")
	errInvalidOutboxExclusionPair = errors.New("outbox exclusion requires both raw auth key and session id")
)

// OutboxDispatcher 把 PG transactional outbox 中的 update 批量推给在线 session。
// 多 worker 并发 claim：ClaimPending 用 FOR UPDATE SKIP LOCKED，worker 间认领不重叠。
type OutboxDispatcher struct {
	events          store.UpdateEventStore
	outbox          store.DispatchOutboxStore
	sessions        SessionBinder
	log             *zap.Logger
	metrics         Metrics
	updateBuilder   OutboxUpdateBuilder
	batch           int
	interval        time.Duration
	maxIdleInterval time.Duration
	workers         int
	pushTimeout     time.Duration
}

// OutboxOption 调整 OutboxDispatcher 的运行参数。
type OutboxOption func(*OutboxDispatcher)

// OutboxUpdateRequest 是 outbox worker 构造在线 updates 时的批量输入。
type OutboxUpdateRequest struct {
	TargetUserID int64
	Event        domain.UpdateEvent
}

// OutboxUpdateBuilder 按接收者视角批量把 domain.UpdateEvent 转为 TL updates。
type OutboxUpdateBuilder func(ctx context.Context, requests []OutboxUpdateRequest) []*tg.Updates

// WithOutboxUpdateBuilder 注入按接收者视角的批量 updates 构建器。
func WithOutboxUpdateBuilder(builder OutboxUpdateBuilder) OutboxOption {
	return func(d *OutboxDispatcher) {
		d.updateBuilder = builder
	}
}

// WithOutboxBatch 设置每次 claim 的最大条数；<=0 时保持默认。
func WithOutboxBatch(n int) OutboxOption {
	return func(d *OutboxDispatcher) {
		if n > 0 {
			d.batch = n
		}
	}
}

// WithOutboxInterval 设置两次 claim 之间的轮询间隔；<=0 时保持默认。
func WithOutboxInterval(interval time.Duration) OutboxOption {
	return func(d *OutboxDispatcher) {
		if interval > 0 {
			d.interval = interval
		}
	}
}

// WithOutboxWorkers 设置并发 claim worker 数；<=0 时保持默认。
func WithOutboxWorkers(n int) OutboxOption {
	return func(d *OutboxDispatcher) {
		if n > 0 {
			d.workers = n
		}
	}
}

// WithOutboxPushTimeout 设置 updates fanout 入队等待时间；<=0 时使用同步可靠推送。
func WithOutboxPushTimeout(timeout time.Duration) OutboxOption {
	return func(d *OutboxDispatcher) {
		if timeout > 0 {
			d.pushTimeout = timeout
		}
	}
}

// WithOutboxMetrics 注入指标实现；nil 时保持 NopMetrics。
func WithOutboxMetrics(m Metrics) OutboxOption {
	return func(d *OutboxDispatcher) {
		if m != nil {
			d.metrics = m
		}
	}
}

// NewOutboxDispatcher 创建在线 update 推送 worker。batch/interval 默认值见
// defaultOutboxBatch/defaultOutboxInterval，可经 WithOutbox* 选项覆盖（生产由 config 注入）。
func NewOutboxDispatcher(events store.UpdateEventStore, outbox store.DispatchOutboxStore, sessions SessionBinder, log *zap.Logger, opts ...OutboxOption) *OutboxDispatcher {
	if log == nil {
		log = zap.NewNop()
	}
	d := &OutboxDispatcher{
		events:          events,
		outbox:          outbox,
		sessions:        sessions,
		log:             log,
		metrics:         NopMetrics{},
		batch:           defaultOutboxBatch,
		interval:        defaultOutboxInterval,
		maxIdleInterval: defaultOutboxMaxIdleInterval,
		workers:         defaultOutboxWorkers,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

// Run 启动 workers 个并发 worker 持续 claim pending outbox；ctx 退出时全部停止并等待退出。
func (d *OutboxDispatcher) Run(ctx context.Context) {
	if d == nil || d.events == nil || d.outbox == nil || d.sessions == nil {
		return
	}
	claimer, sharded := d.outbox.(shardedOutboxClaimer)
	workers := normalizedOutboxWorkers(d.workers, sharded)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		var shardIDs []int
		if sharded {
			shardIDs = logicalShardsForWorker(i, workers)
		}
		go func() {
			defer wg.Done()
			d.runWorker(ctx, claimer, shardIDs)
		}()
	}
	wg.Wait()
}

func normalizedOutboxWorkers(workers int, sharded bool) int {
	if workers < 1 {
		workers = 1
	}
	if !sharded {
		// 测试替身或旧 store 没有 user shard claim 时，强制单 worker，避免同一用户
		// 的不同 pts 被并行领取。
		return 1
	}
	// 多于 logical shard 的 worker 没有独占 lane。若让空 shard worker 回退全局
	// claim，会与全部分片 worker 重叠，因此必须在启动前钳制。
	if workers > outboxLogicalShards {
		return outboxLogicalShards
	}
	return workers
}

func logicalShardsForWorker(worker, workers int) []int {
	if workers <= 0 || worker < 0 || worker >= workers {
		return nil
	}
	out := make([]int, 0, (outboxLogicalShards+workers-1)/workers)
	for shard := worker; shard < outboxLogicalShards; shard += workers {
		out = append(out, shard)
	}
	return out
}

// runWorker 是单个 claim 循环；多 worker 靠 ClaimPending 的 SKIP LOCKED 互不重叠。
// 空闲指数退避（interval→maxIdleInterval），claim 到事件即复位到 interval：有活快、无活省。
func (d *OutboxDispatcher) runWorker(ctx context.Context, claimer shardedOutboxClaimer, shardIDs []int) {
	dispatch := d.DispatchOnce
	if claimer != nil && len(shardIDs) > 0 {
		dispatch = func(ctx context.Context) bool {
			return d.dispatchOnceShards(ctx, claimer, shardIDs)
		}
	}
	runIdleBackoffLoop(ctx, d.interval, d.maxIdleInterval, dispatch)
}

// batchEventLoader 是 UpdateEventStore 的可选批量能力：一次取多条 (user,pts) 事件。
type batchEventLoader interface {
	BatchByCursor(ctx context.Context, cursors []store.EventCursor) ([]domain.UpdateEvent, error)
}

// batchOutboxMarker 是 DispatchOutboxStore 的可选批量能力：一次标记多行 delivered。
type batchOutboxMarker interface {
	MarkDeliveredBatch(ctx context.Context, items []store.DispatchOutboxItem) error
}

type shardedOutboxClaimer interface {
	ClaimPendingShards(ctx context.Context, shardCount int, shardIDs []int, limit int) ([]store.DispatchOutboxItem, error)
}

// DispatchOnce claim 一批 outbox 并投递，测试可直接调用。返回本次是否 claim 到事件，
// 供 runWorker 决定快轮询还是空闲退避。
// store 同时具备批量取事件 + 批量标记能力时走批量路径（每批 ~3 次 PG 往返），否则逐条回退。
func (d *OutboxDispatcher) DispatchOnce(ctx context.Context) bool {
	items, err := d.outbox.ClaimPending(ctx, d.batch)
	return d.dispatchClaimed(ctx, items, err)
}

func (d *OutboxDispatcher) dispatchOnceShards(ctx context.Context, claimer shardedOutboxClaimer, shardIDs []int) bool {
	items, err := claimer.ClaimPendingShards(ctx, outboxLogicalShards, shardIDs, d.batch)
	return d.dispatchClaimed(ctx, items, err)
}

func (d *OutboxDispatcher) dispatchClaimed(ctx context.Context, items []store.DispatchOutboxItem, err error) bool {
	if err != nil {
		d.log.Warn("claim dispatch outbox", zap.Error(err))
		return false
	}
	if len(items) == 0 {
		return false
	}
	sortDispatchItems(items)
	d.metrics.OutboxClaimed(len(items))
	if loader, ok := d.events.(batchEventLoader); ok {
		if marker, ok := d.outbox.(batchOutboxMarker); ok {
			d.dispatchBatch(ctx, items, loader, marker)
			return true
		}
	}
	blockedUsers := make(map[int64]struct{})
	for _, item := range items {
		if _, blocked := blockedUsers[item.TargetUserID]; blocked {
			continue
		}
		if !d.dispatchItem(ctx, item) {
			blockedUsers[item.TargetUserID] = struct{}{}
		}
	}
	return true
}

func sortDispatchItems(items []store.DispatchOutboxItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.TargetUserID != b.TargetUserID {
			return a.TargetUserID < b.TargetUserID
		}
		if a.Pts != b.Pts {
			return a.Pts < b.Pts
		}
		return a.ID < b.ID
	})
}

type outboxEventKey struct {
	userID int64
	pts    int
}

// dispatchBatch 批量加载已 claim 事件、逐条 push、批量标记 delivered；失败项单独退避重试。
func (d *OutboxDispatcher) dispatchBatch(ctx context.Context, items []store.DispatchOutboxItem, loader batchEventLoader, marker batchOutboxMarker) {
	valid := make([]store.DispatchOutboxItem, 0, len(items))
	blockedUsers := make(map[int64]struct{})
	for _, item := range items {
		if _, blocked := blockedUsers[item.TargetUserID]; blocked {
			continue
		}
		if err := validateOutboxExclusionPair(item); err != nil {
			d.markDispatchFailed(ctx, item, err)
			blockedUsers[item.TargetUserID] = struct{}{}
			continue
		}
		valid = append(valid, item)
	}
	if len(valid) == 0 {
		return
	}
	items = valid
	cursors := make([]store.EventCursor, len(items))
	for i, item := range items {
		cursors[i] = store.EventCursor{UserID: item.TargetUserID, Pts: item.Pts}
	}
	events, err := loader.BatchByCursor(ctx, cursors)
	if err != nil {
		// 批量取失败则整批回退逐条路径，让每条各自重试/标失败，不丢进度。
		d.log.Warn("batch load dispatch events", zap.Error(err))
		blockedUsers := make(map[int64]struct{})
		for _, item := range items {
			if _, blocked := blockedUsers[item.TargetUserID]; blocked {
				continue
			}
			if !d.dispatchItem(ctx, item) {
				blockedUsers[item.TargetUserID] = struct{}{}
			}
		}
		return
	}
	byKey := make(map[outboxEventKey]domain.UpdateEvent, len(events))
	for _, event := range events {
		byKey[outboxEventKey{event.UserID, event.Pts}] = event
	}
	start := time.Now()
	ready := make([]outboxDispatchReady, 0, len(items))
	requests := make([]OutboxUpdateRequest, 0, len(items))
	clear(blockedUsers)
	for _, item := range items {
		if _, blocked := blockedUsers[item.TargetUserID]; blocked {
			continue
		}
		event, ok := byKey[outboxEventKey{item.TargetUserID, item.Pts}]
		if !ok {
			d.markDispatchFailed(ctx, item, errMissingOutboxEvent)
			blockedUsers[item.TargetUserID] = struct{}{}
			continue
		}
		ready = append(ready, outboxDispatchReady{item: item})
		requests = append(requests, OutboxUpdateRequest{TargetUserID: item.TargetUserID, Event: event})
	}
	builtUpdates := d.buildOutboxUpdates(ctx, requests)
	delivered := make([]store.DispatchOutboxItem, 0, len(items))
	clear(blockedUsers)
	for i, entry := range ready {
		item := entry.item
		if _, blocked := blockedUsers[item.TargetUserID]; blocked {
			continue
		}
		update := builtUpdates[i]
		if update == nil {
			delivered = append(delivered, item)
			continue
		}
		if _, retriable, err := d.pushOutboxUpdate(ctx, item, update); err != nil {
			blockedUsers[item.TargetUserID] = struct{}{}
			if retriable {
				// 出站队列拥塞：留 dispatching 行靠租约过期重投，不计入 attempts 升级。
				// 不加入 delivered，故不会被 MarkDeliveredBatch 删除。
				continue
			}
			d.markDispatchFailed(ctx, item, err)
			continue
		}
		delivered = append(delivered, item)
	}
	if len(delivered) == 0 {
		return
	}
	if err := marker.MarkDeliveredBatch(ctx, delivered); err != nil {
		// 批量标记失败则逐条标记，避免整批已投递却卡在 dispatching 等租约过期重投。
		d.log.Warn("mark dispatch delivered batch", zap.Error(err))
		for _, item := range delivered {
			if markErr := d.outbox.MarkDelivered(ctx, item); markErr != nil {
				d.log.Warn("mark dispatch delivered", zap.Int64("target_user_id", item.TargetUserID), zap.Int64("outbox_id", item.ID), zap.Error(markErr))
			}
		}
	}
	per := time.Since(start) / time.Duration(len(delivered))
	for range delivered {
		d.metrics.OutboxDelivered(per)
	}
}

type outboxDispatchReady struct {
	item store.DispatchOutboxItem
}

func (d *OutboxDispatcher) dispatchItem(ctx context.Context, item store.DispatchOutboxItem) bool {
	start := time.Now()
	if err := validateOutboxExclusionPair(item); err != nil {
		d.markDispatchFailed(ctx, item, err)
		return false
	}
	events, err := d.events.ListAfter(ctx, item.TargetUserID, item.Pts-1, 1)
	if err != nil {
		d.markDispatchFailed(ctx, item, err)
		return false
	}
	if len(events) == 0 || events[0].Pts != item.Pts {
		d.markDispatchFailed(ctx, item, errMissingOutboxEvent)
		return false
	}
	update := d.buildOutboxUpdate(ctx, item, events[0])
	if update == nil {
		if err := d.outbox.MarkDelivered(ctx, item); err != nil {
			d.log.Warn("mark noop dispatch delivered", zap.Int64("target_user_id", item.TargetUserID), zap.Int64("outbox_id", item.ID), zap.Error(err))
			return false
		}
		d.metrics.OutboxDelivered(time.Since(start))
		return true
	}
	sent, retriable, err := d.pushOutboxUpdate(ctx, item, update)
	if err != nil {
		if retriable {
			// 出站队列拥塞：保留 dispatching 行，靠租约过期（defaultDispatchLease）重新 claim 重投，
			// 不计入 attempts 升级，避免正常满 fan-out 负载把可靠 update 误打成 failed。
			d.log.Debug("dispatch outbox deferred (push queue full)",
				zap.Int64("target_user_id", item.TargetUserID),
				zap.Int64("outbox_id", item.ID),
				zap.Int("pts", item.Pts),
			)
			return false
		}
		d.markDispatchFailed(ctx, item, err)
		return false
	}
	if err := d.outbox.MarkDelivered(ctx, item); err != nil {
		d.log.Warn("mark dispatch delivered", zap.Int64("target_user_id", item.TargetUserID), zap.Int64("outbox_id", item.ID), zap.Error(err))
		return false
	}
	d.metrics.OutboxDelivered(time.Since(start))
	d.log.Debug("dispatch outbox delivered",
		zap.Int64("target_user_id", item.TargetUserID),
		zap.Int64("outbox_id", item.ID),
		zap.Int("pts", item.Pts),
		zap.Int("sessions", sent),
	)
	return true
}

func (d *OutboxDispatcher) buildOutboxUpdate(ctx context.Context, item store.DispatchOutboxItem, event domain.UpdateEvent) *tg.Updates {
	updates := d.buildOutboxUpdates(ctx, []OutboxUpdateRequest{{TargetUserID: item.TargetUserID, Event: event}})
	if len(updates) == 0 {
		return nil
	}
	return updates[0]
}

func (d *OutboxDispatcher) buildOutboxUpdates(ctx context.Context, requests []OutboxUpdateRequest) []*tg.Updates {
	out := make([]*tg.Updates, len(requests))
	if len(requests) == 0 {
		return out
	}
	if d.updateBuilder != nil {
		built := d.updateBuilder(ctx, requests)
		if len(built) == len(requests) {
			return built
		}
		d.log.Warn("outbox update builder returned mismatched count",
			zap.Int("requests", len(requests)),
			zap.Int("updates", len(built)),
		)
	}
	for i, req := range requests {
		out[i] = tgUpdateForOutboxEvent(req.Event)
	}
	return out
}

// pushOutboxUpdate 投递一条 outbox update，返回 (送达的在线 session 数, 是否可重试, err)。
// 生产 SessionManager 会把 queue-full/closed 慢连接摘除并按离线处理，因此 best-effort
// 接口剩余的非 context 错误通常是确定性的编码/构造错误，必须进入 failed，不能永久占着
// dispatching head 靠租约空转。只有 dispatcher shutdown/deadline 属于可重试中断。
func (d *OutboxDispatcher) pushOutboxUpdate(ctx context.Context, item store.DispatchOutboxItem, update *tg.Updates) (sent int, retriable bool, err error) {
	if err := validateOutboxExclusionPair(item); err != nil {
		return 0, false, errInvalidOutboxExclusionPair
	}

	if d.pushTimeout > 0 {
		if bestEffort, ok := d.sessions.(BestEffortSessionBinder); ok {
			sent, err = bestEffort.PushToUserExceptAuthKeySessionBestEffort(ctx, item.TargetUserID, item.ExcludeAuthKeyID, item.ExcludeSessionID, proto.MessageFromServer, update, d.pushTimeout)
			return sent, outboxPushInterrupted(err), err
		}
	}
	sent, err = d.sessions.PushToUserExceptAuthKeySession(ctx, item.TargetUserID, item.ExcludeAuthKeyID, item.ExcludeSessionID, proto.MessageFromServer, update)
	if err != nil {
		return sent, outboxPushInterrupted(err), err
	}
	return sent, false, nil
}

func validateOutboxExclusionPair(item store.DispatchOutboxItem) error {
	var zeroAuthKeyID [8]byte
	if (item.ExcludeAuthKeyID != zeroAuthKeyID) != (item.ExcludeSessionID != 0) {
		return errInvalidOutboxExclusionPair
	}
	return nil
}

func outboxPushInterrupted(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (d *OutboxDispatcher) markDispatchFailed(ctx context.Context, item store.DispatchOutboxItem, err error) {
	if err == nil {
		err = errMissingOutboxEvent
	}
	if markErr := d.outbox.MarkFailed(ctx, item, err.Error()); markErr != nil {
		d.log.Warn("mark dispatch failed",
			zap.Int64("target_user_id", item.TargetUserID),
			zap.Int64("outbox_id", item.ID),
			zap.Error(markErr),
		)
		return
	}
	d.metrics.OutboxFailed(err)
	d.log.Debug("dispatch outbox failed",
		zap.Int64("target_user_id", item.TargetUserID),
		zap.Int64("outbox_id", item.ID),
		zap.Int("pts", item.Pts),
		zap.Error(err),
	)
}

func tgUpdateForOutboxEvent(event domain.UpdateEvent) *tg.Updates {
	return tgUpdateForOutboxEventForViewer(event, event.UserID)
}

func tgUpdateForOutboxEventForViewer(event domain.UpdateEvent, viewerUserID int64) *tg.Updates {
	if viewerUserID == 0 {
		viewerUserID = event.UserID
	}
	switch event.Type {
	case domain.UpdateEventNewMessage:
		return tgPrivateMessageUpdates(event, event.Message, 0, false, tgUsersForViewer(viewerUserID, event.Users), tgChannels(viewerUserID, event.Channels))
	case domain.UpdateEventReadHistoryInbox, domain.UpdateEventReadHistoryOutbox:
		var update tg.UpdateClass
		if event.Type == domain.UpdateEventReadHistoryOutbox {
			update = tgReadHistoryOutboxUpdate(event)
		} else {
			update = tgReadHistoryInboxUpdate(event)
		}
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Date:    event.Date,
			Seq:     0, // 私聊不维护账号级 seq，恒 0
		}
	case domain.UpdateEventChannelState:
		update := tgOtherUpdateFromEvent(event)
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Chats:   tgChannels(viewerUserID, event.Channels),
			Date:    event.Date,
			Seq:     0,
		}
	case domain.UpdateEventNoop:
		return nil
	default:
		update := tgOtherUpdateFromEvent(event)
		if update == nil {
			return nil
		}
		return &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
			Date:    event.Date,
			Seq:     0,
		}
	}
}

// appendAuxPtsBookkeeping 给"占账号 pts 但 TL update 不带 pts"的事件附一条
// 空 updateDeleteMessages：客户端按 pts/pts_count 推进本地水位且不产生任何
// 可见变化。没有它，客户端水位停在事件前，下一条带 pts 的更新会被判为空洞。
func appendAuxPtsBookkeeping(updates []tg.UpdateClass, event domain.UpdateEvent) []tg.UpdateClass {
	if event.Pts <= 0 || event.PtsCount <= 0 || !event.LacksWirePts() {
		return updates
	}
	return append(updates, &tg.UpdateDeleteMessages{
		Messages: []int{},
		Pts:      event.Pts,
		PtsCount: event.PtsCount,
	})
}
