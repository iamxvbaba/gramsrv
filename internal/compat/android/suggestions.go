// Package android 收敛 DrKLO Android 官方客户端的兼容决策。
package android

import "strings"

const ValidatePhoneNumberSuggestion = "VALIDATE_PHONE_NUMBER"

// DismissSuggestion 返回 telesrv 对 suggestion dismissal 的有界兼容结果。
// telesrv 当前不向 config/channelFull 发布 pending suggestions，因此不存在需要
// 持久化的服务端 suggestion 状态；对已登录客户端的非空 dismissal 做幂等确认，
// 并阻止 DrKLO 对 generic 500 无限重试。
func DismissSuggestion(suggestion string) bool {
	return strings.TrimSpace(suggestion) != ""
}
