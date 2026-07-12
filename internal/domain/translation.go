package domain

import "errors"

const (
	// MaxTranslationTexts matches the largest batch currently issued by TDesktop's
	// whole-chat translation tracker.
	MaxTranslationTexts = 20
	// MaxTranslationInputBytes bounds one upstream request independently of the
	// MTProto frame limit. TDesktop starts a new batch at the same 24 KiB boundary.
	MaxTranslationInputBytes = 24 * 1024
	// MaxTranslationOutputBytes permits language expansion while bounding an
	// untrusted provider response before TL encoding.
	MaxTranslationOutputBytes = 4 * MaxTranslationInputBytes
	MaxTranslationToneRunes   = 64
)

var (
	ErrTranslationDisabled            = errors.New("translation disabled")
	ErrTranslationInputEmpty          = errors.New("translation input empty")
	ErrTranslationInputTooLong        = errors.New("translation input too long")
	ErrTranslationLanguageInvalid     = errors.New("translation language invalid")
	ErrTranslationMessageInvalid      = errors.New("translation message invalid")
	ErrTranslationPeerInvalid         = errors.New("translation peer invalid")
	ErrTranslationRateLimited         = errors.New("translation rate limited")
	ErrTranslationProviderUnavailable = errors.New("translation provider unavailable")
	ErrTranslationTimeout             = errors.New("translation timeout")
)

// TranslationText is protocol-neutral text accepted or returned by the
// translation service. Providers may omit entities when they cannot preserve
// offsets safely; they must never return stale source offsets for changed text.
type TranslationText struct {
	Text     string
	Entities []MessageEntity
}

func (t TranslationText) Clone() TranslationText {
	out := t
	out.Entities = append([]MessageEntity(nil), t.Entities...)
	return out
}

type TranslationRequest struct {
	UserID int64
	// Peer+IDs selects stored messages. Texts selects caller-supplied text.
	// Exactly one mode must be used.
	Peer   Peer
	IDs    []int
	Texts  []TranslationText
	ToLang string
	Tone   string
}

type TranslationResult struct {
	Texts []TranslationText
}
