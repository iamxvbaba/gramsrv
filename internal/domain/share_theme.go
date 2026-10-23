package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Layer 232 share themes decorate the "share profile" card of a user or a
// public channel (messages.setShareTheme / userFull.share_theme /
// channelFull.share_theme). The default theme is stored as no row.

// ShareThemeKind selects the stored share theme variant.
type ShareThemeKind string

const (
	// ShareThemeEmojiBackdrop is shareThemeEmojiBackdrop: an optional
	// collectible backdrop plus up to MaxShareThemeEmojis emoji.
	ShareThemeEmojiBackdrop ShareThemeKind = "emoji_backdrop"
	// ShareThemeGift is shareThemeGift: a unique gift owned by the peer.
	ShareThemeGift ShareThemeKind = "gift"
)

// MaxShareThemeEmojis matches TDesktop's share-theme editor limit.
const MaxShareThemeEmojis = 6

// ErrShareThemeInvalid rejects a malformed or unavailable share theme.
var ErrShareThemeInvalid = errors.New("share theme invalid")

// ShareThemeEmoji is one emoji of an emoji/backdrop theme: either a plain
// emoticon or a custom emoji document.
type ShareThemeEmoji struct {
	Emoticon   string `json:"emoticon,omitempty"`
	DocumentID int64  `json:"document_id,omitempty"`
}

// ShareTheme is a non-default share theme.
type ShareTheme struct {
	Kind         ShareThemeKind
	BackdropID   int // 0 = no backdrop
	Emojis       []ShareThemeEmoji
	UniqueGiftID int64
}

// Validate checks the structural shape; backdrop and gift availability are
// checked against the catalog and ownership by the caller.
func (t ShareTheme) Validate() error {
	switch t.Kind {
	case ShareThemeEmojiBackdrop:
		if t.UniqueGiftID != 0 || t.BackdropID < 0 || len(t.Emojis) > MaxShareThemeEmojis {
			return fmt.Errorf("%w: emoji/backdrop shape", ErrShareThemeInvalid)
		}
		if t.BackdropID == 0 && len(t.Emojis) == 0 {
			return fmt.Errorf("%w: empty emoji/backdrop theme", ErrShareThemeInvalid)
		}
		for _, emoji := range t.Emojis {
			text := strings.TrimSpace(emoji.Emoticon)
			switch {
			case emoji.DocumentID > 0 && emoji.Emoticon == "":
			case emoji.DocumentID == 0 && text != "" && text == emoji.Emoticon &&
				len(text) <= 32 && utf8.ValidString(text):
			default:
				return fmt.Errorf("%w: emoji", ErrShareThemeInvalid)
			}
		}
	case ShareThemeGift:
		if t.UniqueGiftID <= 0 || t.BackdropID != 0 || len(t.Emojis) != 0 {
			return fmt.Errorf("%w: gift shape", ErrShareThemeInvalid)
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrShareThemeInvalid, t.Kind)
	}
	return nil
}

// ShareThemeBackdrop is one selectable backdrop
// (shareThemeBackdropOption), taken from published collectible attributes.
type ShareThemeBackdrop struct {
	BackdropID   int
	Name         string
	CenterColor  int
	EdgeColor    int
	PatternColor int
	TextColor    int
}
