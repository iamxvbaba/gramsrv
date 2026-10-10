package domain

import (
	"strings"
	"sync/atomic"

	"telesrv/internal/branding"
)

const (
	// OfficialSystemUserID 是 Telegram 兼容客户端识别的官方系统账号。
	OfficialSystemUserID int64 = 777000
	// OfficialSystemPhone is a reserved service identity, not an ordinary E.164
	// login number. Auth must recognize and reject it before account lookup.
	OfficialSystemPhone = "42777"

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

	// PremiumBotUserID is the stable identity of the built-in @premiumbot.
	PremiumBotUserID     int64 = 1250000015
	PremiumBotAccessHash int64 = 5841763291047652381

	// GifBotUserID is intentionally distinct from PremiumBotUserID. The
	// upstream dev implementation reused 1250000015; that would overwrite the
	// retained Premium storefront in this project.
	GifBotUserID     int64 = 1250000017
	GifBotAccessHash int64 = 7233282977235616768

	// GiftRelayerUserID is the stable @relayer profile used to host an exported
	// collectible until its current TON owner claims it to a Gramsrv profile.
	GiftRelayerUserID     int64 = 1250000019
	GiftRelayerAccessHash int64 = 6184953271048627319

	// GiftClaimBotUserID is the stable @claim service bot and Mini App owner.
	GiftClaimBotUserID     int64 = 1250000021
	GiftClaimBotAccessHash int64 = 8462917350861429053

	// GiftClaimTestBotUserID is the stable @claimtest service bot and Mini App owner.
	GiftClaimTestBotUserID     int64 = 1250000022
	GiftClaimTestBotAccessHash int64 = 3284157906273045387
)

var configuredPremiumBotUserID atomic.Int64
var configuredPremiumBotUsername atomic.Value

// ConfigurePremiumBotUserID installs the deployment's reserved Premium bot ID
// before stores and RPC services are constructed.
func ConfigurePremiumBotUserID(id int64) bool {
	if !ValidPremiumBotUserID(id) {
		return false
	}
	configuredPremiumBotUserID.Store(id)
	return true
}

func ConfigurePremiumBotUsername(username string) bool {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if !ValidBotUsername(username) {
		return false
	}
	configuredPremiumBotUsername.Store(username)
	return true
}

func ValidPremiumBotUserID(id int64) bool {
	if id <= 0 {
		return false
	}
	switch id {
	case OfficialSystemUserID, BotFatherUserID, StickersBotUserID, ChatBotUserID,
		VerifyBotUserID, VerifierBotUserID, GifBotUserID:
		return false
	}
	return true
}

func PremiumBotConfiguredUserID() int64 {
	if id := configuredPremiumBotUserID.Load(); id > 0 {
		return id
	}
	return PremiumBotUserID
}

func PremiumBotConfiguredUsername() string {
	if username, ok := configuredPremiumBotUsername.Load().(string); ok && username != "" {
		return username
	}
	return "premiumbot"
}

// officialSystemUserDisplayName 覆盖 OfficialSystemUser 的 FirstName——
// 空表示「使用 branding.ProductName()」（编译期默认）。operator 在
// Server Settings → Server identity 中配置的名称既在启动时写入，也在运行期
// 由身份 watcher 检测到变化后重新写入（主服务器与管理面板分属两个进程、只共享
// identity.json），因此用 atomic.Value 承载，避免投影路径与刷新路径的数据竞争。
// 刻意只覆盖展示名，不动 Username：@username 是稳定、可寻址的标识符，
// 其他东西可能已引用它；展示名则只出现在聊天头部。
var officialSystemUserDisplayName atomic.Value

// SetOfficialSystemUserDisplayName 记录 operator 为官方系统账号 (777000)
// 配置的自定义服务器名，启动时与运行期身份刷新时调用。
// 传 "" 表示回退到 branding.ProductName()——与 avatar 覆盖相同的
// 「未设置 → 默认值」契约。
func SetOfficialSystemUserDisplayName(name string) {
	officialSystemUserDisplayName.Store(strings.TrimSpace(name))
}

// officialSystemDisplayName 返回官方系统账号当前生效的展示名：若通过
// SetOfficialSystemUserDisplayName 设置了 operator 自定义值则用之，否则
// branding.ProductName()。OfficialSystemUser（777000 的 FirstName）与登录
// 欢迎消息的 {{server_name}} 占位符（见 login_welcome_template.go）共用，
// 保证两者一致。
func officialSystemDisplayName() string {
	if name, ok := officialSystemUserDisplayName.Load().(string); ok && name != "" {
		return name
	}
	return branding.ProductName()
}

