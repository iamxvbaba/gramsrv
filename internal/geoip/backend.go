package geoip

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// maxResponseBytes 限制读取的响应体大小。正常响应不到 1KB,这个上限只用来防止被劫持
// 或改写的端点把内存吃光。
const maxResponseBytes = 64 << 10

// lookupStatus 是一次后端查询的结果分类。分类有两个用途:让熔断器知道什么情况值得
// 停下来,以及让上层知道该不该继续问下一个后端。
type lookupStatus int

const (
	// statusOK 是"成功拿到了可用的国家/地区"。空结果不算 OK。
	statusOK lookupStatus = iota
	// statusNotFound 是"后端明确没有这个 IP 的可用数据"。重试同一个后端没意义,
	// 但继续问下一个后端有意义——免费库的覆盖范围互相不重合。
	statusNotFound
	// statusRateLimited 是 HTTP 429。只有它会推高限流窗口。
	statusRateLimited
	// statusTimeout 是本地上下文/请求超时。
	statusTimeout
	// statusFailed 是其它网络/协议错误,包括后端回 5xx。
	statusFailed
)

// parseOutcome 是解析一个响应体得到的结果。
type parseOutcome int

const (
	// parseOK 表示响应里有可用的国家名。
	parseOK parseOutcome = iota
	// parseNotFound 表示响应是完整的,但没有可用的国家名(数据缺失、is_bogon、404)。
	parseNotFound
	// parseFailed 表示响应本身没法用(坏 JSON、后端自报错误),应当计入不可达窗口。
	parseFailed
)

// responseParser 把一个后端的响应体翻译成展示文案。
type responseParser func(body []byte) (Location, parseOutcome)

// backend 是一个地理后端。所有实现都是 HTTP + JSON,差别只在 URL 形态和响应字段。
type backend interface {
	// Name 是给日志用的短标识(一般是主机名)。
	Name() string
	// Endpoint 是完整模板,只在日志里出现。
	Endpoint() string
	// Lookup 查询单个已归一、确认公网可达的地址。
	Lookup(ctx context.Context, addr netip.Addr) (Location, lookupStatus)
	// Breaker 返回该后端自己的健康状态机。多个后端各有一份,互不影响。
	Breaker() *breaker
}

// httpBackend 是所有后端共用的 HTTP 实现。
//
// 端点做成配置而不是写死,是为了在对方改路径、换镜像或换供应商时只改环境变量;
// 但请求形态(GET)和响应字段由 parser 固定。
type httpBackend struct {
	name     string
	endpoint string
	client   *http.Client
	parse    responseParser
	brk      *breaker
	log      *zap.Logger
}

type httpBackendConfig struct {
	endpoint string
	timeout  time.Duration
	parser   responseParser
	breaker  breakerConfig
}

