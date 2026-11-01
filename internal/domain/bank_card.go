package domain

import (
	"unicode"
	"unicode/utf8"
)

// MinBankCardDigits is the minimum number of digits in a card number (the lower
// bound of TDLib match_bank_card_numbers).
const MinBankCardDigits = 13

// MayContainBankCardNumber reports whether the text contains enough ASCII digits
// to form a card number. The fast path of automatic entity detection uses it to
// skip the vast majority of messages that contain only a few digits (time,
// amount, ID).
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

// BankCardDigits strips the grouping spaces and hyphens from a card number (a
// detected bank-card entity contains only these two separators), returning false
// when any other character is present. When a client taps a bank-card entity it
// sends the entity's raw text to payments.getBankCardData, for example Android
// ChatActivity.didLongPressCard sends "4242 4242 4242 4242".
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

// BankCardLuhnValid reports whether a digits-only card number passes the Luhn check.
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

// BankCardScheme returns the card scheme that TDLib distinguishes when recognizing
// a card number (td/telegram/MessageEntity.cpp is_valid_bank_card: Visa,
// Mastercard, American Express, UnionPay, MIR), or empty when it cannot be
// determined. digits must be at least 4 digits.
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

// bankCardEntityValid mirrors TDLib is_valid_bank_card: the Luhn check plus each
// scheme's digit count.
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

// detectBankCardMessageEntities finds card numbers in the text following TDLib
// match_bank_card_numbers: 13-19 digits, optionally grouped with spaces or
// hyphens, not immediately preceded by a letter, digit or .,+-_, and not
// immediately followed by a letter or -_; a space after 16-19 consecutive digits
// ends the card number. The official server sends messageEntityBankCard, and
// TDLib no longer recognizes it on its own for received messages
// (MessagesManager.cpp get_message_text skip_new_entities).
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
