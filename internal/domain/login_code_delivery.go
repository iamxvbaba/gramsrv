package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// loginCodeTemplateCodePlaceholder 标记登录码消息模板中被替换为真实验证码的位置。
// 模板正文不再是旧的硬编码 "Login code: %s"，因此验证码的 bold MessageEntity
// 偏移/长度必须按占位符实际落点动态计算（见 OfficialLoginCodeMessage）。
// 任何进入 OfficialLoginCodeMessage 的模板都必须恰好包含一次该占位符
// （见 ValidateLoginCodeMessageTemplate）：零次会静默丢失验证码，两次及以上则
// 无法判定哪一处才是「真正的验证码」。
const loginCodeTemplateCodePlaceholder = "{{code}}"

// DefaultLoginCodeMessageTemplate 是 777000 登录码投递消息的内置最终兜底文案。
// 所有登录码（短信/邮箱）共用同一模板，不随投递渠道变化。支持 {{server_name}}
// 占位符（见 RenderWelcomeMessageTemplate），且必须恰好包含一次 {{code}}
// （见 ValidateLoginCodeMessageTemplate）。
const DefaultLoginCodeMessageTemplate = `Login code: {{code}}. Do not give this code to anyone, even if they say they are from {{server_name}}!

This code can be used to log in to your {{server_name}} account. We never ask it for anything else.

If you didn't request this code by trying to log in on another device, simply ignore this message.`

// ErrLoginCodeMessageTemplateMissingCode 表示候选登录码模板未恰好包含一次
// {{code}} 占位符。管理员 API 层必须直接拒绝这样的保存，而不是静默接受：
// 零次出现意味着真实验证码永远不会投递给用户。
var ErrLoginCodeMessageTemplateMissingCode = errors.New("login code message template must contain the {{code}} placeholder exactly once")

// ValidateLoginCodeMessageTemplate 要求 {{code}} 占位符恰好出现一次。
// 零次是功能性破坏（验证码本身永远不会到达用户），两次及以上是歧义
// （哪一处被替换并加粗？）——两者都直接拒绝，绝不静默修补。
func ValidateLoginCodeMessageTemplate(template string) error {
	if strings.Count(template, loginCodeTemplateCodePlaceholder) != 1 {
		return ErrLoginCodeMessageTemplateMissingCode
	}
	return nil
}

// ResolveLoginCodeMessageTemplate 按优先级挑选最终模板正文：显式管理面板覆盖
// （panelOverride，identity.Info 中按原样存储，空表示未配置）→ 显式环境变量默认值
// （envDefault，空表示未配置）→ 内置 DefaultLoginCodeMessageTemplate。纯函数，
// 便于不对 identity store / config 做依赖地单测其优先级。调用方（internal/app/auth）
// 每次投递前实时解析（绝不缓存），使管理面板的修改立即生效——与
// ResolveWelcomeMessageTemplate 的语义保持一致。登录码模板不按渠道分支。
//
// 本函数自身不校验 {{code}} 占位符——持久化覆盖值的调用方（管理 API）必须先
// 调用 ValidateLoginCodeMessageTemplate 再保存。OfficialLoginCodeMessage 会对
// 解析结果再次校验，作为对经由其他途径到达的非法值（手工编辑 identity.json、
// 越界环境变量）的纵深防御。
func ResolveLoginCodeMessageTemplate(panelOverride, envDefault string) string {
	if t := strings.TrimSpace(panelOverride); t != "" {
		return panelOverride
	}
	if t := strings.TrimSpace(envDefault); t != "" {
		return envDefault
	}
	return DefaultLoginCodeMessageTemplate
}

