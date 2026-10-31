package appearance

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDefaultCatalogShape(t *testing.T) {
	catalog := Default()
	if catalog.Source == "" {
		t.Fatalf("Source is empty")
	}
	if len(catalog.ChatThemes) == 0 {
		t.Fatalf("ChatThemes empty, want official chat themes")
	}
	if len(catalog.Wallpapers) == 0 {
		t.Fatalf("Wallpapers empty, want official wallpapers")
	}
	if len(catalog.PeerColors) != 21 {
		t.Fatalf("PeerColors length = %d, want carried-over count 21", len(catalog.PeerColors))
	}
	if len(catalog.PeerProfileColors) != 16 {
		t.Fatalf("PeerProfileColors length = %d, want carried-over count 16", len(catalog.PeerProfileColors))
	}

	// At least one wallpaper with a downloadable document + thumbnail, and its embedded bytes match the sha256.
	var withDoc *Wallpaper
	for i := range catalog.Wallpapers {
		if catalog.Wallpapers[i].Document.ID != 0 && catalog.Wallpapers[i].Document.Path != "" {
			withDoc = &catalog.Wallpapers[i]
			break
		}
	}
	if withDoc == nil {
		t.Fatalf("no wallpaper with a downloadable document")
	}
	assertEmbeddedSHA(t, withDoc.Document.Path, withDoc.Document.SHA256)
	if len(withDoc.Document.Thumbs) > 0 {
		assertEmbeddedSHA(t, withDoc.Document.Thumbs[0].Path, withDoc.Document.Thumbs[0].SHA256)
	}

	// Every chat theme has at least one setting with a wallpaper document (official themes all carry a theme background).
	for _, ct := range catalog.ChatThemes {
		if len(ct.Settings) == 0 {
			t.Fatalf("chat theme %q has no settings", ct.Emoticon)
		}
	}

	// The carried-over color palette structure stays unchanged.
	if catalog.PeerColors[0].Colors != nil {
		t.Fatalf("PeerColors[0].Colors = %+v, want nil default palette", catalog.PeerColors[0].Colors)
	}
	if catalog.PeerColors[7].Colors == nil || len(catalog.PeerColors[7].Colors.Colors) == 0 {
		t.Fatalf("PeerColors[7].Colors = %+v, want explicit palette", catalog.PeerColors[7].Colors)
	}
	if catalog.PeerProfileColors[0].Colors == nil || len(catalog.PeerProfileColors[0].Colors.StoryColors) != 2 {
		t.Fatalf("PeerProfileColors[0].Colors = %+v, want profile palette", catalog.PeerProfileColors[0].Colors)
	}
}

func assertEmbeddedSHA(t *testing.T, path, want string) {
	t.Helper()
	data, err := FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s sha256 = %s, want %s", path, got, want)
	}
}
