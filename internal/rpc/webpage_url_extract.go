package rpc

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"
	"golang.org/x/net/publicsuffix"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

// urlInTextRe 匹配原始文本里的带 scheme 链接（取到首个空白或尖括号/引号为止）。
// 是否接纳由 detectURLEntities 再按 http(s) 或当前 app-link 配置收口，不能把任意
// foo:// 都提升为服务端认证的可点击实体。
var urlInTextRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>"'）】]+`)

// bareURLInTextRe 匹配官方客户端会本地识别的裸域名 URL（例如 github.com/@alice）。
// 前导边界排除 email、已有 scheme URL 内部和域名中间；候选命中后仍由 publicsuffix
// 校验 TLD，避免把普通带点文本误升为链接。
var bareURLInTextRe = regexp.MustCompile(`(?i)(^|[^a-z0-9_@./:+-])((?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:[a-z]{2,63}|xn--[a-z0-9-]{2,59})(?::[0-9]{1,5})?(?:[/?#][^\s<>"'）】]*)?)`)

// urlTrailingPunct 是不属于 URL 的句末标点（'/' 是合法路径末尾，保留）。
const urlTrailingPunct = ".,;:!?)]}'\"。，、！？"

type byteSpan struct {
	start  int
	end    int
	scheme string
}

func rawURLByteSpans(message string) []byteSpan {
	if !strings.Contains(message, "://") && !strings.Contains(message, ".") {
		return nil
	}
	var out []byteSpan
	if strings.Contains(message, "://") {
		for _, loc := range urlInTextRe.FindAllStringIndex(message, -1) {
			raw := strings.TrimRight(message[loc[0]:loc[1]], urlTrailingPunct)
			if raw == "" {
				continue
			}
			schemeEnd := strings.Index(raw, "://")
			if schemeEnd <= 0 {
				continue
			}
			out = append(out, byteSpan{
				start:  loc[0],
				end:    loc[0] + len(raw),
				scheme: strings.ToLower(raw[:schemeEnd]),
			})
		}
	}
	if strings.Contains(message, ".") {
		for _, loc := range bareURLInTextRe.FindAllStringSubmatchIndex(message, -1) {
			if len(loc) < 6 || loc[4] < 0 || loc[5] <= loc[4] {
				continue
			}
			start, end := loc[4], loc[5]
			raw := strings.TrimRight(message[start:end], urlTrailingPunct)
			end = start + len(raw)
			if raw == "" || overlapsByteSpan(out, start, end) || !bareURLCandidateValid(raw) {
				continue
			}
			out = append(out, byteSpan{start: start, end: end})
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].start == out[j].start {
			return out[i].end < out[j].end
		}
		return out[i].start < out[j].start
	})
	return out
}

func overlapsByteSpan(spans []byteSpan, start, end int) bool {
	for _, span := range spans {
		if start < span.end && span.start < end {
			return true
		}
	}
	return false
}

// detectURLEntities 服务端扫描消息文本生成 url 高亮实体（MessageEntityURL）。裸域名
// URL（github.com/@alice）按官方客户端行为接纳；带 scheme URL 除 http(s) 外，仅接受当前
// Router 配置允许的 app-link scheme/host。偏移/长度按 UTF-16 码元（Telegram 实体口径）。
// 自定义 scheme 只参与 entity，不改变网页预览的 http(s) 边界。
func detectURLEntities(message string, appLinks links.AppLinkBuilder) []tg.MessageEntityClass {
	spans := rawURLByteSpans(message)
	if len(spans) == 0 {
		return nil
	}
	out := make([]tg.MessageEntityClass, 0, len(spans))
	for _, span := range spans {
		raw := message[span.start:span.end]
		if span.scheme != "" && span.scheme != "http" && span.scheme != "https" && !appLinks.AcceptsEntityURL(raw) {
			continue
		}
		out = append(out, &tg.MessageEntityURL{
			Offset: utf16CodeUnitLen(message[:span.start]),
			Length: utf16CodeUnitLen(raw),
		})
	}
	return out
}

