package rpc

import (
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/links"
)

// 服务端自动实体检测：@mention / #hashtag / $cashtag / bot command / 银行卡号。
//
// 官方 Telegram 服务端会对消息原文检测这些「可自动识别」实体并写入 message.entities；
// 客户端发 sendMessage 时只发「用户意图」类实体(bold/italic/code/textUrl/customEmoji/
// inputMessageEntityMentionName 等)以及客户端本地检测的 url。DrKLO 对正常已发送消息
// useManualParse=false,不本地 Linkify @username,完全依赖 message.entities 渲染 @mention
// 高亮——故服务端不补 messageEntityMention 时 @username 不渲染成可点击蓝色。url 检测见
// detectURLEntities(webpage_url_extract.go)。
//
// 所有 offset/length 以 UTF-16 码元计(Telegram 实体口径),复用 utf16CodeUnitLen;@/#/$ /
// 触发字符本身计入实体长度。检测纯按文本正则,不查库校验 username/command 是否真实存在
// (官方亦如此:messageEntityMention 不带 user_id,客户端点击时再 resolveUsername)。

// augmentAutoEntities 在客户端已发实体基础上补充服务端检测的自动实体。补充项与任何
// 已有实体(客户端富文本意图实体或先补入的自动实体)区间相交时丢弃,避免把 mention/
// hashtag 打进 code/pre/textUrl/已有 mentionName 内部或彼此重叠(对齐官方不重复打实体)。
// URL 按跨度逐条补缺，不能因客户端带了某一条 HTTP URL 就跳过同消息中客户端不认识的
// app-link。客户端实体保持在前(超过上限裁剪时优先保留),结果裁剪到实体上限。
func augmentAutoEntities(message string, entities []tg.MessageEntityClass, appLinks links.AppLinkBuilder) []tg.MessageEntityClass {
	// 快路径:绝大多数消息不含任何可自动识别的触发字符。单次 ContainsAny 扫描即短路返回,
	// 跳过下面各检测器对全文的扫描与区间分配(纯文本发送零额外开销)。裸域名 URL 只需
	// 一个 '.' 即可触发（如 github.com），进入检测后仍会做 TLD/边界校验；银行卡号要至少
	// 13 个数字才触发（TDLib 不识别收到消息里的卡号，官方由服务端下发 messageEntityBankCard）；
	// email/phone 未实现故不在触发集内。
	if message == "" || (!strings.ContainsAny(message, "@#$/.") && !domain.MayContainBankCardNumber(message)) {
		return entities
	}
	type interval struct{ start, end int }
	occupied := make([]interval, 0, len(entities)+8)
	for _, e := range entities {
		if ln := e.GetLength(); ln > 0 {
			off := e.GetOffset()
			occupied = append(occupied, interval{off, off + ln})
		}
	}
	overlaps := func(s, e int) bool {
		for _, iv := range occupied {
			if s < iv.end && iv.start < e {
				return true
			}
		}
		return false
	}

	// extra 延迟分配:仅当真正补到实体时才建切片并复制 entities。像 "邮箱 a@b.com" 这种有
	// 触发字符但补不出实体的情况(@ 前是单词字符 → 非 mention),保持零复制返回原 entities。
	var extra []tg.MessageEntityClass
	accept := func(c tg.MessageEntityClass) {
		if len(entities)+len(extra) >= maxMessageEntityCount {
			return
		}
		ln := c.GetLength()
		if ln <= 0 {
			return
		}
		off := c.GetOffset()
		if overlaps(off, off+ln) {
			return
		}
		extra = append(extra, c)
		occupied = append(occupied, interval{off, off + ln})
	}

	// URL 跨度始终计算并加入排除区(occupied),使 @mention/#hashtag 等不会落进 URL 路径内部
	// (如 https://t.me/@scam 的 @scam,既不符官方语义也是钓鱼风险)。逐跨度补缺可覆盖
	// “客户端带 HTTP entity、但不认识 telesrv://”的混合消息，同时仍避免重复实体。
	for _, u := range detectURLEntities(message, appLinks) {
		ln := u.GetLength()
		if ln <= 0 {
			continue
		}
		off := u.GetOffset()
		if overlaps(off, off+ln) {
			continue
		}
		occupied = append(occupied, interval{off, off + ln})
		if len(entities)+len(extra) < maxMessageEntityCount {
			extra = append(extra, u)
		}
	}

	// 其余自动实体由协议中立 detector 产生，RPC 边界只负责把 domain entity 投影为 TL。
	// 这样服务端生成的系统消息可复用同一 UTF-16/URL 排除规则，而 tg 类型仍不越过 RPC。
	spans := make([]domain.MessageEntitySpan, 0, len(occupied))
	for _, interval := range occupied {
		spans = append(spans, domain.MessageEntitySpan{Offset: interval.start, Length: interval.end - interval.start})
	}
	for _, c := range tgMessageEntities(domain.DetectAutomaticMessageEntities(message, spans)) {
		accept(c)
	}

	if len(extra) == 0 {
		return entities
	}
	out := make([]tg.MessageEntityClass, 0, len(entities)+len(extra))
	out = append(out, entities...)
	return append(out, extra...)
}

func (r *Router) augmentAutoEntities(message string, entities []tg.MessageEntityClass) []tg.MessageEntityClass {
	return augmentAutoEntities(message, entities, r.appLinks)
}
