// Package geoip 把会话 IP 解析为 account.getAuthorizations 展示用的国家/地区文案。
//
// 协议层没有 help.requestIpAddress 的对等实现,authorization.country / .region 过去
// 只能回传字面量 "Unknown"。本包是可选增强:未配置后端时 New 返回 nil Resolver,
// 调用方回落到原占位文案,默认行为与启用前完全一致。
//
// 运维方可以按顺序配置多个免费后端(见 Config.Endpoints),解析按顺序 failover:
// 上一个后端查不到或此刻不可用,自动落到下一个。实测 2026-09 各家都会时不时地不给
// 完整数据——reallyfreegeoip.org 连续几次请求就 429;geojs.io 只有 ASN 库覆盖,
// 1.1.1.1 那种地址压根没有 country 字段;hackmyip.com 会间歇性回 400;ipapi.is 对
// 保留网段回 is_bogon。单后端必然会让一部分会话列表显示 Unknown,多后端才是可用解。
//
// 每个后端有两条独立的健康状态(见 breaker):限流窗口长,不可达窗口短。上层按顺序
// 逐个后端尝试,打开的窗口直接跳过,一个请求都不发。
//
// 隐私约束:仅解析公网可路由地址。私网/回环/链路本地地址既没有地理归属,也不应
// 被送到第三方服务。
package geoip

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Location 是 IP 的地理归属展示文案。
//
// Country 非空才表示解析成功。Region 是更细一级的位置(城市 → 州/省 → 时区),
// 拿不到时留空,由调用方决定占位文案——本包不伪造地名。
type Location struct {
	Country string
	Region  string
}

// Resolver 批量解析 IP 到展示文案。
//
// 返回的 map 以调用方原样传入的字符串为 key,未命中的 IP 不出现在 map 里。调用方
// 用零值回落即可,不需要知道内部的 IP 归一化和 failover 规则。
type Resolver interface {
	Resolve(ctx context.Context, ips []string) map[string]Location
	// Close 释放后端持有的资源。Resolver 为 nil 时调用方不应调用。
	Close() error
}

// Config 描述 geoip 后端链。零值不合法,未启用请直接不要调用 New。
type Config struct {
	// Endpoints 是按顺序尝试的后端 URL 模板列表,第一个是主力。元素必须含 {ip}
	// 占位符,例如 "https://api.ipapi.is/?q={ip}"。空列表表示不启用地理归属。
	//
	// 顺序有意义:未解析的地址会依次交给后面的后端,主力越准,后面的后端就越少被
	// 问到,配额消耗也越低。
	Endpoints []string
	// Timeout 是单个 HTTP 请求的上限。免费后端正常延迟 0.6~1.3s,2s 留足抖动余量;
	// 超时按失败处理,计入该后端的不可达窗口。
	Timeout time.Duration
	// Concurrency 是单次 Resolve 内跨所有后端的在途请求总数上限。批量并发是必须的:
	// 串行查 N 台设备就是 N×延迟,客户端会直接感知到卡顿。
	Concurrency int
	// CacheTTL 是命中缓存的有效期。单个 IP 的地理归属几乎不变,默认给足一天。
	CacheTTL time.Duration
	// NegativeTTL 是"所有后端都没给出结果"(限流/超时/网络错误/查无此 IP)的负缓存期。
	// 必须远小于 CacheTTL,否则一次限流会把该 IP 锁死很久。
	NegativeTTL time.Duration
	// CacheSize 是缓存条目上限。
	CacheSize int
	// RateLimitThreshold 是连续限流多少次后打开该后端的限流窗口。
	RateLimitThreshold int
	// RateLimitCooldown 是限流窗口时长,期间该后端一个请求都不发。
	RateLimitCooldown time.Duration
	// DownThreshold 是连续多少次超时/网络错误后把该后端判定为不可达。
	DownThreshold int
	// DownCooldown 是不可达窗口时长。比限流窗口短得多:后端可能只是抖了一下。
	DownCooldown time.Duration
}

