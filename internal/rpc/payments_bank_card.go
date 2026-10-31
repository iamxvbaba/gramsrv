package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bankCardData 实现 payments.getBankCardData：卡号须为 13–19 位数字并通过 Luhn 校验，否则
// 返回官方 BANK_CARD_NUMBER_INVALID。title 是 TDLib 识别卡号时区分的卡组织
// （domain.BankCardScheme），无法判定时留空而不臆造。telesrv 不接入任何发卡行，open_urls 恒为空；
// 客户端仍提供复制卡号（DrKLO ChatActivity.didPressBankCard 的 CopyCardNumber）。
func bankCardData(number string) (*tg.PaymentsBankCardData, error) {
	if len(number) < 13 || len(number) > 19 || !domain.BankCardLuhnValid(number) {
		return nil, tgerr.New(400, "BANK_CARD_NUMBER_INVALID")
	}
	return &tg.PaymentsBankCardData{Title: domain.BankCardScheme(number), OpenURLs: []tg.BankCardOpenURL{}}, nil
}
