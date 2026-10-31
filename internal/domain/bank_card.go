package domain

import (
	"unicode"
	"unicode/utf8"
)

// MinBankCardDigits 是卡号最少的数字位数（TDLib match_bank_card_numbers 的下限）。
const MinBankCardDigits = 13

// MayContainBankCardNumber 报告文本里的 ASCII 数字是否够组成一个卡号。自动实体识别的快速
// 路径用它跳过绝大多数只含少量数字（时间、金额、编号）的消息。
func MayContainBankCardNumber(message string) bool {
	count := 0
	for i := 0; i < len(message); i++ {
		if c := message[i]; c >= '0' && c <= '9' {
			count++
			if count >= MinBankCardDigits {
				return true
			}
		}
	}
	return false
}

// BankCardDigits 去掉卡号里的分组空格和连字符（识别出的卡号实体只含这两种分隔符），
// 出现其它字符时返回 false。客户端点击卡号实体时把实体原文发给 payments.getBankCardData，
// 例如 Android ChatActivity.didLongPressCard 发的是 "4242 4242 4242 4242"。
func BankCardDigits(number string) (string, bool) {
	digits := make([]byte, 0, len(number))
	for i := 0; i < len(number); i++ {
		switch c := number[i]; {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case c == ' ' || c == '-':
		default:
			return "", false
		}
	}
	return string(digits), true
}

// BankCardLuhnValid 报告纯数字卡号是否通过 Luhn 校验。
func BankCardLuhnValid(digits string) bool {
	if digits == "" {
		return false
	}
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		digit := int(c - '0')
		if (len(digits)-1-i)%2 == 1 {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
	}
	return sum%10 == 0
}

// BankCardScheme 返回 TDLib 识别卡号时区分的卡组织（td/telegram/MessageEntity.cpp
// is_valid_bank_card：Visa、Mastercard、American Express、UnionPay、MIR），无法判定时为空。
// digits 须至少 4 位纯数字。
func BankCardScheme(digits string) string {
	if len(digits) < 4 {
		return ""
	}
	prefix := func(n int) int {
		value := 0
		for _, c := range digits[:n] {
			value = value*10 + int(c-'0')
		}
		return value
	}
	switch p2, p4 := prefix(2), prefix(4); {
	case digits[0] == '4':
		return "Visa"
	case (51 <= p2 && p2 <= 55) || (2221 <= p4 && p4 <= 2720):
		return "Mastercard"
	case p2 == 34 || p2 == 37:
		return "American Express"
	case p2 == 62 || p2 == 81:
		return "UnionPay"
	case 2200 <= p4 && p4 <= 2204:
		return "MIR"
	}
	return ""
}

// bankCardEntityValid 是 TDLib is_valid_bank_card：Luhn 校验加各卡组织的位数。
func bankCardEntityValid(digits string) bool {
	if !BankCardLuhnValid(digits) {
		return false
	}
	switch n := len(digits); BankCardScheme(digits) {
	case "Visa":
		return n == 13 || n == 16 || n == 18 || n == 19
	case "Mastercard", "MIR":
		return n == 16
	case "American Express":
		return n == 15
	case "UnionPay":
		return n >= 16
	}
	return true
}

// detectBankCardMessageEntities 按 TDLib match_bank_card_numbers 找出文本中的卡号：
// 13–19 位数字，可用空格或连字符分组，前面不能紧跟字母、数字或 .,+-_，后面不能紧跟
// 字母或 -_；连续 16–19 位数字后的空格结束卡号。官方服务端下发 messageEntityBankCard，
// TDLib 对收到的消息不再自行识别（MessagesManager.cpp get_message_text skip_new_entities）。
func detectBankCardMessageEntities(message string) []MessageEntity {
	if !MayContainBankCardNumber(message) {
		return nil
	}
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	var out []MessageEntity
	i, n := 0, len(message)
	for {
		for i < n && !isDigit(message[i]) {
			i++
		}
		if i == n {
			return out
		}
		if prev, ok := previousRune(message, i); ok &&
			(prev == '.' || prev == ',' || prev == '+' || prev == '-' || prev == '_' || unicode.IsLetter(prev)) {
			for i < n && (isDigit(message[i]) || message[i] == ' ' || message[i] == '-') {
				i++
			}
			continue
		}
		begin := i
		digits := make([]byte, 0, 19)
		count := 0
		for i < n && (isDigit(message[i]) || message[i] == ' ' || message[i] == '-') {
			if message[i] == ' ' && count >= 16 && count <= 19 && count == i-begin {
				break
			}
			if isDigit(message[i]) {
				count++
				if len(digits) < cap(digits) {
					digits = append(digits, message[i])
				}
			}
			i++
		}
		if count < 13 || count > 19 {
			continue
		}
		end := i
		for !isDigit(message[end-1]) {
			end--
		}
		if end-begin > 2*count-1 {
			continue
		}
		if next, size := utf8.DecodeRuneInString(message[end:]); size > 0 &&
			(next == '-' || next == '_' || unicode.IsLetter(next)) {
			continue
		}
		if !bankCardEntityValid(string(digits)) {
			continue
		}
		out = append(out, MessageEntity{
			Type:   MessageEntityBankCard,
			Offset: automaticEntityUTF16Length(message[:begin]),
			Length: automaticEntityUTF16Length(message[begin:end]),
		})
	}
}
