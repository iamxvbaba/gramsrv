// Package freeze bundles the default "frozen account" custom emoji into the
// server binary. The .tgs body is embedded so the synthetic third-party mark on
// frozen accounts (see internal/rpc/bot_verification_projection.go
// accountFrozenMarkIcon) always has a resolvable document even on servers that
// ship no on-disk sticker seed directories.
package freeze

import (
	"embed"
)

// FS serves the bundled snowflake custom emoji body.
//
//go:embed freeze.tgs
var FS embed.FS

// DocumentID is the frozen-account mark icon document (accountFrozenMarkIcon).
const DocumentID int64 = 4572719592734399185

// DocumentAccessHash is the id/key pair the frozen mark ships to clients.
const DocumentAccessHash int64 = 5898696213995476702

// SetID identifies the "freeze" custom emoji set that owns the document.
const SetID int64 = 1666345467034827165

// SetAccessHash pairs with SetID for the set reference in the document attributes.
const SetAccessHash int64 = 6870414715053953623

// Emoticon is the pack mapping entry for the bundled emoji.
const Emoticon = "\u2744\ufe0f"
