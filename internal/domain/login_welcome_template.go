package domain

import "strings"

// LoginMethod 区分登录欢迎消息模板可按渠道定制的两种登录方式：手机号
// （短信/App 码）与邮箱（邮箱登录账号，见 SignInMethodLabel）。没有第三维
// 区分——注册与否、2FA 与否都不影响，因为 recordWelcomeMessage 的调用方
// 不会携带更多信息。
type LoginMethod string

const (
	LoginMethodPhone LoginMethod = "phone"
	LoginMethodEmail LoginMethod = "email"
)

// LoginMethodFromLabel 把 SignInMethodLabel 的人类可读字符串映射回
// LoginMethod，这样已经算出 label 的调用方（{{...}} 模板历史 "via %s"
// 措辞）不必二次从 User 计算。
func LoginMethodFromLabel(label string) LoginMethod {
	if label == "email" {
		return LoginMethodEmail
	}
	return LoginMethodPhone
}

// DefaultWelcomeMessagePhoneTemplate / DefaultWelcomeMessageEmailTemplate 是
// 登录欢迎消息（每次完成登录后由官方系统账号 777000 发送）的内置最终兜底文案。
// 两条消息刻意分开（而非一份带方法名替换的模板），让各自渠道读起来自然。
//
// {{server_name}} 会被替换为服务器当前生效的展示名
// （见 ResolveWelcomeMessageTemplate / RenderWelcomeMessageTemplate）。
const (
	DefaultWelcomeMessagePhoneTemplate = "👋 Welcome to {{server_name}}!\n\nA new sign-in to your account was just completed using your phone number.\n\nIf this was you, no action is needed. If it wasn't, please revoke this session immediately from Settings → Privacy and Security → Active Sessions."

	DefaultWelcomeMessageEmailTemplate = "👋 Welcome to {{server_name}}!\n\nA new sign-in to your account was just completed using your email address.\n\nIf this was you, no action is needed. If it wasn't, please revoke this session immediately from Settings → Privacy and Security → Active Sessions."
)

// ResolveWelcomeMessageTemplate 按优先级挑选指定登录方式的消息模板正文：
// 显式管理面板覆盖（panelOverride，identity.Info 中按原样存储，空表示未配置）
// → 显式环境变量默认值（envDefault，空表示未配置）→ 该方式的编译期内置文案。
// 纯函数，便于无依赖地单测优先级。调用方（internal/app/auth）每次
// recordWelcomeMessage 都实时调用（绝不缓存），使管理面板修改立即生效。
func ResolveWelcomeMessageTemplate(method LoginMethod, panelOverride, envDefault string) string {
	if t := strings.TrimSpace(panelOverride); t != "" {
		return panelOverride
	}
	if t := strings.TrimSpace(envDefault); t != "" {
		return envDefault
	}
	if method == LoginMethodEmail {
		return DefaultWelcomeMessageEmailTemplate
	}
	return DefaultWelcomeMessagePhoneTemplate
}

// RenderWelcomeMessageTemplate 把 template 中的 {{server_name}} 占位符替换为
// 服务器当前生效的展示名。是字面量、单占位符替换——不引入模板引擎，因为
// 只有这一处替换要做。
func RenderWelcomeMessageTemplate(template string) string {
	return strings.ReplaceAll(template, "{{server_name}}", officialSystemDisplayName())
}
