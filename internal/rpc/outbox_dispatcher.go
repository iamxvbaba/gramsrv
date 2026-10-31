package rpc

import (
	"context"
	"errors"
	"fmt"
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
	defaultOutboxBatch    = 10
	defaultOutboxInterval = 200 * time.Millisecond
	defaultOutboxWorkers  = 4
	// outboxLogicalShards is the stable user->lane hash space. It does not change with the runtime
	// worker count; each worker exclusively owns a subset of shards, ensuring the same user always
	// stays serialized on a single lane while different users can run in parallel.
	outboxLogicalShards = store.DispatchOutboxLogicalShards
	// defaultOutboxMaxIdleInterval is the idle backoff ceiling: on consecutive empty claims the poll
	// interval backs off exponentially from interval up to this ceiling, cutting the "query the DB
	// every 200ms even with no messages" busy-work; as soon as there is activity it resets to
	// interval immediately. The cost is that the first message after a long silence waits at most
	// this ceiling — backoff is only reached while continuously idle, and recent activity during a
	// session keeps fast polling, so normal send/receive latency is unaffected.
	defaultOutboxMaxIdleInterval = 1 * time.Second
)

var (
	errMissingOutboxEvent         = errors.New("missing outbox update event")
	errOutboxUpdateBuilderMissing = errors.New("outbox update builder is required")
	errOutboxUpdateBuilderCount   = errors.New("outbox update builder returned mismatched count")
	errOutboxUpdateBuilderEmpty   = errors.New("outbox update builder returned a nil non-noop update")
	errInvalidOutboxExclusionPair = errors.New("outbox exclusion requires both raw auth key and session id")
)

// OutboxDispatcher batch-pushes updates from the PG transactional outbox to online sessions.
// Multiple workers claim concurrently: ClaimPending uses FOR UPDATE SKIP LOCKED, so claims never
// overlap between workers.
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

// OutboxOption adjusts the OutboxDispatcher's runtime parameters.
type OutboxOption func(*OutboxDispatcher)

// OutboxUpdateRequest is the batch input for an outbox worker building online updates.
type OutboxUpdateRequest struct {
	TargetUserID int64
	Event        domain.UpdateEvent
}

// OutboxUpdateBuilder batch-converts domain.UpdateEvent into TL updates from each recipient's
// perspective.
type OutboxUpdateBuilder func(ctx context.Context, requests []OutboxUpdateRequest) ([]*tg.Updates, error)

// WithOutboxUpdateBuilder injects the recipient-perspective batch updates builder.
func WithOutboxUpdateBuilder(builder OutboxUpdateBuilder) OutboxOption {
	return func(d *OutboxDispatcher) {
		d.updateBuilder = builder
	}
}

// WithOutboxBatch sets the maximum number of rows per claim; <=0 keeps the default.
func WithOutboxBatch(n int) OutboxOption {
	return func(d *OutboxDispatcher) {
		if n > 0 {
			d.batch = n
		}
	}
}

// WithOutboxInterval sets the poll interval between two claims; <=0 keeps the default.
func WithOutboxInterval(interval time.Duration) OutboxOption {
	return func(d *OutboxDispatcher) {
		if interval > 0 {
			d.interval = interval
		}
	}
}

// WithOutboxWorkers sets the number of concurrent claim workers; <=0 keeps the default.
func WithOutboxWorkers(n int) OutboxOption {
	return func(d *OutboxDispatcher) {
		if n > 0 {
			d.workers = n
		}
	}
}

// WithOutboxPushTimeout sets the wait time for updates fanout enqueue; <=0 uses synchronous
// reliable push.
func WithOutboxPushTimeout(timeout time.Duration) OutboxOption {
	return func(d *OutboxDispatcher) {
		if timeout > 0 {
			d.pushTimeout = timeout
		}
	}
}

// WithOutboxMetrics injects the metrics implementation; nil keeps NopMetrics.
func WithOutboxMetrics(m Metrics) OutboxOption {
	return func(d *OutboxDispatcher) {
		if m != nil {
			d.metrics = m
		}
	}
}

// NewOutboxDispatcher creates the online update push worker. See defaultOutboxBatch/
// defaultOutboxInterval for the batch/interval defaults; they can be overridden via WithOutbox*
// options (in production, injected from config).
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

// Run starts workers concurrent workers that continuously claim pending outbox rows; when ctx
// exits, all stop and are waited on.
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
		// When a test double or an old store has no user shard claim, force a single worker so that
		// different pts for the same user are not claimed in parallel.
		return 1
	}
	// Workers beyond the number of logical shards have no exclusive lane. Letting an empty-shard
	// worker fall back to a global claim would overlap with all the sharded workers, so it must be
	// clamped before startup.
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

// runWorker is a single claim loop; multiple workers avoid overlap via ClaimPending's SKIP LOCKED.
// Idle exponential backoff (interval->maxIdleInterval); claiming an event resets to interval: fast
// when active, cheap when idle.
func (d *OutboxDispatcher) runWorker(ctx context.Context, claimer shardedOutboxClaimer, shardIDs []int) {
	dispatch := d.DispatchOnce
	if claimer != nil && len(shardIDs) > 0 {
		dispatch = func(ctx context.Context) bool {
			return d.dispatchOnceShards(ctx, claimer, shardIDs)
		}
	}
	runIdleBackoffLoop(ctx, d.interval, d.maxIdleInterval, dispatch)
}

// batchEventLoader is an optional batch capability of UpdateEventStore: fetch multiple (user,pts)
// events at once.
type batchEventLoader interface {
	BatchByCursor(ctx context.Context, cursors []store.EventCursor) ([]domain.UpdateEvent, error)
}

