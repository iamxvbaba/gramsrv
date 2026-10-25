package botapi

import (
	"net/http"
	"strconv"

	"telesrv/internal/domain"
)

// getAvailableGifts implements the Bot API getAvailableGifts method: the Star
// Gift catalog a bot may pick from when sending a gift.
//
// The catalog is the server's, not a hardcoded list: a bot is expected to call
// this method, read a gift id out of the result and pass it to sendGift, so a
// gift id is never a constant a bot hardcodes. The list is the enabled catalog
// exactly as payments.getStarGifts serves it over MTProto — a limited gift that
// is sold out or a gift that is not yet released still appears, because
// remaining_count is what tells a bot it cannot be bought right now. Availability
// is enforced where it is atomic, in the purchase transaction behind sendGift.
func (h *handler) getAvailableGifts(w http.ResponseWriter, r *http.Request) {
	service, ok := h.gateway.(StarGiftGatewayService)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	gifts, err := service.BotAPIAvailableGifts(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR")
		return
	}
	out := make([]map[string]any, 0, len(gifts))
	for _, gift := range gifts {
		out = append(out, apiStarGift(gift))
	}
	writeAPIOK(w, map[string]any{"gifts": out})
}

// apiStarGift projects one catalog entry as a Bot API Gift.
//
// star_count and id are always present. upgrade_star_count appears only while a
// collectible upgrade is actually purchasable: the store reports UpgradeStars
// even for a gift whose unique supply is exhausted, and advertising a price the
// server would then refuse is worse than omitting the field. total_count and
// remaining_count are limited-gift inventory and are omitted for unlimited gifts,
// which is what the Bot API contract expects.
func apiStarGift(gift domain.StarGift) map[string]any {
	item := map[string]any{
		"id":         strconv.FormatInt(gift.ID, 10),
		"star_count": gift.Stars,
	}
	if gift.Sticker.ID > 0 {
		item["sticker"] = apiSticker(gift.Sticker)
	}
	if gift.UpgradeStars > 0 && gift.UpgradeIssued < gift.UpgradeTotal {
		item["upgrade_star_count"] = gift.UpgradeStars
	}
	if gift.Limited {
		item["total_count"] = gift.AvailabilityTotal
		item["remaining_count"] = gift.AvailabilityRemains
	}
	return item
}
