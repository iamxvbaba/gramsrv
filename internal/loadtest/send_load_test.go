package loadtest

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	messageapp "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/mtprotoedge"
	"telesrv/internal/rpc"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/redisstore"
)

// Phase-1 single-machine SLO targets, from the Next Execution Plan in
// docs/message-module.md. By default they are only printed as information; when
// TELESRV_LOAD_ENFORCE_SLO=1 an overrun fails (used as a regression gate).
const (
	sloSendP99     = 150 * time.Millisecond
	sloDiffP99     = 100 * time.Millisecond
	sloThroughput  = 200.0 // msg/s
	drainTimeout   = 60 * time.Second
	diffSampleGoal = 500
)

// TestMessageSendBaseline load-tests the private-text send hot path with real
// PostgreSQL + Redis, runs an outbox dispatcher concurrently to drain online
// pushes, and finally samples the getDifference read path.
//
// This is a closed-loop saturation load test: concurrency workers each send
// continuously until messages have been sent. Throughput = messages / wallclock,
// and the latency percentiles reflect the saturation latency at that concurrency.
// A fixed-arrival-rate (open-loop) version is left for later refinement (see
// docs/message-module.md).
func TestMessageSendBaseline(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN and TELESRV_TEST_REDIS_ADDR to run message load baseline")
	}

	// The default user pool is large: too few users concentrate writes onto a handful
	// of dialog/message_box rows and cause row-lock contention, inflating send tail
	// latency (a small-pool artifact; with 200k users spread out, contention is tiny
	// in production).
	users := envInt("TELESRV_LOAD_USERS", 1000)
	if users < 2 {
		users = 2
	}
	concurrency := envInt("TELESRV_LOAD_CONCURRENCY", 32)
	if concurrency < 1 {
		concurrency = 1
	}
	totalMsgs := envInt("TELESRV_LOAD_MESSAGES", 5000)
	if totalMsgs < 1 {
		totalMsgs = 1
	}
	poolConns := envInt("TELESRV_LOAD_POOL_CONNS", 64)
	workers := envInt("TELESRV_OUTBOX_WORKERS", 8)
	outboxBatch := envInt("TELESRV_OUTBOX_BATCH", 100)
	outboxInterval := envDuration("TELESRV_OUTBOX_INTERVAL", 50*time.Millisecond)
	leaseTimeout := envDuration("TELESRV_OUTBOX_LEASE_TIMEOUT", 30*time.Second)
	enforceSLO := os.Getenv("TELESRV_LOAD_ENFORCE_SLO") == "1"
	deferDispatch := os.Getenv("TELESRV_LOAD_DEFER_DISPATCH") == "1"

	ctx := context.Background()
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.Open(ctx, dsn, postgres.WithMaxConns(poolConns))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	rdb, err := redisstore.Open(ctx, redisAddr, os.Getenv("TELESRV_TEST_REDIS_PASSWORD"), 0)
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	// Wire the message hot path exactly as main.go does: Redis box_id allocator +
	// PG message store + transactional outbox.
	userStore := postgres.NewUserStore(pool)
	updateEventStore := postgres.NewUpdateEventStore(pool)
	dispatchOutboxStore := postgres.NewDispatchOutboxStore(pool, postgres.WithLeaseTimeout(leaseTimeout))
	dialogStore := postgres.NewDialogStore(pool)
	boxIDAllocator := redisstore.NewBoxIDAllocator(rdb, postgres.NewMessageBoxCounterSource(pool))
	messageStore := postgres.NewMessageStore(pool, postgres.WithMessageAllocators(boxIDAllocator))
	svc := messageapp.NewService(messageStore, dialogStore)

	// Create an isolated test user pool; use a random salt to isolate leftover
	// history, and clean up in FK dependency order at the end.
	ids := seedUsers(t, ctx, userStore, users)
	t.Cleanup(func() { cleanup(t, pool, rdb, ids) })

	// The online-push binder uses a real SessionManager (zero connections);
	// PushToUserExceptSession returns 0, so the outbox does the full
	// claim->ListAfter->MarkDelivered PG round trip, measuring draining rather than
	// network fanout.
	binder := mtprotoedge.NewSessionManager(zap.NewNop())
	metrics := &loadMetrics{}
	projectionRouter := rpc.New(rpc.Config{}, rpc.Deps{
		Users: appusers.NewService(userStore),
	}, zap.NewNop(), clock.System)
	dispatcher := rpc.NewOutboxDispatcher(updateEventStore, dispatchOutboxStore, binder, zap.NewNop(),
		rpc.WithOutboxWorkers(workers),
		rpc.WithOutboxBatch(outboxBatch),
		rpc.WithOutboxInterval(outboxInterval),
		rpc.WithOutboxMetrics(metrics),
		rpc.WithOutboxUpdateBuilder(projectionRouter.BuildOutboxUpdates),
	)
	dispCtx, stopDispatcher := context.WithCancel(ctx)
	dispDone := make(chan struct{})
	startDispatcher := func() {
		go func() {
			dispatcher.Run(dispCtx)
			close(dispDone)
		}()
	}
	// With deferDispatch=1, don't dispatch first: let sending build the backlog to the
	// max, then start the dispatcher after sending ends, to isolate the "pure drain
	// ceiling" (otherwise the dispatcher keeps up in real time, backlog stays near 0,
	// and the ceiling is unmeasurable).
	if !deferDispatch {
		startDispatcher()
	}

	// Sample the outbox backlog (pending+dispatching) in the background, recording the
	// runtime peak.
	var maxBacklog atomic.Int64
	sampleCtx, stopSampler := context.WithCancel(ctx)
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-ticker.C:
				if n := backlog(ctx, pool, ids); n > maxBacklog.Load() {
					maxBacklog.Store(n)
				}
			}
		}
	}()

	// Random RandomID base, ensuring (sender,random_id) is idempotently unique and
	// does not collide across reruns.
	randBase := int64(randomUint64(t) & 0x7fff_ffff_ffff)
	nowUnix := int(time.Now().Unix())
	body := "telesrv load baseline message body"

	perWorkerLat := make([][]time.Duration, concurrency)
	var sent, dup, sendErr atomic.Int64
	var counter atomic.Int64

	// Warmup: concurrently send a batch of un-timed messages first, warming the
	// connection pool (MinConns->MaxConns) and the PG plan cache, so the later timed
	// window reflects steady state rather than cold-start tail latency (with an
	// over-cold pool the first 32 concurrent requests squeeze through few connections).
	warmup := envInt("TELESRV_LOAD_WARMUP", min(users*10, 1000))
	if warmup > 0 {
		var wwg sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wwg.Add(1)
			go func() {
				defer wwg.Done()
				for {
					n := counter.Add(1)
					if n > int64(warmup) {
						return
					}
					sid := ids[(n-1)%int64(users)]
					rid := ids[n%int64(users)]
					_, _ = svc.SendPrivateText(ctx, sid, domain.SendPrivateTextRequest{
						SenderUserID:    sid,
						RecipientUserID: rid,
						RandomID:        randBase + n,
						Message:         body,
						Date:            nowUnix,
					})
				}
			}()
		}
		wwg.Wait()
		counter.Store(int64(warmup))
	}

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			lat := make([]time.Duration, 0, totalMsgs/concurrency+1)
			for {
				n := counter.Add(1)
				if n > int64(warmup+totalMsgs) {
					break
				}
				senderID := ids[(n-1)%int64(users)]
				recipientID := ids[n%int64(users)]
				req := domain.SendPrivateTextRequest{
					SenderUserID:    senderID,
					RecipientUserID: recipientID,
					RandomID:        randBase + n,
					Message:         body,
					Date:            nowUnix,
				}
				t0 := time.Now()
				res, err := svc.SendPrivateText(ctx, senderID, req)
				lat = append(lat, time.Since(t0))
				if err != nil {
					sendErr.Add(1)
					continue
				}
				if res.Duplicate {
					dup.Add(1)
				}
				sent.Add(1)
			}
			perWorkerLat[w] = lat
		}(w)
	}
	wg.Wait()
	sendWall := time.Since(start)
	deliveredAtSendEnd := metrics.delivered.Load()
	if deferDispatch {
		// Sending has built the backlog to the max, so start the dispatcher now: the
		// drain phase is pure draining, and drainRate reflects the drain ceiling.
		startDispatcher()
	}

	// Wait for the outbox to drain (backlog back to 0), record the drain time, then
	// stop the sampler and dispatcher. Measuring the drain rate alone "after sending
	// ends" isolates dispatcher throughput without the send-phase contention on PG.
	drainStart := time.Now()
	drained := waitDrain(ctx, pool, ids, drainTimeout)
	drainWall := time.Since(drainStart)
	drainRate := float64(metrics.delivered.Load()-deliveredAtSendEnd) / drainWall.Seconds()
	stopSampler()
	<-sampleDone
	stopDispatcher()
	<-dispDone

	// Merge and sort the send latency samples.
	latencies := make([]time.Duration, 0, totalMsgs)
	for _, l := range perWorkerLat {
		latencies = append(latencies, l...)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	// Sample the getDifference read path (ListAfter pulling account events from pts=0).
	diffLat := sampleGetDifference(t, ctx, updateEventStore, ids)
	sort.Slice(diffLat, func(i, j int) bool { return diffLat[i] < diffLat[j] })

	okSent := sent.Load()
	throughput := float64(okSent) / sendWall.Seconds()
	sendP99 := percentile(latencies, 99)
	diffP99 := percentile(diffLat, 99)

	t.Logf("==== message module load baseline ====")
	t.Logf("config:  users=%d concurrency=%d messages=%d pool=%d outbox(workers=%d batch=%d interval=%s lease=%s)",
		users, concurrency, totalMsgs, poolConns, workers, outboxBatch, outboxInterval, leaseTimeout)
	t.Logf("send:    %d ok, %d dup, %d err in %s -> %.0f msg/s",
		okSent, dup.Load(), sendErr.Load(), sendWall.Round(time.Millisecond), throughput)
	t.Logf("send.lat p50=%s p90=%s p99=%s max=%s",
		percentile(latencies, 50).Round(time.Microsecond),
		percentile(latencies, 90).Round(time.Microsecond),
		sendP99.Round(time.Microsecond),
		percentile(latencies, 100).Round(time.Microsecond))
	t.Logf("outbox:  delivered=%d failed=%d claimed=%d maxBacklog=%d drain=%s drainRate=%.0f rows/s drained=%v",
		metrics.delivered.Load(), metrics.failed.Load(), metrics.claimed.Load(),
		maxBacklog.Load(), drainWall.Round(time.Millisecond), drainRate, drained)
	t.Logf("getDiff: samples=%d p50=%s p90=%s p99=%s max=%s",
		len(diffLat),
		percentile(diffLat, 50).Round(time.Microsecond),
		percentile(diffLat, 90).Round(time.Microsecond),
		diffP99.Round(time.Microsecond),
		percentile(diffLat, 100).Round(time.Microsecond))
	t.Logf("SLO:     send.p99 %s(<%s) %s | getDiff.p99 %s(<%s) %s | throughput %.0f(>=%.0f) %s",
		sendP99.Round(time.Millisecond), sloSendP99, pass(sendP99 < sloSendP99),
		diffP99.Round(time.Millisecond), sloDiffP99, pass(diffP99 < sloDiffP99),
		throughput, sloThroughput, pass(throughput >= sloThroughput))
	t.Logf("=======================================")

	// Hard correctness assertions: sending must not error, there must be no unexpected
	// duplicates, and the outbox must drain with no terminal failures.
	if sendErr.Load() != 0 {
		t.Fatalf("send errors = %d, want 0", sendErr.Load())
	}
	if dup.Load() != 0 {
		t.Fatalf("duplicates = %d, want 0 (random_id must be unique)", dup.Load())
	}
	if okSent != int64(totalMsgs) {
		t.Fatalf("sent ok = %d, want %d", okSent, totalMsgs)
	}
	if !drained {
		t.Fatalf("outbox did not drain within %s, residual backlog %d", drainTimeout, backlog(ctx, pool, ids))
	}
	if metrics.failed.Load() != 0 {
		t.Fatalf("outbox failed = %d, want 0", metrics.failed.Load())
	}

	// Performance SLO: informational by default; only gate mode
	// (TELESRV_LOAD_ENFORCE_SLO=1) fails hard.
	if enforceSLO {
		if sendP99 >= sloSendP99 {
			t.Errorf("send p99 %s >= SLO %s", sendP99, sloSendP99)
		}
		if diffP99 >= sloDiffP99 {
			t.Errorf("getDifference p99 %s >= SLO %s", diffP99, sloDiffP99)
		}
		if throughput < sloThroughput {
			t.Errorf("throughput %.0f msg/s < SLO %.0f msg/s", throughput, sloThroughput)
		}
	}
}

