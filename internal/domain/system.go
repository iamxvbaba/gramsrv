package domain

import "telesrv/internal/branding"

const (
	// OfficialSystemUserID 是 Telegram 兼容客户端识别的官方系统账号。
	OfficialSystemUserID int64 = 777000

	// BotFatherUserID 是内置 BotFather 账号，与官方 @BotFather 同 ID。
	BotFatherUserID int64 = 93372553
	// BotFatherAccessHash 固定不变；与迁移 0090 的种子行双写，必须保持一致。
	BotFatherAccessHash int64 = 7421896403922962293

	// StickersBotUserID 是内置 @Stickers 账号。它是 server 内置 service bot，
	// 不走外部 Bot API 进程。
	StickersBotUserID int64 = 1063110917
	// StickersBotAccessHash 固定不变；与 postgres 种子行双写，必须保持一致。
	StickersBotAccessHash int64 = 5213187021149032991

	// ChatBotUserID 是内置 @ChatBot 账号。它把私聊文本转给 server AI provider 链。
	ChatBotUserID int64 = 1250000007
	// ChatBotAccessHash 固定不变；与 postgres 种子行双写，必须保持一致。
	ChatBotAccessHash int64 = 6332902371644871201

	// VerifyBotUserID is the built-in @verifybot: it collects official platform
	// verification applications and reports decisions back to the applicant. The
	// id is reserved and stable, so a restart never re-creates the account under a
	// different identity.
	VerifyBotUserID int64 = 1250000011
	// VerifyBotAccessHash is fixed and double-written with the seed row in
	// migration 0153; the two must never drift.
	VerifyBotAccessHash int64 = 7802113947355620887

	// VerifierBotUserID is the built-in @verifierbot: the first THIRD-PARTY
	// verifier of a deployment (core.telegram.org/api/bots/verification). It
	// collects applications for its own icon+description mark and reports the
	// operator's decision back to the applicant. The id is reserved and stable, so
	// a restart never re-creates the account under a different identity.
	//
	// It is not a second route to the platform checkmark: that badge is granted by
	// the operator alone and collected by VerifyBotUserID above.
	VerifierBotUserID int64 = 1250000013
	// VerifierBotAccessHash is fixed and double-written with the seed row in
	// migration 0156; the two must never drift.
	VerifierBotAccessHash int64 = 6913402578811563729
)

// OfficialSystemUser 返回第一阶段内置的官方系统账号。
func OfficialSystemUser() User {
	return User{
		ID:         OfficialSystemUserID,
		AccessHash: 6599886787491911851,
		Phone:      "42777",
		FirstName:  branding.ProductName,
		Username:   branding.ProductUsername,
		Verified:   true,
		Support:    true,
	}
}

// BotFatherUser 返回内置 BotFather 账号。username 不以 bot 结尾属种子例外（与官方一致）。
func BotFatherUser() User {
	return User{
		ID:             BotFatherUserID,
		AccessHash:     BotFatherAccessHash,
		FirstName:      "BotFather",
		Username:       "BotFather",
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

// StickersBotUser 返回内置 @Stickers 账号。username 不以 bot 结尾属种子例外（与官方一致）。
func StickersBotUser() User {
	return User{
		ID:             StickersBotUserID,
		AccessHash:     StickersBotAccessHash,
		FirstName:      "Stickers",
		Username:       "Stickers",
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 2,
	}
}

// ChatBotUser 返回内置 @ChatBot 账号。
func ChatBotUser() User {
	return User{
		ID:             ChatBotUserID,
		AccessHash:     ChatBotAccessHash,
		FirstName:      "ChatBot",
		Username:       "ChatBot",
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

// VerifyBotUser returns the built-in @verifybot account. It is verified itself,
// so the applicant sees the same badge on the account that grants it.
func VerifyBotUser() User {
	return User{
		ID:             VerifyBotUserID,
		AccessHash:     VerifyBotAccessHash,
		FirstName:      "Verify Bot",
		Username:       "verifybot",
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

// VerifierBotUser returns the built-in @verifierbot account.
//
// Verified is false on purpose: the official checkmark is the platform's own
// mechanism, and a third-party verifier wearing it would blur exactly the
// distinction this bot has to explain to every applicant. What makes the account a
// verifier is the operator-granted BotVerifierSettings row, not this seed.
func VerifierBotUser() User {
	return User{
		ID:             VerifierBotUserID,
		AccessHash:     VerifierBotAccessHash,
		FirstName:      "Verifier Bot",
		Username:       "verifierbot",
		Verified:       false,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

// SystemUserByID 返回内置系统账号；非系统账号返回 ok=false。
// 所有对 777000 的硬编码注入点统一经此函数，新增内置账号只改这里。
func SystemUserByID(id int64) (User, bool) {
	switch id {
	case OfficialSystemUserID:
		return OfficialSystemUser(), true
	case BotFatherUserID:
		return BotFatherUser(), true
	case StickersBotUserID:
		return StickersBotUser(), true
	case ChatBotUserID:
		return ChatBotUser(), true
	case VerifyBotUserID:
		return VerifyBotUser(), true
	case VerifierBotUserID:
		return VerifierBotUser(), true
	}
	return User{}, false
}

func IsSystemUserID(id int64) bool {
	_, ok := SystemUserByID(id)
	return ok
}

func SystemUserByPhone(phone string) (User, bool) {
	phone = NormalizePhone(phone)
	for _, id := range []int64{OfficialSystemUserID, BotFatherUserID, StickersBotUserID, ChatBotUserID, VerifyBotUserID, VerifierBotUserID} {
		u, ok := SystemUserByID(id)
		if !ok || u.Phone == "" {
			continue
		}
		if NormalizePhone(u.Phone) == phone {
			return u, true
		}
	}
	return User{}, false
}
