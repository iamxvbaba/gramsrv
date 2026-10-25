package domain

import (
	"errors"
	"sort"
	"time"
)

// StarGiftCollectionDisplayFilter* are persisted filters used by the
// collection editor. The iOS client may combine several bits in one mask.
const (
	StarGiftCollectionDisplayFilterUnlimited uint32 = 1 << iota
	StarGiftCollectionDisplayFilterLimited
	StarGiftCollectionDisplayFilterUpgradable
	StarGiftCollectionDisplayFilterUnique
	StarGiftCollectionDisplayFilterDisplayed
	StarGiftCollectionDisplayFilterHidden
)

const (
	StarGiftCollectionDisplayFilterMaskAll = StarGiftCollectionDisplayFilterUnlimited |
		StarGiftCollectionDisplayFilterLimited |
		StarGiftCollectionDisplayFilterUpgradable |
		StarGiftCollectionDisplayFilterUnique |
		StarGiftCollectionDisplayFilterDisplayed |
		StarGiftCollectionDisplayFilterHidden
	// The client currently has compact and expanded tiles. Keeping the value
	// numeric leaves room for future layout modes without changing the TL.
	StarGiftCollectionDisplaySizeCompact  = 0
	StarGiftCollectionDisplaySizeExpanded = 1
)

var (
	ErrStarGiftCollectionDisplayInvalid     = errors.New("stargift: invalid collection display settings")
	ErrStarGiftCollectionDisplayUnsupported = errors.New("stargift: collection display settings store is unavailable")
)

// StarGiftCollectionDisplayItem is the persisted presentation of one gift in
// a collection. Order is the complete collection order; Size controls the
// compact/expanded tile shown by the custom client.
type StarGiftCollectionDisplayItem struct {
	GiftID int64
	Order  int
	Size   int
}

// StarGiftCollectionDisplaySettings is the complete display state for one
// collection. A row is materialized lazily, so the zero settings returned for
// an old collection are the protocol default: displayed, no filters, compact
// tiles in the normal collection order.
type StarGiftCollectionDisplaySettings struct {
	Owner        Peer
	CollectionID int
	Hidden       bool
	MainTab      bool
	FilterMask   int
	Items        []StarGiftCollectionDisplayItem
	UpdatedAt    time.Time
}

// StarGiftCollectionDisplayPatch is the optional part of the custom update
// method. ItemsSet distinguishes an omitted layout vector from an explicit
// replacement vector.
type StarGiftCollectionDisplayPatch struct {
	Hidden     *bool
	MainTab    *bool
	FilterMask *int
	Items      []StarGiftCollectionDisplayItem
	ItemsSet   bool
}

// NormalizeStarGiftCollectionDisplayItems validates and returns a complete,
// canonical order for the current collection membership. The custom method
// deliberately requires the full vector when the layout flag is present, so
// concurrent edits cannot silently discard a gift's position.
func NormalizeStarGiftCollectionDisplayItems(giftIDs []int64, items []StarGiftCollectionDisplayItem) ([]StarGiftCollectionDisplayItem, error) {
	if len(items) != len(giftIDs) {
		return nil, ErrStarGiftCollectionDisplayInvalid
	}
	known := make(map[int64]struct{}, len(giftIDs))
	for _, giftID := range giftIDs {
		if giftID <= 0 {
			return nil, ErrStarGiftCollectionDisplayInvalid
		}
		known[giftID] = struct{}{}
	}
	seenIDs := make(map[int64]struct{}, len(items))
	allOrdersZero := len(items) > 1
	out := append([]StarGiftCollectionDisplayItem(nil), items...)
	for _, item := range out {
		if _, ok := known[item.GiftID]; !ok || item.GiftID <= 0 ||
			(item.Size != StarGiftCollectionDisplaySizeCompact && item.Size != StarGiftCollectionDisplaySizeExpanded) {
			return nil, ErrStarGiftCollectionDisplayInvalid
		}
		if _, duplicate := seenIDs[item.GiftID]; duplicate {
			return nil, ErrStarGiftCollectionDisplayInvalid
		}
		if item.Order != 0 {
			allOrdersZero = false
		}
		seenIDs[item.GiftID] = struct{}{}
	}
	if len(seenIDs) != len(known) {
		return nil, ErrStarGiftCollectionDisplayInvalid
	}
	if allOrdersZero {
		// Some clients send the desired vector order and leave the optional
		// presentation order at its zero value. The vector is authoritative.
		for i := range out {
			out[i].Order = i
		}
	} else {
		seenOrders := make(map[int]struct{}, len(items))
		for _, item := range out {
			if item.Order < 0 || item.Order >= len(out) {
				return nil, ErrStarGiftCollectionDisplayInvalid
			}
			if _, duplicate := seenOrders[item.Order]; duplicate {
				return nil, ErrStarGiftCollectionDisplayInvalid
			}
			seenOrders[item.Order] = struct{}{}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

// StarGiftCollectionDisplaySettingsHash is stable for the ordered response
// and changes on every persisted display setting or layout change.
func StarGiftCollectionDisplaySettingsHash(settings []StarGiftCollectionDisplaySettings) int64 {
	var h uint64 = 0x53474453
	for _, setting := range settings {
		h = h*0x4f25 + uint64(setting.CollectionID)
		if setting.Hidden {
			h = h*0x4f25 + 1
		} else {
			h = h * 0x4f25
		}
		if setting.MainTab {
			h = h*0x4f25 + 1
		} else {
			h = h * 0x4f25
		}
		h = h*0x4f25 + uint64(uint32(setting.FilterMask))
		for _, item := range setting.Items {
			h = h*0x4f25 + uint64(item.GiftID)
			h = h*0x4f25 + uint64(item.Order)
			h = h*0x4f25 + uint64(item.Size)
		}
	}
	return int64(h & 0x7fffffffffffffff)
}
