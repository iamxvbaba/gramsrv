package rpc

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
)

// 服务端自动实体检测：@mention / #hashtag / $cashtag / bot command。
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
// 客户端已带 url/textUrl 实体(自行检测过,如 DrKLO)时跳过 url 检测,沿用既有口径。
// 客户端实体保持在前(超过上限裁剪时优先保留),结果裁剪到实体上限。
func augmentAutoEntities(message string, entities []tg.MessageEntityClass) []tg.MessageEntityClass {
	// 快路径:绝大多数消息不含任何可自动识别的触发字符。单次 ContainsAny 扫描即短路返回,
	// 跳过下面各检测器对全文的扫描与区间分配(纯文本发送零额外开销)。所有 http(s) 链接
	// 都含 '/',故 "@#$/" 一并覆盖 url 检测;email/phone 未实现故不在触发集内。
	if message == "" || !strings.ContainsAny(message, "@#$/") {
		return entities
	}
	hasClientURL := false
	type interval struct{ start, end int }
	occupied := make([]interval, 0, len(entities)+8)
	for _, e := range entities {
		if ln := e.GetLength(); ln > 0 {
			off := e.GetOffset()
			occupied = append(occupied, interval{off, off + ln})
		}
		switch e.(type) {
		case *tg.MessageEntityURL, *tg.MessageEntityTextURL:
			hasClientURL = true
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
	// (如 https://t.me/@scam 的 @scam,既不符官方语义也是钓鱼风险);但仅在客户端未带任何
	// url/textUrl 实体时才作为实体下发,沿用 all-or-nothing(DrKLO 一带即全带;TDesktop 不带、依赖服务端)。
	for _, u := range detectURLEntities(message) {
		ln := u.GetLength()
		if ln <= 0 {
			continue
		}
		off := u.GetOffset()
		if overlaps(off, off+ln) {
			continue
		}
		occupied = append(occupied, interval{off, off + ln})
		if !hasClientURL && len(entities)+len(extra) < maxMessageEntityCount {
			extra = append(extra, u)
		}
	}

	// 其余自动实体落在任何已占区间(客户端实体或 URL 跨度)内则丢弃;客户端实体优先保留(上限内)。
	for _, c := range detectMentionEntities(message) {
		accept(c)
	}
	for _, c := range detectHashtagEntities(message) {
		accept(c)
	}
	for _, c := range detectCashtagEntities(message) {
		accept(c)
	}
	for _, c := range detectBotCommandEntities(message) {
		accept(c)
	}

	if len(extra) == 0 {
		return entities
	}
	out := make([]tg.MessageEntityClass, 0, len(entities)+len(extra))
	out = append(out, entities...)
	return append(out, extra...)
}

// isWordRune 判定「单词字符」(用于实体前导边界:前一个字符是单词字符时不是新实体起点,
// 借此排除 email 的 local@domain、路径里的 and/or 等)。
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isHashtagRune 是 hashtag 正文允许的字符(支持 unicode 字母/数字,如 #日本語)。
func isHashtagRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// prevRuneBefore 取字节位置 i 之前的一个完整 rune(供前导边界判定);i<=0 返回 ok=false
// (字符串起点视为合法实体边界)。
func prevRuneBefore(s string, i int) (rune, bool) {
	if i <= 0 || i > len(s) {
		return 0, false
	}
	r, size := utf8.DecodeLastRuneInString(s[:i])
	if size == 0 {
		return 0, false
	}
	return r, true
}

// detectMentionEntities 检测 @username(messageEntityMention,仅 offset/length 无 user_id)。
// 规则:前导字符非单词字符且非 '@';username = [A-Za-z0-9_] 长 1..32;'@' 计入长度。
// (设置 mentioned 标志是另一条独立链路 mentionedUserIDsFromMessage,基于 user_id,与本检测无关。)
func detectMentionEntities(message string) []tg.MessageEntityClass {
	var out []tg.MessageEntityClass
	for i := 0; i < len(message); i++ {
		if message[i] != '@' {
			continue
		}
		if r, ok := prevRuneBefore(message, i); ok && (isWordRune(r) || r == '@') {
			continue
		}
		j := i + 1
		for j < len(message) && isUsernameByte(message[j]) {
			j++
		}
		if n := j - i - 1; n < 1 || n > 32 {
			continue
		}
		out = append(out, &tg.MessageEntityMention{
			Offset: utf16CodeUnitLen(message[:i]),
			Length: utf16CodeUnitLen(message[i:j]),
		})
		i = j - 1
	}
	return out
}

// detectBotCommandEntities 检测 /command 与 /command@botusername(messageEntityBotCommand)。
// 规则:前导字符非单词字符且非 '/','@','<';command = [A-Za-z0-9_] 长 1..64;可选
// '@' + [A-Za-z0-9_] 1..32 的 bot username 后缀。前导排除单词字符使日期 12/25、and/or、
// url 路径不被误判为命令。
func detectBotCommandEntities(message string) []tg.MessageEntityClass {
	var out []tg.MessageEntityClass
	for i := 0; i < len(message); i++ {
		if message[i] != '/' {
			continue
		}
		if r, ok := prevRuneBefore(message, i); ok && (isWordRune(r) || r == '/' || r == '@' || r == '<') {
			continue
		}
		j := i + 1
		for j < len(message) && isUsernameByte(message[j]) {
			j++
		}
		if n := j - i - 1; n < 1 || n > 64 {
			continue
		}
		end := j
		if end < len(message) && message[end] == '@' {
			k := end + 1
			for k < len(message) && isUsernameByte(message[k]) {
				k++
			}
			if bn := k - end - 1; bn >= 1 && bn <= 32 {
				end = k
			}
		}
		out = append(out, &tg.MessageEntityBotCommand{
			Offset: utf16CodeUnitLen(message[:i]),
			Length: utf16CodeUnitLen(message[i:end]),
		})
		i = end - 1
	}
	return out
}

// detectHashtagEntities 检测 #hashtag(messageEntityHashtag,支持 unicode 字母/数字)。
// 规则:前导字符非单词字符且非 '#','@';正文 1..256 个 hashtag 字符且首字符非数字
// (排除 #123 这类纯/前导数字串)。
func detectHashtagEntities(message string) []tg.MessageEntityClass {
	var out []tg.MessageEntityClass
	// '#' 是 ASCII,绝不出现在多字节 UTF-8 序列内部,故按字节扫描触发字符(避免对每个
	// 位置做 rune 解码);仅在边界判定与 body(支持 unicode 字母/数字)上才做 rune 解码。
	for i := 0; i < len(message); i++ {
		if message[i] != '#' {
			continue
		}
		if r, ok := prevRuneBefore(message, i); ok && (isWordRune(r) || r == '#' || r == '@') {
			continue
		}
		j := i + 1
		var firstRune rune
		runeCount := 0
		for j < len(message) {
			r, size := utf8.DecodeRuneInString(message[j:])
			if size <= 0 || !isHashtagRune(r) {
				break
			}
			if runeCount == 0 {
				firstRune = r
			}
			runeCount++
			j += size
		}
		if runeCount >= 1 && runeCount <= 256 && !unicode.IsDigit(firstRune) {
			out = append(out, &tg.MessageEntityHashtag{
				Offset: utf16CodeUnitLen(message[:i]),
				Length: utf16CodeUnitLen(message[i:j]),
			})
			i = j - 1 // for 循环 i++ 后落到 j,跳过已消费的 hashtag body
		}
	}
	return out
}

// detectCashtagEntities 检测 $TICKER(messageEntityCashtag)。规则:前导字符非单词字符
// 且非 '$';正文 1..8 个大写字母,且其后紧邻字符非单词字符(排除 $USDfoo)。
func detectCashtagEntities(message string) []tg.MessageEntityClass {
	var out []tg.MessageEntityClass
	for i := 0; i < len(message); i++ {
		if message[i] != '$' {
			continue
		}
		if r, ok := prevRuneBefore(message, i); ok && (isWordRune(r) || r == '$') {
			continue
		}
		j := i + 1
		for j < len(message) && message[j] >= 'A' && message[j] <= 'Z' {
			j++
		}
		if n := j - i - 1; n < 1 || n > 8 {
			continue
		}
		if r, size := utf8.DecodeRuneInString(message[j:]); size > 0 && isWordRune(r) {
			continue
		}
		out = append(out, &tg.MessageEntityCashtag{
			Offset: utf16CodeUnitLen(message[:i]),
			Length: utf16CodeUnitLen(message[i:j]),
		})
		i = j - 1
	}
	return out
}
