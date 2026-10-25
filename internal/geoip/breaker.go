package geoip

import (
	"sync"
	"time"
)

// breaker 是单个地理后端的健康状态机。
//
// 免费地理后端都会限流,而且会以完全不同的方式坏掉:
//
//   - reallyfreegeoip.org 配额窗口内持续回 429(实测:连续约 5 次请求后开始 429,
//     静默约 60s 恢复)。
//   - geojs.io 只带 ASN 库,某些网段(实测 1.1.1.1)压根没有 country 字段——那是数据
//     缺失,不是故障,必须让位给下一个后端而不是熔断。
//   - hackmyip.com 会在服务端故障时直接回 400/5xx(实测 2026-09 间歇性 400)。
//   - 任何后端都可能 DNS 解析失败或连接超时。
//
// 所以这里有两条独立的状态:限流窗口长(冷却久,继续打只会把窗口一直续着),不可达
// 窗口短(一次网络抖动不该让后端离线太久)。两者打开时上层都整批跳过,不发任何请求。
type breaker struct {
	mu sync.Mutex
	// now 可注入,让测试能确定性地推进冷却,不必真等两分钟。
	now               func() time.Time
	rateLimitThreshod int
	rateLimitCooldown time.Duration
	downThreshold     int
	downCooldown      time.Duration
	// consecutiveRateLimited / consecutiveHardFailure 是自上次成功以来的连续计数。
	// 两类失败互斥:一次成功把两个都清零,一次 429 只影响前者,一次超时只影响后者。
	consecutiveRateLimited int
	consecutiveHardFailure int
	rateLimitUntil         time.Time
	downUntil              time.Time
}

type breakerConfig struct {
	rateLimitThreshold int
	rateLimitCooldown  time.Duration
	downThreshold      int
	downCooldown       time.Duration
}

func newBreaker(cfg breakerConfig) *breaker {
	return &breaker{
		now:               time.Now,
		rateLimitThreshod: positiveInt(cfg.rateLimitThreshold, defaultRateLimitThreshold),
		rateLimitCooldown: positiveDuration(cfg.rateLimitCooldown, defaultRateLimitCooldown),
		downThreshold:     positiveInt(cfg.downThreshold, defaultDownThreshold),
		downCooldown:      positiveDuration(cfg.downCooldown, defaultDownCooldown),
	}
}

// breakerState 描述当前为什么不能发请求。
type breakerState struct {
	// Open 表示该后端此刻应当被整批跳过。
	Open bool
	// Remaining 是当前生效窗口的剩余时间。
	Remaining time.Duration
	// Reason 是给日志用的短标签:rate_limit / down / ""。
	Reason string
}

// state 返回当前状态。打开时上层应整批跳过,一个请求都不发。
func (b *breaker) state() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stateLocked()
}

func (b *breaker) stateLocked() breakerState {
	now := b.now()
	// 窗口过期就顺带清零计数,给下一轮留出完整阈值。
	if !b.rateLimitUntil.IsZero() && !now.Before(b.rateLimitUntil) {
		b.rateLimitUntil = time.Time{}
		b.consecutiveRateLimited = 0
	}
	if !b.downUntil.IsZero() && !now.Before(b.downUntil) {
		b.downUntil = time.Time{}
		b.consecutiveHardFailure = 0
	}
	switch {
	case !b.rateLimitUntil.IsZero():
		return breakerState{Open: true, Remaining: b.rateLimitUntil.Sub(now), Reason: "rate_limit"}
	case !b.downUntil.IsZero():
		return breakerState{Open: true, Remaining: b.downUntil.Sub(now), Reason: "down"}
	default:
		return breakerState{}
	}
}

// record 记录一次查询结果。
//
// statusOK 把两个计数都清零:后端活着。statusNotFound 不动任何计数——那是后端给出的
// 确定答复(数据缺失),上层会继续问下一个后端,不该因此熔断。statusRateLimited 只推高
// 限流计数,超时和传输错误只推高不可达计数。
func (b *breaker) record(status lookupStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stateLocked().Open {
		// 已经在冷却里,延长的意义不大:冷却本身就是"这段时间别发请求"。
		return
	}
	switch status {
	case statusOK:
		b.consecutiveRateLimited = 0
		b.consecutiveHardFailure = 0
	case statusRateLimited:
		b.consecutiveHardFailure = 0
		b.consecutiveRateLimited++
		if b.consecutiveRateLimited >= b.rateLimitThreshod {
			b.rateLimitUntil = b.now().Add(b.rateLimitCooldown)
			b.consecutiveRateLimited = 0
		}
	case statusTimeout, statusFailed:
		b.consecutiveRateLimited = 0
		b.consecutiveHardFailure++
		if b.consecutiveHardFailure >= b.downThreshold {
			// 短窗口:后端可能只是抖了一下,不能因为一次网络故障就离线几分钟。
			b.downUntil = b.now().Add(b.downCooldown)
			b.consecutiveHardFailure = 0
		}
	}
}