const (
	defaultTimeout            = 2 * time.Second
	defaultConcurrency        = 4
	defaultCacheTTL           = 24 * time.Hour
	defaultNegativeTTL        = 5 * time.Minute
	defaultCacheSize          = 4096
	defaultRateLimitThreshold = 3
	// 实测 2026-09:连续约 5 次请求后开始 429,静默约 60s 恢复。限流冷却取 2min——
	// 比实测窗口宽裕一倍,但不至于长到让用户看着 Unknown 干等。
	defaultRateLimitCooldown = 2 * time.Minute
	// 不可达只给 30s:一个后端挂掉不该让整条链离线几分钟,但每次都先撞满一遍
	// 超时预算也不行——那正是 failover 要消灭的卡顿。
	defaultDownThreshold = 2
	defaultDownCooldown  = 30 * time.Second
)

// New 按 cfg 构造解析器。
//
// Endpoints 为空时返回 (nil, nil)——那代表"没开地理归属",不是错误。调用方必须
// 容忍 nil Resolver,那正是未配置时的预期路径。
func New(cfg Config, log *zap.Logger) (Resolver, error) {
	if log == nil {
		log = zap.NewNop()
	}
	backends, err := newBackendChain(cfg, log)
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, nil
	}
	return &cachedResolver{
		backends:    backends,
		concurrency: positiveInt(cfg.Concurrency, defaultConcurrency),
		cache: newCache(cacheConfig{
			ttl:     positiveDuration(cfg.CacheTTL, defaultCacheTTL),
			negTTL:  positiveDuration(cfg.NegativeTTL, defaultNegativeTTL),
			maxSize: positiveInt(cfg.CacheSize, defaultCacheSize),
		}),
		log: log,
	}, nil
}

// newBackendChain 按配置顺序构造后端链,并去掉重复的端点。
//
// 去重是必要的:同一个端点配两次不会提高成功率,只会让一次会话列表多打一遍请求,
// 而免费后端的配额经不起这种浪费。
func newBackendChain(cfg Config, log *zap.Logger) ([]backend, error) {
	timeout := positiveDuration(cfg.Timeout, defaultTimeout)
	breakerCfg := breakerConfig{
		rateLimitThreshold: positiveInt(cfg.RateLimitThreshold, defaultRateLimitThreshold),
		rateLimitCooldown:  positiveDuration(cfg.RateLimitCooldown, defaultRateLimitCooldown),
		downThreshold:      positiveInt(cfg.DownThreshold, defaultDownThreshold),
		downCooldown:       positiveDuration(cfg.DownCooldown, defaultDownCooldown),
	}
	seen := make(map[string]struct{}, len(cfg.Endpoints))
	backends := make([]backend, 0, len(cfg.Endpoints))
	for _, endpoint := range cfg.Endpoints {
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}
		b, err := newHTTPBackend(httpBackendConfig{
			endpoint: endpoint,
			timeout:  timeout,
			breaker:  breakerCfg,
		}, log)
		if err != nil {
			return nil, fmt.Errorf("geoip endpoint %q: %w", endpoint, err)
		}
		backends = append(backends, b)
	}
	return backends, nil
}

// cachedResolver 是唯一对外的 Resolver 实现。它负责 IP 归一、缓存、后端 failover 和
// 熔断,让每个 backend 退化成"给我一个已归一的公网 IP,给我国家/地区"。
type cachedResolver struct {
	backends    []backend
	concurrency int
	cache       *cache
	log         *zap.Logger
}