// OfficialSystemUser 返回第一阶段内置的官方系统账号。
func OfficialSystemUser() User {
	return User{
		ID:         OfficialSystemUserID,
		AccessHash: 6599886787491911851,
		Phone:      OfficialSystemPhone,
		FirstName:  officialSystemDisplayName(),
		Username:   branding.ProductUsername(),
		Verified:   true,
		Support:    true,
	}
}

// ChatBotDescription and StickersBotDescription are the shared branded seed
// text for both user.about and bots.description. PostgreSQL migrations contain
// only the default snapshot; startup reconciliation and the memory backend use
// these helpers so custom deployments do not expose stale "telesrv" text.
func ChatBotDescription() string {
	return "Chat with the configured " + branding.ProductName() + " AI provider."
}

func StickersBotDescription() string {
	return "Create custom sticker and emoji packs for " + branding.ProductName() + "."
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
		About:          StickersBotDescription(),
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
		About:          ChatBotDescription(),
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

// PremiumBotUser returns the built-in Premium storefront and payment peer.
func PremiumBotUser() User {
	return User{
		ID:             PremiumBotConfiguredUserID(),
		AccessHash:     PremiumBotAccessHash,
		FirstName:      "Premium Bot",
		Username:       PremiumBotConfiguredUsername(),
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

// GifBotUser returns the built-in inline-only @gif catalog bot.
func GifBotUser() User {
	return User{
		ID:             GifBotUserID,
		AccessHash:     GifBotAccessHash,
		FirstName:      "GIF",
		Username:       "gif",
		Verified:       true,
		Bot:            true,
		BotInfoVersion: 1,
	}
}

func GiftRelayerUser() User {
	return User{
		ID: GiftRelayerUserID, AccessHash: GiftRelayerAccessHash,
		FirstName: "InvGram Gift Relayer", Username: "relayer", Verified: true,
	}
}

func GiftClaimBotUser() User {
	return User{
		ID: GiftClaimBotUserID, AccessHash: GiftClaimBotAccessHash,
		FirstName: "InvGram Gift Claim", Username: "claim", Verified: true,
		Bot: true, BotInfoVersion: 1,
	}
}

func GiftClaimTestBotUser() User {
	return User{
		ID: GiftClaimTestBotUserID, AccessHash: GiftClaimTestBotAccessHash,
		FirstName: "InvGram Gift Claim (test)", Username: "claimtest", Verified: true,
		Bot: true, BotInfoVersion: 1,
	}
}

// SystemUserByID 返回内置系统账号；非系统账号返回 ok=false。
// 所有对 777000 的硬编码注入点统一经此函数，新增内置账号只改这里。
func SystemUserByID(id int64) (User, bool) {
	if id == PremiumBotConfiguredUserID() {
		return PremiumBotUser(), true
	}
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
	case GifBotUserID:
		return GifBotUser(), true
	case GiftRelayerUserID:
		return GiftRelayerUser(), true
	case GiftClaimBotUserID:
		return GiftClaimBotUser(), true
	case GiftClaimTestBotUserID:
		return GiftClaimTestBotUser(), true
	}
	return User{}, false
}

func IsSystemUserID(id int64) bool {
	_, ok := SystemUserByID(id)
	return ok
}

// SystemUserIDs returns every built-in account id, in a stable order.
//
// It is the one list to extend when a service account is added, so a caller that
// has to enumerate them -- a SQL predicate excluding infrastructure, say -- cannot
// silently miss one the way an inline literal would.
func SystemUserIDs() []int64 {
	return []int64{
		OfficialSystemUserID,
		BotFatherUserID,
		StickersBotUserID,
		ChatBotUserID,
		VerifyBotUserID,
		VerifierBotUserID,
		PremiumBotConfiguredUserID(),
		GifBotUserID,
		GiftRelayerUserID,
		GiftClaimBotUserID,
		GiftClaimTestBotUserID,
	}
}

// SystemUserByUsername resolves reserved short handles such as @gif that do
// not satisfy the ordinary 4/5-character account creation rules.
func SystemUserByUsername(username string) (User, bool) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	for _, id := range SystemUserIDs() {
		u, ok := SystemUserByID(id)
		if ok && strings.EqualFold(u.Username, username) {
			return u, true
		}
	}
	return User{}, false
}

func SystemUserByPhone(phone string) (User, bool) {
	phone = NormalizePhone(phone)
	for _, id := range SystemUserIDs() {
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
