package domain

import "testing"

func TestStarGiftSlugPrefixFromTitle(t *testing.T) {
	for title, want := range map[string]string{
		"Plush Pepe":      "PlushPepe",
		"Durov's Cap":     "DurovsCap",
		"Jack-in-the-Box": "JackintheBox",
		"Плюшевый Пепе":   "",
		"B-Day Candle 2":  "BDayCandle2",
	} {
		if got := StarGiftSlugPrefixFromTitle(title); got != want {
			t.Errorf("%q -> %q, want %q", title, got, want)
		}
	}
	write := StarGiftCollectibleWrite{GiftID: 1, UpgradeStars: 1, SupplyTotal: 1, SlugPrefix: "PlushPepe", CommandID: "x"}
	if !starGiftCollectibleSlugPrefix.MatchString(write.SlugPrefix) {
		t.Fatal("mixed-case prefix rejected")
	}
	if starGiftCollectibleSlugPrefix.MatchString("Plush Pepe") || starGiftCollectibleSlugPrefix.MatchString("-Pepe") {
		t.Fatal("invalid prefix accepted")
	}
}