// Resolve 批量解析一组 IP。
//
// nil receiver 直接返回 nil:未启用地理归属时调用方会把 nil Resolver 传下来,
// 这里必须能安全退化,而不是 panic。
func (r *cachedResolver) Resolve(ctx context.Context, ips []string) map[string]Location {
	if r == nil || len(ips) == 0 {
		return nil
	}
	// 归一后按唯一地址去重:一次会话列表里多台设备常常共享同一个出口 IP。
	want := make(map[netip.Addr]struct{}, len(ips))
	for _, ip := range ips {
		if addr, ok := publicAddr(ip); ok {
			want[addr] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}

	// 先查缓存,只把真正缺的送后端。限流很凶,一次本可命中缓存的出网就足以
	// 拖慢整批并可能换来 429。
	pending := make([]netip.Addr, 0, len(want))
	hit := make(map[netip.Addr]Location, len(want))
	for addr := range want {
		entry, found := r.cache.get(addr)
		switch {
		case !found:
			pending = append(pending, addr)
		case entry.resolved:
			hit[addr] = entry.location
		}
	}

	if len(pending) > 0 {
		r.resolvePending(ctx, pending, hit)
	}
	return expandByInput(hit, ips)
}

// resolvePending 沿后端链依次尝试,结果与负缓存一起落盘。
func (r *cachedResolver) resolvePending(ctx context.Context, pending []netip.Addr, hit map[netip.Addr]Location) {
	for _, b := range r.backends {
		remaining := unresolved(pending, hit)
		if len(remaining) == 0 {
			return
		}
		if state := b.Breaker().state(); state.Open {
			// 熔断打开时整批跳过,不产生任何出网请求。熔断的全部意义就在这里:继续打
			// 只会把 429/故障窗口一直续着,同时把每个请求的延迟预算也搭进去。
			r.log.Debug("geoip 解析跳过：后端暂不可用",
				zap.String("backend", b.Name()),
				zap.String("reason", state.Reason),
				zap.Duration("remaining", state.Remaining),
				zap.Int("skipped_ips", len(remaining)))
			continue
		}
		r.resolveWithBackend(ctx, b, remaining, hit)
	}
	// 所有后端都试过了还没结果才记负缓存:某个后端的缺失不能连带把整条链判死,
	// 否则免费库的覆盖差异会让一批地址长期显示 Unknown。
	for _, addr := range pending {
		if _, ok := hit[addr]; !ok {
			r.cache.putMiss(addr)
		}
	}
}

// resolveWithBackend 用一个后端并发解析一批地址。
//
// 半途出现硬失败(超时/传输错误/5xx)就停止发新请求并让位给下一个后端:后端正在坏掉
// 时,把它剩下的地址一个一个撞满超时预算只会拖慢整批,那正是 failover 要消灭的卡顿。
// 明确答复"没这条 IP"不触发熔断,那只是让下一个后端继续试的机会。
func (r *cachedResolver) resolveWithBackend(ctx context.Context, b backend, addrs []netip.Addr, hit map[netip.Addr]Location) {
	sem := make(chan struct{}, r.concurrency)
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		healthy atomic.Bool
	)
	healthy.Store(true)
	for _, addr := range addrs {
		if !healthy.Load() {
			break
		}
		// 信号量在发送方获取而不是在 goroutine 里:这样本循环自己就会阻塞,天然形成
		// 并发上限,不必再让每个 IP 都起一个 goroutine 去抢。
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(addr netip.Addr) {
			defer func() {
				<-sem
				wg.Done()
			}()
			loc, status := b.Lookup(ctx, addr)
			b.Breaker().record(status)
			switch status {
			case statusOK:
				mu.Lock()
				hit[addr] = loc
				mu.Unlock()
				r.cache.put(addr, loc)
			case statusNotFound:
				// 后端活着但没这条数据,继续问下一个后端。
			case statusRateLimited:
				// 熔断窗口由 breaker 维护,本批剩下的地址继续打没有意义,直接让位。
				healthy.Store(false)
			default:
				healthy.Store(false)
			}
		}(addr)
	}
	wg.Wait()
}

// Close 释放后端资源。HTTP 后端没有长期资源,这里只是不让接口变得不对称。
func (r *cachedResolver) Close() error { return nil }

// unresolved 返回还没拿到结果的地址。
func unresolved(pending []netip.Addr, hit map[netip.Addr]Location) []netip.Addr {
	out := make([]netip.Addr, 0, len(pending))
	for _, addr := range pending {
		if _, ok := hit[addr]; !ok {
			out = append(out, addr)
		}
	}
	return out
}

// expandByInput 把按归一地址索引的结果重新映射到调用方传入的原始字符串。
// 调用方直接用 authorization.ip 索引即可,不必知道归一规则。
func expandByInput(hit map[netip.Addr]Location, ips []string) map[string]Location {
	if len(hit) == 0 {
		return nil
	}
	out := make(map[string]Location, len(hit))
	for _, ip := range ips {
		addr, ok := publicAddr(ip)
		if !ok {
			continue
		}
		if loc, ok := hit[addr]; ok {
			out[ip] = loc
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func positiveInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
