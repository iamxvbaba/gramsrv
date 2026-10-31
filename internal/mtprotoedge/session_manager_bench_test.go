package mtprotoedge

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

// Connection-layer fan-out / churn load test: focused on SessionManager lock
// contention, without going through real sockets / encryption.
//
// The constructed Conn deliberately does not startOutbound: after pushToUser takes
// a locked snapshot of byUser, it calls c.Send for each conn outside the lock, and
// at that point c.outbound==nil immediately returns ErrConnClosed (see
// outbound.go), so the measurement concentrates on "the locked section + dispatch
// overhead", i.e. the global lock hot spot that sharding aims to eliminate. Each
// connection uses only struct memory (no 1024-capacity outbound channel, no
// goroutine), so it can be registered at the 200k scale.
//
// Usage:
//
//	go test ./internal/mtprotoedge/ -run '^$' -bench BenchmarkSessionManager -benchmem -cpu 1,4,8
//	go test ./internal/mtprotoedge/ -run '^$' -bench BenchmarkSessionManagerPushConcurrent -mutexprofile mu.out
//	TELESRV_LOAD_CONNS=200000 go test ./internal/mtprotoedge/ -run TestSessionManagerFanoutThroughput -v -timeout 300s

func benchConn(sessionID int64, authKeyID [8]byte, userID int64) *Conn {
	c := &Conn{sessionID: sessionID, authKeyID: authKeyID}
	if userID != 0 {
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
	}
	c.receivesUpdates.Store(true) // take fanout's "collect conns→Send outside lock" branch, not pending staging
	return c
}

func authKeyIDFromInt(v uint64) [8]byte {
	var id [8]byte
	for i := 0; i < 8; i++ {
		id[i] = byte(v >> (8 * i))
	}
	return id
}

// seedSessions registers conns connections, binding connsPerUser connections per
// user (simulating multiple devices). It returns the number of registered users
// (deduplicated, ordered range [1, userCount]).
func seedSessions(sm *SessionManager, conns, connsPerUser int) (userCount int) {
	if connsPerUser < 1 {
		connsPerUser = 1
	}
	for i := 0; i < conns; i++ {
		userID := int64(i/connsPerUser) + 1
		sm.Register(benchConn(int64(i)+1, authKeyIDFromInt(uint64(i)+1), userID))
	}
	return (conns + connsPerUser - 1) / connsPerUser
}

// BenchmarkSessionManagerPushConcurrent simulates the real hot spot at 200k online:
// many goroutines concurrently pushToUser to different users, all contending for
// the same global lock. -mutexprofile will surface SessionManager.mu at the top.
func BenchmarkSessionManagerPushConcurrent(b *testing.B) {
	const conns = 200_000
	const connsPerUser = 2
	sm := NewSessionManager(zap.NewNop())
	userCount := seedSessions(sm, conns, connsPerUser)
	msg := &tg.UpdatesTooLong{}
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var n uint64
		for pb.Next() {
			n++
			userID := int64(n%uint64(userCount)) + 1
			_, _ = sm.PushToUser(ctx, userID, proto.MessageFromServer, msg)
		}
	})
}

// BenchmarkSessionManagerRegisterChurn measures the lock cost of connection
// setup/teardown: concurrent Register+Unregister. 200k online means continuous
// connect/disconnect churn, each contending for the global write lock.
func BenchmarkSessionManagerRegisterChurn(b *testing.B) {
	sm := NewSessionManager(zap.NewNop())
	var seq atomic.Uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := seq.Add(1)
			c := benchConn(int64(id), authKeyIDFromInt(id), int64(id))
			sm.Register(c)
			sm.Unregister(c)
		}
	})
}

// BenchmarkSessionManagerPushFanoutWidth measures the fanout breadth cost of a
// single push: one user bound to many connections, and a single PushToUser must
// traverse all of them while holding the lock. A real private-chat user has few
// devices (2-4), so this is an upper-bound reference.
func BenchmarkSessionManagerPushFanoutWidth(b *testing.B) {
	for _, width := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("width=%d", width), func(b *testing.B) {
			sm := NewSessionManager(zap.NewNop())
			const userID = 1
			for i := 0; i < width; i++ {
				sm.Register(benchConn(int64(i)+1, authKeyIDFromInt(uint64(i)+1), userID))
			}
			msg := &tg.UpdatesTooLong{}
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = sm.PushToUser(ctx, userID, proto.MessageFromServer, msg)
			}
		})
	}
}

// TestSessionManagerFanoutThroughput is a data-driven throughput test: after
// registering N connections, P goroutines continuously push concurrently, measuring
// actual push throughput and p99 under the global lock. Small smoke scale by
// default; set TELESRV_LOAD_CONNS to scale up to 200k.
func TestSessionManagerFanoutThroughput(t *testing.T) {
	conns := envIntDefault("TELESRV_LOAD_CONNS", 20_000)
	connsPerUser := envIntDefault("TELESRV_LOAD_CONNS_PER_USER", 2)
	workers := envIntDefault("TELESRV_LOAD_PUSH_WORKERS", 0) // 0 → GOMAXPROCS
	duration := time.Duration(envIntDefault("TELESRV_LOAD_SECONDS", 3)) * time.Second
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	sm := NewSessionManager(zap.NewNop())
	t0 := time.Now()
	userCount := seedSessions(sm, conns, connsPerUser)
	seedWall := time.Since(t0)
	if got := sm.Online(); got != conns {
		t.Fatalf("online = %d, want %d", got, conns)
	}

	msg := &tg.UpdatesTooLong{}
	ctx := context.Background()
	var ops atomic.Int64
	perWorkerLat := make([][]time.Duration, workers)

	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			lat := make([]time.Duration, 0, 1<<16)
			var n uint64
			for time.Now().Before(deadline) {
				// Check the clock once every batch of 256, reducing time.Now's share.
				for j := 0; j < 256; j++ {
					n++
					userID := int64(n%uint64(userCount)) + 1
					s := time.Now()
					_, _ = sm.PushToUser(ctx, userID, proto.MessageFromServer, msg)
					lat = append(lat, time.Since(s))
				}
				ops.Add(256)
			}
			perWorkerLat[w] = lat
		}(w)
	}
	wg.Wait()
	wall := time.Since(start)

	all := make([]time.Duration, 0, ops.Load())
	for _, l := range perWorkerLat {
		all = append(all, l...)
	}
	sortDurations(all)
	total := ops.Load()
	thr := float64(total) / wall.Seconds()

	t.Logf("==== session_manager fan-out throughput ====")
	t.Logf("config:  conns=%d connsPerUser=%d users=%d pushWorkers=%d dur=%s seed=%s",
		conns, connsPerUser, userCount, workers, duration, seedWall.Round(time.Millisecond))
	t.Logf("push:    %d ops in %s -> %.0f push/s", total, wall.Round(time.Millisecond), thr)
	t.Logf("push.lat p50=%s p90=%s p99=%s max=%s",
		pct(all, 50), pct(all, 90), pct(all, 99), pct(all, 100))
	t.Logf("=============================================")
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p*len(sorted))/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sortDurations(d []time.Duration) {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
}

func envIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
