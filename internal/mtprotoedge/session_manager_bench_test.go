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

// 连接层 fan-out / churn 压测：聚焦 SessionManager 的锁争用，不走真实 socket / 加密。
//
// 构造的 Conn 故意不 startOutbound：pushToUser 持锁快照 byUser 后，锁外对每个 conn 调 c.Send，
// 此时 c.outbound==nil 立即返回 ErrConnClosed（见 outbound.go），因此测量集中在「持锁段 + 分发开销」，
// 即分片要消除的全局锁热点。每条连接仅结构体内存（无 1024 容量的 outbound channel、无 goroutine），
// 故可注册到 20 万规模。
//
// 用法：
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
	c.receivesUpdates.Store(true) // 走 fanout 的「收集 conns→锁外 Send」分支，而非 pending 暂存
	return c
}

func authKeyIDFromInt(v uint64) [8]byte {
	var id [8]byte
	for i := 0; i < 8; i++ {
		id[i] = byte(v >> (8 * i))
	}
	return id
}

// seedSessions 注册 conns 个连接，每个 user 绑定 connsPerUser 个连接（模拟多设备）。
// 返回注册的 userID 列表（去重、有序范围 [1, userCount]）。
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

// BenchmarkSessionManagerPushConcurrent 模拟 20 万在线下的真实热点：大量 goroutine 并发对
// 不同 user pushToUser，全部抢同一把全局锁。-mutexprofile 会把 SessionManager.mu 顶上来。
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

// BenchmarkSessionManagerRegisterChurn 测连接建立/断开的锁成本：并发 Register+Unregister。
// 20 万在线意味着持续的 connect/disconnect churn，每次都抢全局写锁。
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

// BenchmarkSessionManagerPushFanoutWidth 测单次 push 的 fanout 广度成本：一个 user 绑定很多连接，
// 单次 PushToUser 要持锁遍历全部。真实私聊 user 设备数少（2-4），此为上界参考。
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

// TestSessionManagerFanoutThroughput 是数据驱动吞吐测：注册 N 连接后，P 个 goroutine 持续并发
// push，测全局锁下的实际 push 吞吐与 p99。默认小规模冒烟；设 TELESRV_LOAD_CONNS 放大到 20 万。
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
				// 批量 256 次再查一次时钟，降低 time.Now 占比。
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