// loadMetrics implements rpc.Metrics, counting outbox claim/deliver/fail.
type loadMetrics struct {
	claimed   atomic.Int64
	delivered atomic.Int64
	failed    atomic.Int64
}

func (m *loadMetrics) MessageSend(time.Duration, bool, error)          {}
func (m *loadMetrics) MessageRateLimited(int)                          {}
func (m *loadMetrics) OutboxClaimed(n int)                             { m.claimed.Add(int64(n)) }
func (m *loadMetrics) OutboxDelivered(time.Duration)                   { m.delivered.Add(1) }
func (m *loadMetrics) OutboxFailed(error)                              { m.failed.Add(1) }
func (m *loadMetrics) PresenceLastSeenBatch(int, time.Duration, error) {}
func (m *loadMetrics) PresenceLastSeenSubmitted()                      {}
func (m *loadMetrics) PresenceLastSeenPending(int)                     {}
func (m *loadMetrics) PresenceLastSeenOverflow()                       {}
func (m *loadMetrics) PresenceLastSeenDrainDropped(int)                {}

func seedUsers(t *testing.T, ctx context.Context, store *postgres.UserStore, n int) []int64 {
	t.Helper()
	salt := randomUint64(t) % 1_000_000
	ids := make([]int64, n)
	errs := make([]error, n)
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			u, err := store.Create(ctx, domain.User{
				AccessHash: int64(i + 1),
				Phone:      fmt.Sprintf("+1555%06d%05d", salt, i),
				FirstName:  fmt.Sprintf("Load%05d", i),
			})
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = u.ID
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("create load user %d: %v", i, err)
		}
	}
	return ids
}

