package domain

import "testing"

func TestStarGiftSlugPrefixFromTitle(t *testing.T) {
	for title, want := range map[string]string{
		"Plush Pepe":     "PlushPepe",
		"Durov's Cap":    "DurovsCap",
		"  Lol Pop 2 ":   "LolPop2",
		"Плюшевый Пепе":  "",
		"#5170145012310": "",
		"🎁":              "",
	} {
		if got := StarGiftSlugPrefixFromTitle(title); got != want {
			t.Errorf("StarGiftSlugPrefixFromTitle(%q) = %q, want %q", title, got, want)
		}
	}
	long := StarGiftSlugPrefixFromTitle("Abcdefghij Abcdefghij Abcdefghij Abcdefghij Abcdefghij")
	if len(long) != 40 || !starGiftCollectibleSlugPrefix.MatchString(long) {
		t.Fatalf("long title prefix = %q (%d), want a valid 40-char prefix", long, len(long))
	}
	if !starGiftCollectibleSlugPrefix.MatchString("PlushPepe") {
		t.Fatal("mixed-case prefixes must be valid")
	}
}
