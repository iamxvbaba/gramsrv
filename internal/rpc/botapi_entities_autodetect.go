package rpc

import (
	"telesrv/internal/domain"
)

// Bot API 出站文本的自动实体补全。
//
// 官方服务端对所有出站消息都做词法实体检测(@mention / #hashtag / $cashtag /
// bot command / URL)，与消息是用户发的还是 bot 发的无关。Bot API 的
// parse_mode/entities 只描述 bot 的显式富文本意图，不会产生这些实体；而客户端
// (TDesktop 的 ItemTextBotDefaultOptions、TWeb 的 wrapRichText)只渲染服务端下发的
// messageEntityBotCommand，缺失即不蓝显——所以 grammystore 之类通过
// POST /bot<token>/sendMessage 发出的 /buy /start 必须由服务端补齐，
// 与内置 bot 走 serviceBotReplyEntities 的效果一致。
//
// 与 MTProto 侧 augmentAutoEntities 的差别只在实体容器：Bot API 边界已经持有
// domain entity，无需绕 tg 转换。检测规则本身复用同一套 domain/rpc 检测器，
// 因此两条路径产出的实体完全一致。

// augmentBotAPIAutoEntities 在 Bot API 已解析实体基础上补充服务端检测的自动实体。
// bot 显式实体优先保留并排在前面；补充项与任何已有区间相交时丢弃，避免把
// mention/hashtag/command 打进 code/pre/text_url 内部。URL 跨度同样计入排除区，
// 使 https://t.me/@name 里的 @name 不会变成 mention 实体。
func (r *Router) augmentBotAPIAutoEntities(message string, entities []domain.MessageEntity) []domain.MessageEntity {
	if message == "" || len(entities) >= domain.MaxMessageEntityCount {
		return entities
	}
	type interval struct{ start, end int }
	occupied := make([]interval, 0, len(entities)+8)
	for _, entity := range entities {
		if entity.Length > 0 && entity.Offset >= 0 {
			occupied = append(occupied, interval{start: entity.Offset, end: entity.Offset + entity.Length})
		}
	}
	overlaps := func(start, end int) bool {
		for _, iv := range occupied {
			if start < iv.end && iv.start < end {
				return true
			}
		}
		return false
	}

	var extra []domain.MessageEntity
	accept := func(entity domain.MessageEntity) {
		if entity.Length <= 0 || len(entities)+len(extra) >= domain.MaxMessageEntityCount {
			return
		}
		if overlaps(entity.Offset, entity.Offset+entity.Length) {
			return
		}
		extra = append(extra, entity)
		occupied = append(occupied, interval{start: entity.Offset, end: entity.Offset + entity.Length})
	}

	// URL 先补：既是官方顺序，也让后续词法检测避开链接内部。
	for _, u := range detectURLEntities(message, r.appLinks) {
		accept(domain.MessageEntity{Type: domain.MessageEntityURL, Offset: u.GetOffset(), Length: u.GetLength()})
	}

	spans := make([]domain.MessageEntitySpan, 0, len(occupied))
	for _, iv := range occupied {
		spans = append(spans, domain.MessageEntitySpan{Offset: iv.start, Length: iv.end - iv.start})
	}
	for _, entity := range domain.DetectAutomaticMessageEntities(message, spans) {
		accept(entity)
	}

	if len(extra) == 0 {
		return entities
	}
	out := make([]domain.MessageEntity, 0, len(entities)+len(extra))
	out = append(out, entities...)
	return append(out, extra...)
}