// newHTTPBackend 按端点 URL 推断供应商并构造后端。
//
// 按主机名而不是按显式 kind 推断,是为了让运维只填一串 URL:同一套
// TELESRV_GEOIP_ENDPOINTS 对四个免费服务都成立。识别不出的主机(自建的 MaxMind 代理等)
// 走通用解析,尽量从常见字段名里取。
func newHTTPBackend(cfg httpBackendConfig, log *zap.Logger) (backend, error) {
	endpoint := strings.TrimSpace(cfg.endpoint)
	if !strings.Contains(endpoint, "{ip}") {
		return nil, errors.New("endpoint must contain the {ip} placeholder, got " + endpoint)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("endpoint is not a valid URL: " + err.Error())
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, errors.New("endpoint must use http or https, got " + parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("endpoint must include a host, got " + endpoint)
	}
	parser := parserForHost(parsed.Hostname())
	return &httpBackend{
		name:     parsed.Hostname(),
		endpoint: endpoint,
		client:   newGeoIPHTTPClient(cfg.timeout),
		parse:    parser,
		brk:      newBreaker(cfg.breaker),
		log:      log.Named("backend").Named(parsed.Hostname()),
	}, nil
}

func newGeoIPHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		// 单请求超时。不用 http.Client.Timeout 之外再套 context 是为了把
		// "我们的超时"和"调用方 ctx 取消"在日志里区分开。
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func (b *httpBackend) Name() string      { return b.name }
func (b *httpBackend) Endpoint() string  { return b.endpoint }
func (b *httpBackend) Breaker() *breaker { return b.brk }

// Lookup 查询单个地址,返回文案与结果分类。
func (b *httpBackend) Lookup(ctx context.Context, addr netip.Addr) (Location, lookupStatus) {
	ip := addr.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.requestURL(ip), nil)
	if err != nil {
		b.log.Error("geoip 请求构造失败", zap.String("ip", ip), zap.Error(err))
		return Location{}, statusFailed
	}
	req.Header.Set("accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		// 调用方 ctx 超时和本机网络抖动在这里汇合。运营最想先看到的是前者
		// (后端变慢拖住了 RPC),所以单独分类并按 warn 记。
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			b.log.Warn("geoip 请求超时",
				zap.String("ip", ip),
				zap.Duration("timeout", b.client.Timeout),
				zap.Error(err))
			return Location{}, statusTimeout
		}
		b.log.Warn("geoip 请求失败",
			zap.String("ip", ip),
			zap.Bool("caller_cancelled", errors.Is(err, context.Canceled)),
			zap.Error(err))
		return Location{}, statusFailed
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// 限流是最主要的失败模式,单独 warn 并把 Retry-After 带出来:对方如果给了冷却
		// 提示,比我们自己猜一个 5 分钟有用得多。
		b.log.Warn("geoip 命中上游限流",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.String("retry_after", resp.Header.Get("Retry-After")),
			zap.String("endpoint", b.endpoint))
		return Location{}, statusRateLimited
	case resp.StatusCode == http.StatusNotFound:
		// 明确的"没这条记录",继续问下一个后端。
		return Location{}, statusNotFound
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			b.log.Warn("geoip 响应读取失败", zap.String("ip", ip), zap.Error(err))
			return Location{}, statusFailed
		}
		return b.parseBody(ip, body)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// 4xx(除 404/429)是请求本身不被接受:HackMyIP 服务故障时实测回的就是
		// 400 + {"success":false}。这不算"没这条 IP",但也不该拖慢下一轮,让位即可。
		b.log.Warn("geoip 请求被上游拒绝",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.String("endpoint", b.endpoint))
		return Location{}, statusNotFound
	default:
		b.log.Warn("geoip 请求返回非预期状态码",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.String("endpoint", b.endpoint))
		return Location{}, statusFailed
	}
}

// parseBody 解析响应体并翻译成结果分类。
func (b *httpBackend) parseBody(ip string, body []byte) (Location, lookupStatus) {
	loc, outcome := b.parse(body)
	switch outcome {
	case parseOK:
		return loc, statusOK
	case parseNotFound:
		// 上游活着但没这条数据(实测 1.1.1.1 在 geojs 上就没有 country)。
		// 这不计入任何熔断计数,只把机会让给下一个后端。
		return Location{}, statusNotFound
	default:
		b.log.Warn("geoip 响应解析失败",
			zap.String("ip", ip),
			zap.String("endpoint", b.endpoint),
			zap.Int("bytes", len(body)),
			zap.String("body", truncateForLog(body)))
		return Location{}, statusFailed
	}
}

// logBodyLimit 是写进日志的响应体上限。后端坏掉时返回的往往是 HTML 错误页,不截断
// 会把整页刷进日志。
const logBodyLimit = 256

func truncateForLog(body []byte) string {
	if len(body) <= logBodyLimit {
		return string(body)
	}
	return string(body[:logBodyLimit]) + "…(truncated)"
}

// requestURL 把 {ip} 换成归一后的地址。地址经 publicAddr 校验过,一定是合法 IP,
// 但仍然走 url.PathEscape——占位符可能被放进 path segment,直接拼字符串会在遇到
// 意外输入时让 URL 结构失控。
func (b *httpBackend) requestURL(ip string) string {
	return strings.ReplaceAll(b.endpoint, "{ip}", url.PathEscape(ip))
}

// parserForHost 按主机名挑解析器。识别不出的主机走通用解析。
func parserForHost(host string) responseParser {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case host == "reallyfreegeoip.org" || strings.HasSuffix(host, ".reallyfreegeoip.org"):
		return parseReallyFreeGeoIP
	case host == "geojs.io" || strings.HasSuffix(host, ".geojs.io"):
		return parseGeoJS
	case host == "hackmyip.com" || strings.HasSuffix(host, ".hackmyip.com"):
		return parseHackMyIP
	case host == "ipapi.is" || strings.HasSuffix(host, ".ipapi.is"):
		return parseIPAPIS
	default:
		return parseGenericJSON
	}
}
