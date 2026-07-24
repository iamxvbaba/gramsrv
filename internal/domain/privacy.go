package domain

import "errors"

// PrivacyKey identifies a Telegram account privacy setting without exposing tg.*.
type PrivacyKey string

const (
	PrivacyKeyStatusTimestamp   PrivacyKey = "status_timestamp"
	PrivacyKeyChatInvite        PrivacyKey = "chat_invite"
	PrivacyKeyPhoneCall         PrivacyKey = "phone_call"
	PrivacyKeyPhoneP2P          PrivacyKey = "phone_p2p"
	PrivacyKeyForwards          PrivacyKey = "forwards"
	PrivacyKeyProfilePhoto      PrivacyKey = "profile_photo"
	PrivacyKeyPhoneNumber       PrivacyKey = "phone_number"
	PrivacyKeyAddedByPhone      PrivacyKey = "added_by_phone"
	PrivacyKeyVoiceMessages     PrivacyKey = "voice_messages"
	PrivacyKeyAbout             PrivacyKey = "about"
	PrivacyKeyBirthday          PrivacyKey = "birthday"
	PrivacyKeyStarGiftsAutoSave PrivacyKey = "star_gifts_auto_save"
	PrivacyKeyNoPaidMessages    PrivacyKey = "no_paid_messages"
	PrivacyKeySavedMusic        PrivacyKey = "saved_music"
)

// PrivacyRuleKind mirrors Layer 225 privacy rule constructors.
type PrivacyRuleKind string

const (
	PrivacyRuleAllowContacts            PrivacyRuleKind = "allow_contacts"
	PrivacyRuleAllowAll                 PrivacyRuleKind = "allow_all"
	PrivacyRuleAllowUsers               PrivacyRuleKind = "allow_users"
	PrivacyRuleDisallowContacts         PrivacyRuleKind = "disallow_contacts"
	PrivacyRuleDisallowAll              PrivacyRuleKind = "disallow_all"
	PrivacyRuleDisallowUsers            PrivacyRuleKind = "disallow_users"
	PrivacyRuleAllowChatParticipants    PrivacyRuleKind = "allow_chat_participants"
	PrivacyRuleDisallowChatParticipants PrivacyRuleKind = "disallow_chat_participants"
	PrivacyRuleAllowCloseFriends        PrivacyRuleKind = "allow_close_friends"
	PrivacyRuleAllowPremium             PrivacyRuleKind = "allow_premium"
	PrivacyRuleAllowBots                PrivacyRuleKind = "allow_bots"
	PrivacyRuleDisallowBots             PrivacyRuleKind = "disallow_bots"
)

// PrivacyRule is a protocol-neutral privacy rule.
type PrivacyRule struct {
	Kind    PrivacyRuleKind `json:"kind"`
	UserIDs []int64         `json:"user_ids,omitempty"`
	ChatIDs []int64         `json:"chat_ids,omitempty"`
}

// PrivacyRules is one owner/key rule set.
type PrivacyRules struct {
	OwnerUserID int64         `json:"owner_user_id,omitempty"`
	Key         PrivacyKey    `json:"key"`
	Rules       []PrivacyRule `json:"rules"`
}

// PrivacyContext describes viewer facts needed for privacy evaluation.
type PrivacyContext struct {
	OwnerUserID       int64
	ViewerUserID      int64
	ViewerIsContact   bool
	ViewerIsBot       bool
	ViewerIsPremium   bool
	ViewerCloseFriend bool
	SharedChatIDs     []int64
}

var (
	ErrPrivacyKeyInvalid  = errors.New("privacy key invalid")
	ErrPrivacyRuleInvalid = errors.New("privacy rule invalid")
)

// DefaultPrivacyRules returns Telegram-like defaults used when no user setting exists.
func DefaultPrivacyRules(key PrivacyKey) []PrivacyRule {
	switch key {
	case PrivacyKeyPhoneNumber:
		return []PrivacyRule{{Kind: PrivacyRuleDisallowAll}}
	case PrivacyKeyNoPaidMessages:
		// This key is an allow-list of peers exempt from paid private
		// messages, not the base visibility of a profile field.
		return []PrivacyRule{{Kind: PrivacyRuleDisallowAll}}
	case PrivacyKeyBirthday:
		return []PrivacyRule{{Kind: PrivacyRuleAllowContacts}}
	default:
		return []PrivacyRule{{Kind: PrivacyRuleAllowAll}}
	}
}
