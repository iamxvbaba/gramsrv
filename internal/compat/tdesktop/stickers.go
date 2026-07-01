package tdesktop

import "github.com/gotd/td/tg"

// ReorderStickerSets is a bounded compatibility stub for TDesktop startup/user
// preference writes. Sticker-set ordering is not persisted in the current
// small-scale media scope, but clients expect a successful Bool response.
func ReorderStickerSets(_ *tg.MessagesReorderStickerSetsRequest) (bool, error) {
	return true, nil
}
