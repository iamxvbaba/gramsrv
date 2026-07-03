package rpc

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// urlInTextRe 匹配原始文本里的 http(s) 链接（取到首个空白或尖括号/引号为止）。
var urlInTextRe = regexp.MustCompile(`https?://[^\s<>"'）】]+`)

// urlTrailingPunct 是不属于 URL 的句末标点（'/' 是合法路径末尾，保留）。
const urlTrailingPunct = ".,;:!?)]}'\"。，、！？"

// detectURLEntities 服务端扫描消息文本生成 url 高亮实体（MessageEntityURL）。TDesktop 等客户端
// 发消息不带 url 实体、依赖服务端检测原文（官方服务端行为），否则链接不高亮。偏移/长度按
// UTF-16 码元（Telegram 实体口径）。
func detectURLEntities(message string) []tg.MessageEntityClass {
	if !strings.Contains(message, "http") {
		return nil
	}
	locs := urlInTextRe.FindAllStringIndex(message, -1)
	if len(locs) == 0 {
		return nil
	}
	out := make([]tg.MessageEntityClass, 0, len(locs))
	for _, loc := range locs {
		raw := strings.TrimRight(message[loc[0]:loc[1]], urlTrailingPunct)
		if raw == "" {
			continue
		}
		out = append(out, &tg.MessageEntityURL{
			Offset: utf16CodeUnitLen(message[:loc[0]]),
			Length: utf16CodeUnitLen(raw),
		})
	}
	return out
}

// firstPreviewableURL 从消息文本+实体中提取首个可预览的 http/https 链接，用于链接预览：
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
		if normalized, ok := domain.NormalizeWebPageURL(candidate); ok {
			return normalized, true
		}
	}
	// 回退扫原始文本：绝大多数消息无链接，无 "http" 子串则直接跳过正则与分配。
	if !strings.Contains(message, "http") {
		return "", false
	}
	if raw, ok := firstURLInText(message); ok {
		if normalized, ok := domain.NormalizeWebPageURL(raw); ok {
			return normalized, true
		}
	}
	return "", false
}

// firstURLInText 扫描原始文本里的首个 http(s) 链接，剥掉句末标点。
func firstURLInText(message string) (string, bool) {
	match := urlInTextRe.FindString(message)
	if match == "" {
		return "", false
	}
	// 句末标点不属于 URL（"见 https://example.com。" / "...go)."）。'/' 是合法路径末尾，保留。
	match = strings.TrimRight(match, urlTrailingPunct)
	if match == "" {
		return "", false
	}
	return match, true
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
	if page, ok := r.resolveAIComposeStyleWebPage(ctx, rawURL); ok {
		page.ForceLargeMedia = forceLarge
		page.ForceSmallMedia = forceSmall
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
	}
	if r.deps.Files == nil || !r.deps.Files.WebPagePreviewEnabled() {
		return nil
	}
	normalized, ok := domain.NormalizeWebPageURL(rawURL)
	if !ok {
		return nil
	}
	if page, found := r.deps.Files.LookupWebPage(ctx, normalized); found && page.State == domain.MessageWebPageStateDone {
		page.ForceLargeMedia = forceLarge
		page.ForceSmallMedia = forceSmall
		return &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, InvertMedia: invertMedia, WebPage: &page}
	}
	return &domain.MessageMedia{
		Kind:        domain.MessageMediaKindWebPage,
		InvertMedia: invertMedia,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending,
			ID:    domain.WebPageURLHash(normalized),
			URL:   normalized,
			// Date=「processing started」时刻：留 0（=1970）会被严格客户端判为 pending 早已
			// 过期 → 直接显示纯文本，须填发送时刻。
			Date:            int(r.clock.Now().Unix()),
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