// LoginCodeDeliveryRequest describes one durable 777000 login-code delivery.
// PhoneCodeHash is an opaque idempotency token and must never be persisted in
// plaintext; store implementations persist only its SHA-256 digest.
type LoginCodeDeliveryRequest struct {
	UserID        int64
	PhoneCodeHash string
	Code          string
	// Template is the already-resolved login-code message template (see
	// ResolveLoginCodeMessageTemplate) -- resolving it requires the identity
	// store and config, both of which live above internal/store, so callers
	// (internal/app/auth) do that and pass the final template text in here,
	// the same division of responsibility OfficialWelcomeMessage's body
	// parameter uses. Empty falls back to DefaultLoginCodeMessageTemplate
	// (see OfficialLoginCodeMessage).
	Template string
	Date     int
	// ExpiresAt is the unix second after which the compact idempotency receipt
	// may be reclaimed. It must cover the corresponding code's usable lifetime.
	ExpiresAt int64
}

// LoginCodeDeliveryResult returns the immutable first delivery. Created is
// false when the same phone_code_hash was already committed and replayed.
type LoginCodeDeliveryResult struct {
	Message Message
	Created bool
}

// OfficialLoginCodeMessage builds the account-visible incoming message from
// Telegram's official notification account. Persistence assigns ID, UID and
// Pts atomically.
//
// template 先渲染（{{server_name}} 替换，再把 {{code}} 替换为真实验证码），
// 加粗 MessageEntity 按替换后 {{code}} 的实际落点动态定位——绝不假设固定前缀，
// 因为模板可由管理员编辑（见 ValidateLoginCodeMessageTemplate）。模板为空或
// 校验失败时回退到 DefaultLoginCodeMessageTemplate，绝不投递没有验证码的消息。
func OfficialLoginCodeMessage(userID int64, template, code string, date int) (Message, error) {
	return OfficialLoginCodeMessageWithTemplate(userID, template, code, date)
}

// SnapshotLoginCodeMessageTemplate resolves the brand and fallback at the first
// delivery. The returned template still contains {{code}}, so a compact
// receipt can reproduce the original body without storing the secret code.
func SnapshotLoginCodeMessageTemplate(template string) string {
	if strings.TrimSpace(template) == "" || ValidateLoginCodeMessageTemplate(template) != nil {
		template = DefaultLoginCodeMessageTemplate
	}
	rendered := RenderWelcomeMessageTemplate(template)
	if ValidateLoginCodeMessageTemplate(rendered) != nil {
		rendered = RenderWelcomeMessageTemplate(DefaultLoginCodeMessageTemplate)
		if ValidateLoginCodeMessageTemplate(rendered) != nil {
			return strings.ReplaceAll(DefaultLoginCodeMessageTemplate, "{{server_name}}", "ShuzaGram")
		}
	}
	return rendered
}

func OfficialLoginCodeMessageWithTemplate(userID int64, template, code string, date int) (Message, error) {
	if userID <= 0 || IsSystemUserID(userID) || strings.TrimSpace(code) == "" || len(code) > 64 || date < 0 || date > math.MaxInt32 {
		return Message{}, fmt.Errorf("%w: user=%d code_length=%d date=%d", ErrLoginCodeDeliveryInvalid, userID, len(code), date)
	}
	rendered := SnapshotLoginCodeMessageTemplate(template)
	idx := strings.Index(rendered, loginCodeTemplateCodePlaceholder)
	if idx < 0 {
		// 实际不可达：模板刚被校验过（或是内置默认）保证占位符恰好一次，
		// 而 {{server_name}} 替换不会移除或移动无关占位符。仍作防御，
		// 绝不投递静默丢失验证码的消息。
		rendered = DefaultLoginCodeMessageTemplate
		idx = strings.Index(rendered, loginCodeTemplateCodePlaceholder)
	}
	body := rendered[:idx] + code + rendered[idx+len(loginCodeTemplateCodePlaceholder):]
	return Message{
		OwnerUserID: userID,
		Peer:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		From:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		Date:        date,
		Body:        body,
		Entities: []MessageEntity{
			{Type: MessageEntityBold, Offset: automaticEntityUTF16Length(rendered[:idx]), Length: automaticEntityUTF16Length(code)},
		},
	}, nil
}
