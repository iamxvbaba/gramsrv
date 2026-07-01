package phone

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"

	"telesrv/internal/domain"
)

// registry 是 active call 的进程内权威存储：单实现、不进 store 双实现体系。
//
// 论证（设计已确认，勿改回双 store）：active call 是秒级短命状态，服务端重启后
// 客户端侧媒体连接早已断开，「重启恢复半截通话」不是有效需求；单一实现让测试与
// 生产共用同一份代码，从构造上消灭 memory/postgres 行为漂移。多实例化时以同样的
// 窄接口换 Redis 实现，信令层零改动。
type registry struct {
	mu       sync.Mutex
	byID     map[int64]*entry
	byRandom map[randomKey]int64 // (callerID, randomID) → callID，吸收客户端 RPC 重试
	active   map[int64]int       // userID → 非终态通话数（并发上限依据）
}

type randomKey struct {
	callerID int64
	randomID int64
}

// entry 持有一通通话；call 字段由 registry.mu 保护。
// sigMu 单独串行化该通话的信令转发（锁内做推送入队），与状态锁分离，
// 保证 discard 等状态迁移不被对端出站队列堵塞拖住。
type entry struct {
	call domain.PhoneCall

	sigMu        sync.Mutex
	sigWindowSec int64
	sigCount     int
}

func newRegistry() *registry {
	return &registry{
		byID:     make(map[int64]*entry),
		byRandom: make(map[randomKey]int64),
		active:   make(map[int64]int),
	}
}

// sweepLocked 是 P1 的纯年龄 GC（调用方持有 r.mu）：
//   - 终态 tombstone 超过 tombstoneTTL → 回收（密钥材料随之销毁）；
//   - 非终态超过 2×ringTimeout → 直接回收（双端同时崩溃的兜底，防僵尸通话
//     吃满并发上限；不推送、不落历史，正常超时由客户端定时器与 P2 dispatcher 处理）。
func (r *registry) sweepLocked(nowUnix int64, ringTimeoutSec, tombstoneTTLSec int64) {
	for id, e := range r.byID {
		switch {
		case e.call.Terminal():
			if nowUnix-int64(e.call.DiscardedAt) > tombstoneTTLSec {
				r.removeLocked(id, e, false)
			}
		default:
			if nowUnix-int64(e.call.Date) > 2*ringTimeoutSec {
				r.removeLocked(id, e, true)
			}
		}
	}
}

func (r *registry) removeLocked(id int64, e *entry, wasActive bool) {
	delete(r.byID, id)
	delete(r.byRandom, randomKey{callerID: e.call.AdminID, randomID: e.call.RandomID})
	if wasActive {
		r.decActiveLocked(e.call.AdminID)
	}
}

func (r *registry) decActiveLocked(userID int64) {
	if n := r.active[userID]; n <= 1 {
		delete(r.active, userID)
	} else {
		r.active[userID] = n - 1
	}
}

// markDiscardedLocked 把非终态 entry 迁入终态并更新并发计数。
func (r *registry) markDiscardedLocked(e *entry, reason domain.PhoneCallDiscardReason, duration, nowUnix int) {
	r.markDiscardedWithSlugLocked(e, reason, "", duration, nowUnix)
}

func (r *registry) markDiscardedWithSlugLocked(e *entry, reason domain.PhoneCallDiscardReason, reasonSlug string, duration, nowUnix int) {
	if e.call.Terminal() {
		return
	}
	e.call.State = domain.PhoneCallStateDiscarded
	e.call.DiscardReason = reason
	e.call.DiscardReasonSlug = reasonSlug
	e.call.Duration = duration
	e.call.DiscardedAt = nowUnix
	r.decActiveLocked(e.call.AdminID)
}

// newID 生成 registry 内唯一的正 int64（调用方持有 r.mu）。
func (r *registry) newIDLocked() (int64, error) {
	for i := 0; i < 32; i++ {
		id, err := randomPositiveInt64()
		if err != nil {
			return 0, err
		}
		if _, exists := r.byID[id]; !exists {
			return id, nil
		}
	}
	return 0, fmt.Errorf("phone: exhausted call id attempts")
}

func randomPositiveInt64() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("phone: random id: %w", err)
	}
	v := int64(binary.BigEndian.Uint64(buf[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v, nil
}
