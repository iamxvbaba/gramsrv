package officialgifts

import "strconv"

// Telegram's classic gifts carry no title in the snapshot: the official
// clients show them by their sticker alone. Without one, the panel and the
// catalog showed "Unnamed official gift #<id>" and collectible slugs fell
// back to "official-<id>". These are their well-known names.
var classicGiftNames = map[int64]string{
	5170145012310081615: "Heart",
	5170233102089322756: "Teddy Bear",
	5170250947678437525: "Gift Box",
	5168103777563050263: "Rose",
	5170144170496491616: "Cake",
	5170314324215857265: "Bouquet",
	5170564780938756245: "Rocket",
	6028601630662853006: "Champagne",
	5168043875654172773: "Trophy",
	5170690322832818290: "Ring",
	5170521118301225164: "Diamond",
}

// ClassicGiftName returns the known name of an untitled classic gift.
func ClassicGiftName(id int64) (string, bool) {
	name, ok := classicGiftNames[id]
	return name, ok
}

// displayTitle is the snapshot title, or the classic gift's known name, or
// "Gift 500⭐" for an untitled gift we do not know (the price tells them
// apart; the operator can still rename it on import).
func displayTitle(id int64, title string, stars int64) string {
	if title != "" {
		return title
	}
	if name, ok := classicGiftNames[id]; ok {
		return name
	}
	return "Gift " + strconv.FormatInt(stars, 10) + "⭐"
}