// cleanup deletes test data in FK dependency order:
// outbox->events->boxes->private_messages->dialogs->users, then clears the Redis
// box_id counters. message_boxes.from_user_id is ON DELETE RESTRICT, so the boxes
// must be deleted first. cleanup runs after the assertions; errors only warn and do
// not affect results already obtained.
func cleanup(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, ids []int64) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		"DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])",
		"DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])",
		"DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])",
		"DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])",
		"DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])",
		"DELETE FROM users WHERE id = ANY($1::bigint[])",
	}
	for _, sql := range stmts {
		if _, err := pool.Exec(ctx, sql, ids); err != nil {
			t.Logf("cleanup %q: %v", sql, err)
		}
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, fmt.Sprintf("counter:box_id:{%d}", id))
	}
	if err := rdb.Del(ctx, keys...).Err(); err != nil {
		t.Logf("cleanup redis counters: %v", err)
	}
}

// backlog returns the current number of undelivered (pending+dispatching) outbox
// rows for the test user set.
func backlog(ctx context.Context, pool *pgxpool.Pool, ids []int64) int64 {
	var n int64
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[]) AND status IN ('pending','dispatching')",
		ids,
	).Scan(&n); err != nil {
		return -1
	}
	return n
}

// waitDrain polls backlog until it reaches zero or times out, returning whether it drained.
func waitDrain(ctx context.Context, pool *pgxpool.Pool, ids []int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if backlog(ctx, pool, ids) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func sampleGetDifference(t *testing.T, ctx context.Context, events *postgres.UpdateEventStore, ids []int64) []time.Duration {
	t.Helper()
	out := make([]time.Duration, 0, diffSampleGoal)
	for i := 0; len(out) < diffSampleGoal; i++ {
		id := ids[i%len(ids)]
		t0 := time.Now()
		if _, err := events.ListAfter(ctx, id, 0, 100); err != nil {
			t.Fatalf("getDifference ListAfter user %d: %v", id, err)
		}
		out = append(out, time.Since(t0))
	}
	return out
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func pass(ok bool) string {
	if ok {
		return "PASS"
	}
	return "WARN"
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func randomUint64(t *testing.T) uint64 {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return binary.LittleEndian.Uint64(b[:])
}
