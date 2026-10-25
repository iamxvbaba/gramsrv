package rpc

import (
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
)

// preCheckoutTimeoutSeconds 是客户端挂起等待 bot 应答的窗口。Bot API 明确要求在
// 10 秒内回答 pre_checkout_query，超时这笔支付即失败。
const preCheckoutTimeoutSeconds = 10

// preCheckoutErrorMaxLen 是 error_message 的长度上限，与 Bot API 一致：超长直接拒绝，
// 而不是静默截断成一个含义已经变了的说辞。
const preCheckoutErrorMaxLen = 200

// preCheckoutFailedErr 是"付款闸门没有得到批准"：bot 明确拒绝、没有应答，或应答来迟。
//
// 它和"支付失败"是两件事：前者是卖家拒绝了订单，后者是技术故障。PAYMENT_FAILED
// 正是 Bot API 在 pre-checkout 未通过时使用的错误。
func preCheckoutFailedErr() error {
	return tgerr.New(400, "PAYMENT_FAILED")
}

// preCheckoutDeclinedErr 带上了 bot 给的理由——Bot API 就是把这个 error_message 显示给
// 付款人的，所以理由必须原样送到客户端。
//
// 这里手写 tgerr.Error 而不是 tgerr.New：New 会从 Message 反解 Type，而我们需要
// Type 固定为 PAYMENT_FAILED、Message 装 bot 的原话。errors.go 里禁止手写的理由是
// FLOOD_WAIT_X 这类带参数字段的反解，那一条在此不适用。
func preCheckoutDeclinedErr(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return preCheckoutFailedErr()
	}
	if utf8.RuneCountInString(message) > preCheckoutErrorMaxLen {
		return tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	return &tgerr.Error{
		Code:    400,
		Type:    "PAYMENT_FAILED",
		Message: message,
	}
}