// batchOutboxMarker is an optional batch capability of DispatchOutboxStore: mark multiple rows
// delivered at once.
type batchOutboxMarker interface {
	MarkDeliveredBatch(ctx context.Context, items []store.DispatchOutboxItem) error
}

type shardedOutboxClaimer interface {
	ClaimPendingShards(ctx context.Context, shardCount int, shardIDs []int, limit int) ([]store.DispatchOutboxItem, error)
}

// DispatchOnce claims a batch of outbox rows and dispatches them; tests can call it directly.
// Returns whether an event was claimed this time, so runWorker can decide between fast polling and
// idle backoff.
// When the store supports both batch event fetching and batch marking it takes the batch path
// (~3 PG round-trips per batch); otherwise it falls back to per-row.
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

// dispatchBatch batch-loads already-claimed events, pushes them one by one, and batch-marks them
// delivered; failed items back off and retry individually.
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
		// If batch fetching fails, fall back to the per-row path for the whole batch so each row
		// retries or is marked failed on its own, without losing progress.
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
	builtUpdates, err := d.buildOutboxUpdates(ctx, requests)
	if err != nil {
		// An aggregate build failure does not mean every durable row is bad. Reuse the events
		// already loaded for this batch to isolate singletons, so one bad row or a batch-capacity
		// error does not poison unrelated users; once a user's lane head fails, later pts still must
		// not jump ahead of it.
		d.log.Warn("batch build dispatch outbox updates; isolating items", zap.Error(err))
		clear(blockedUsers)
		for i, entry := range ready {
			item := entry.item
			if _, blocked := blockedUsers[item.TargetUserID]; blocked {
				continue
			}
			if !d.dispatchPreparedItem(ctx, item, requests[i].Event, time.Now()) {
				blockedUsers[item.TargetUserID] = struct{}{}
			}
		}
		return
	}
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
				// Outbound queue congestion: leave the dispatching row to be redelivered when the
				// lease expires, without incrementing attempts. It is not added to delivered, so it
				// will not be removed by MarkDeliveredBatch.
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
		// If batch marking fails, mark one by one so the whole batch is not stuck in dispatching
		// waiting for lease expiry and redelivery.
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
	return d.dispatchPreparedItem(ctx, item, events[0], start)
}

// dispatchPreparedItem builds and dispatches an already-loaded event whose exclusion pair has been
// validated. When the batch-build isolation path calls it, it does not read the event store again.
func (d *OutboxDispatcher) dispatchPreparedItem(ctx context.Context, item store.DispatchOutboxItem, event domain.UpdateEvent, start time.Time) bool {
	update, err := d.buildOutboxUpdate(ctx, item, event)
	if err != nil {
		d.markDispatchFailed(ctx, item, err)
		return false
	}
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
			// Outbound queue congestion: keep the dispatching row to be re-claimed and redelivered
			// after the lease expires (defaultDispatchLease), without incrementing attempts, so a
			// normal full fan-out load does not wrongly mark a reliable update as failed.
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

func (d *OutboxDispatcher) buildOutboxUpdate(ctx context.Context, item store.DispatchOutboxItem, event domain.UpdateEvent) (*tg.Updates, error) {
	updates, err := d.buildOutboxUpdates(ctx, []OutboxUpdateRequest{{TargetUserID: item.TargetUserID, Event: event}})
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, nil
	}
	return updates[0], nil
}

func (d *OutboxDispatcher) buildOutboxUpdates(ctx context.Context, requests []OutboxUpdateRequest) ([]*tg.Updates, error) {
	out := make([]*tg.Updates, len(requests))
	if len(requests) == 0 {
		return out, nil
	}
	if d.updateBuilder == nil {
		return nil, errOutboxUpdateBuilderMissing
	}
	built, err := d.updateBuilder(ctx, requests)
	if err != nil {
		return nil, err
	}
	if len(built) != len(requests) {
		return nil, fmt.Errorf("%w: got %d want %d", errOutboxUpdateBuilderCount, len(built), len(requests))
	}
	for i := range built {
		if built[i] == nil && requests[i].Event.Type != domain.UpdateEventNoop {
			return nil, fmt.Errorf("%w: index=%d user_id=%d pts=%d event_type=%s", errOutboxUpdateBuilderEmpty, i, requests[i].TargetUserID, requests[i].Event.Pts, requests[i].Event.Type)
		}
	}
	return built, nil
}

// pushOutboxUpdate dispatches one outbox update, returning (number of online sessions reached,
// whether it is retriable, err). The production SessionManager removes queue-full/closed slow
// connections and treats them as offline, so the remaining non-context errors from the best-effort
// interface are usually deterministic encoding/construction errors that must go to failed rather
// than permanently holding the dispatching head idle via the lease. Only a dispatcher
// shutdown/deadline is a retriable interruption.
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
		if event.Message.Deleted {
			return tgDeletedPrivateMessageOutboxUpdate(event)
		}
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
			Seq:     0, // private chats keep no account-level seq, always 0
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

func tgDeletedPrivateMessageOutboxUpdate(event domain.UpdateEvent) *tg.Updates {
	if event.Pts <= 0 || event.PtsCount <= 0 {
		return nil
	}
	ids := []int{}
	if event.Message.ID > 0 && event.Message.ID <= domain.MaxMessageBoxID {
		ids = append(ids, event.Message.ID)
	}
	date := event.Date
	if date == 0 {
		date = event.Message.Date
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateDeleteMessages{
			Messages: ids,
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}},
		Date: date,
		Seq:  0,
	}
}

// appendAuxPtsBookkeeping attaches an empty updateDeleteMessages to events that "consume account
// pts but whose TL update carries no pts": the client advances its local watermark by pts/pts_count
// without producing any visible change. Without it, the client watermark stops before the event and
// the next pts-bearing update is treated as a gap.
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