func bareURLCandidateValid(raw string) bool {
	if raw == "" || strings.Contains(raw, "://") || strings.ContainsAny(raw, " \t\r\n<>\"'") {
		return false
	}
	hostPort := raw
	if cut := strings.IndexAny(hostPort, "/?#"); cut >= 0 {
		hostPort = hostPort[:cut]
	}
	if hostPort == "" || strings.Contains(hostPort, "@") {
		return false
	}
	host := hostPort
	if h, port, ok := splitBareHostPort(hostPort); ok {
		host = h
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return false
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !bareDomainHostValid(host) {
		return false
	}
	return bareDomainTLDValid(host)
}

func splitBareHostPort(hostPort string) (string, string, bool) {
	idx := strings.LastIndexByte(hostPort, ':')
	if idx < 0 {
		return hostPort, "", false
	}
	return hostPort[:idx], hostPort[idx+1:], true
}

func bareDomainHostValid(host string) bool {
	if host == "" || strings.Contains(host, ":") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func bareDomainTLDValid(host string) bool {
	tld := host[strings.LastIndexByte(host, '.')+1:]
	suffix, icann := publicsuffix.PublicSuffix("x." + tld)
	return icann && suffix == tld
}

// firstPreviewableURL 从消息文本+实体中提取首个可预览链接，用于链接预览；裸域名会按
// https:// 规范化：
//   - MessageEntityTextURL：URL 直接在实体里（markdown 风格 [text](url)）。
//   - MessageEntityURL：URL 是文本里 [offset,offset+length) 的子串（Telegram 实体偏移以
//     UTF-16 码元计，需按 UTF-16 切片，不能按 rune/byte）。
//   - 回退：实体里没有 URL 时扫描原始文本。DrKLO 发送时带 url 实体，但 TDesktop 等客户端
//     不带、依赖服务端检测原始文本里的链接（与官方服务端行为一致），故必须扫文本兜底。
//
// 取出现顺序的第一个合法链接（与官方"预览第一条链接"一致）。无可预览链接返回 ok=false。
func firstPreviewableURL(message string, entities []tg.MessageEntityClass) (string, bool) {
	var units []uint16 // 惰性：仅遇到 MessageEntityURL（需按 UTF-16 偏移切片）时才编码全文。
	for _, entity := range entities {
		var candidate string
		switch e := entity.(type) {
		case *tg.MessageEntityTextURL:
			candidate = e.URL // URL 内联，无需切文本。
		case *tg.MessageEntityURL:
			if units == nil {
				units = utf16.Encode([]rune(message))
			}
			candidate = sliceUTF16(units, e.Offset, e.Length)
		default:
			continue
		}
		if normalized, ok := normalizePreviewURLCandidate(candidate); ok {
			return normalized, true
		}
	}
	// 回退扫原始文本：绝大多数消息无链接，无 URL 触发字符则直接跳过正则与分配。
	// firstURLInText 会继续限定为 http(s)/裸域名，自定义 app-link 永不进入网页预览。
	if !strings.Contains(message, "://") && !strings.Contains(message, ".") {
		return "", false
	}
	if raw, ok := firstURLInText(message); ok {
		return raw, true
	}
	return "", false
}

func normalizePreviewURLCandidate(candidate string) (string, bool) {
	candidate = strings.TrimRight(strings.TrimSpace(candidate), urlTrailingPunct)
	if normalized, ok := domain.NormalizeWebPageURL(candidate); ok {
		return normalized, true
	}
	if !bareURLCandidateValid(candidate) {
		return "", false
	}
	return domain.NormalizeWebPageURL("https://" + candidate)
}

// firstURLInText 扫描原始文本里的首个可预览 http(s)/裸域名链接，剥掉句末标点。
func firstURLInText(message string) (string, bool) {
	for _, span := range rawURLByteSpans(message) {
		raw := message[span.start:span.end]
		if normalized, ok := normalizePreviewURLCandidate(raw); ok {
			return normalized, true
		}
	}
	return "", false
}

// sliceUTF16 按 UTF-16 码元偏移/长度从已编码序列取子串；越界返回空串。
func sliceUTF16(units []uint16, offset, length int) string {
	if offset < 0 || length <= 0 || offset > len(units) || length > len(units)-offset {
		return ""
	}
	return strings.TrimSpace(string(utf16.Decode(units[offset : offset+length])))
}

// webPagePendingOrCachedMedia 为发送构造链接预览媒体：
//   - 若该 URL 已解析缓存（典型：客户端输入时 getWebPagePreview 已触发解析），直接挂 done
//     卡片——发送 echo 即带卡，不依赖异步 updateWebPage 换卡（官方行为；TDesktop 对自己
//     发出的消息不应用该换卡，故必须在 echo 直接带 done）。
//   - 否则挂 pending 占位，由异步 resolver 解析后经 updateWebPage 换卡。
//
// 未启用预览或 URL 不可规范化返回 nil（发送降级为无预览，不报错）。
func (r *Router) webPagePendingOrCachedMedia(ctx context.Context, rawURL string, invertMedia, forceLarge, forceSmall bool) *domain.MessageMedia {
	if page, ok := r.resolveInternalWebPage(ctx, rawURL); ok {
		page.ForceLargeMedia = forceLarge
		page.ForceSmallMedia = forceSmall
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
	}
	if page, ok := r.resolveUniqueGiftWebPage(ctx, rawURL); ok {
		page.ForceLargeMedia = forceLarge
		page.ForceSmallMedia = forceSmall
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
	}
	if r.deps.Files == nil || !r.deps.Files.WebPagePreviewEnabled() {
		return nil
	}
	normalized, ok := normalizePreviewURLCandidate(rawURL)
	if !ok {
		return nil
	}
	if page, found := r.deps.Files.LookupWebPage(ctx, normalized); found {
		if page.State == domain.MessageWebPageStateEmpty {
			return nil
		}
		if page.State == domain.MessageWebPageStateDone {
			page.ForceLargeMedia = forceLarge
			page.ForceSmallMedia = forceSmall
			return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
		}
	}
	return &domain.MessageMedia{
		Kind:        domain.MessageMediaKindWebPage,
		InvertMedia: invertMedia,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending,
			ID:    domain.WebPageURLHash(normalized),
			URL:   normalized,
			// date 是客户端重新拉取 pending 消息的绝对截止时间。TDesktop/DrKLO 在
			// date<=now 时立即重取；若 resolver 此时仍运行，TDesktop 会把占位记成 sticky
			// failed，后到的 done update 也不会重新显示卡片。
			Date:            int(r.clock.Now().Add(webPagePendingLifetime).Unix()),
			ForceLargeMedia: forceLarge,
			ForceSmallMedia: forceSmall,
		},
	}
}

// webPageMediaFromText 为文本发送构造链接预览媒体：no_webpage 抑制；否则取首个可预览 URL。
func (r *Router) webPageMediaFromText(ctx context.Context, message string, entities []tg.MessageEntityClass, noWebpage, invertMedia bool) *domain.MessageMedia {
	if noWebpage {
		return nil
	}
	rawURL, ok := firstPreviewableURL(message, entities)
	if !ok {
		return nil
	}
	return r.webPagePendingOrCachedMedia(ctx, rawURL, invertMedia, false, false)
}
