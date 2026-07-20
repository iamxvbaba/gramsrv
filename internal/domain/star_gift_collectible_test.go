package domain

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
)

func TestSavedStarGiftRefRequiresOneOfficialIdentity(t *testing.T) {
	user := Peer{Type: PeerTypeUser, ID: 42}
	channel := Peer{Type: PeerTypeChannel, ID: 84}
	tests := []struct {
		name string
		ref  SavedStarGiftRef
		want bool
	}{
		{name: "user message", ref: SavedStarGiftRef{Owner: user, MsgID: 10}, want: true},
		{name: "channel saved id", ref: SavedStarGiftRef{Owner: channel, SavedID: 20}, want: true},
		{name: "user collectible slug", ref: SavedStarGiftRef{Owner: user, Slug: "official-42-1"}, want: true},
		{name: "channel collectible slug", ref: SavedStarGiftRef{Owner: channel, Slug: "official-84-1"}, want: true},
		{name: "message and slug", ref: SavedStarGiftRef{Owner: user, MsgID: 10, Slug: "official-42-1"}},
		{name: "saved id and slug", ref: SavedStarGiftRef{Owner: channel, SavedID: 20, Slug: "official-84-1"}},
		{name: "whitespace slug", ref: SavedStarGiftRef{Owner: user, Slug: " official-42-1"}},
		{name: "oversized slug", ref: SavedStarGiftRef{Owner: user, Slug: strings.Repeat("x", MaxStarGiftSlugBytes+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.Valid(); got != tt.want {
				t.Fatalf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStarGiftLifecycleStatusRequiresExplicitActive(t *testing.T) {
	if StarGiftLifecycleStatus("").Live() {
		t.Fatal("empty lifecycle status must not be treated as active")
	}
	if !StarGiftLifecycleActive.Live() {
		t.Fatal("active lifecycle status must be live")
	}
}

func validCollectibleDraft() StarGiftCollectibleWrite {
	animation := &StarGiftAnimation{JSON: []byte(`{}`), TGS: []byte{1}, SHA256: make([]byte, sha256.Size)}
	return StarGiftCollectibleWrite{
		GiftID: 1, UpgradeStars: 25, SupplyTotal: 100, SlugPrefix: "official-1", CommandID: "test",
		Models: []StarGiftCollectibleAttribute{
			{Kind: StarGiftCollectibleModel, Name: "Regular", RarityKind: StarGiftRarityPermille, RarityPermille: 922, Animation: animation},
			{Kind: StarGiftCollectibleModel, Name: "Crafted", RarityKind: StarGiftRarityLegendary, Crafted: true, Animation: animation},
		},
		Patterns: []StarGiftCollectibleAttribute{
			{Kind: StarGiftCollectiblePattern, Name: "Pattern", RarityKind: StarGiftRarityPermille, RarityPermille: 989, Animation: animation},
		},
		Backdrops: []StarGiftCollectibleAttribute{
			{Kind: StarGiftCollectibleBackdrop, Name: "Backdrop", BackdropID: 0, RarityKind: StarGiftRarityPermille, RarityPermille: 999},
		},
	}
}

func TestValidateStarGiftCollectibleDraftOfficialProvenance(t *testing.T) {
	write := validCollectibleDraft()
	write.OfficialGiftID = 10
	write.SourceManifestSHA256 = make([]byte, sha256.Size)
	if err := ValidateStarGiftCollectibleDraft(write); err != nil {
		t.Fatalf("valid official draft: %v", err)
	}

	tests := map[string]StarGiftCollectibleWrite{}
	withoutHash := write
	withoutHash.SourceManifestSHA256 = nil
	tests["official ID without hash"] = withoutHash
	withoutID := write
	withoutID.OfficialGiftID = 0
	tests["hash without official ID"] = withoutID
	negativeID := write
	negativeID.OfficialGiftID = -1
	tests["negative official ID"] = negativeID
	for name, invalid := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateStarGiftCollectibleDraft(invalid); !errors.Is(err, ErrStarGiftCollectibleInvalid) {
				t.Fatalf("err=%v, want ErrStarGiftCollectibleInvalid", err)
			}
		})
	}
}

func TestValidateStarGiftCollectibleDraftRejectsImplicitRarity(t *testing.T) {
	write := validCollectibleDraft()
	write.Models[0].RarityKind = ""
	if err := ValidateStarGiftCollectibleDraft(write); !errors.Is(err, ErrStarGiftCollectibleInvalid) {
		t.Fatalf("err=%v, want ErrStarGiftCollectibleInvalid", err)
	}
}

func storedCollectibleWrite() StarGiftCollectibleWrite {
	write := validCollectibleDraft()
	for i := range write.Models {
		write.Models[i].Document = &Document{
			ID: int64(100 + i), MimeType: "application/x-tgsticker",
			Attributes: []DocumentAttribute{{Kind: DocAttrSticker, Alt: "🎁"}},
		}
		write.Models[i].Blob = &FileBlob{LocationKey: "model"}
	}
	write.Patterns[0].Document = &Document{
		ID: 200, MimeType: "application/x-tgsticker",
		Attributes: []DocumentAttribute{{Kind: DocAttrCustomEmoji, Alt: "🎁", TextColor: true}},
		Thumbs:     []PhotoSize{{Kind: PhotoSizeKindPath, Type: "j", Bytes: []byte{1}}},
	}
	write.Patterns[0].Blob = &FileBlob{LocationKey: "pattern"}
	return write
}

func TestValidateStarGiftCollectibleWriteRequiresExactDocumentRoles(t *testing.T) {
	if err := ValidateStarGiftCollectibleWrite(storedCollectibleWrite()); err != nil {
		t.Fatalf("valid stored collectible: %v", err)
	}

	tests := map[string]func(*StarGiftCollectibleWrite){
		"pattern stored as sticker": func(write *StarGiftCollectibleWrite) {
			write.Patterns[0].Document.Attributes = []DocumentAttribute{{Kind: DocAttrSticker, Alt: "🎁"}}
		},
		"pattern custom emoji without text color": func(write *StarGiftCollectibleWrite) {
			write.Patterns[0].Document.Attributes[0].TextColor = false
		},
		"pattern without inline path thumb": func(write *StarGiftCollectibleWrite) {
			write.Patterns[0].Document.Thumbs = nil
		},
		"model stored as custom emoji": func(write *StarGiftCollectibleWrite) {
			write.Models[0].Document.Attributes = []DocumentAttribute{{Kind: DocAttrCustomEmoji, Alt: "🎁", TextColor: true}}
		},
		"ambiguous model render attributes": func(write *StarGiftCollectibleWrite) {
			write.Models[0].Document.Attributes = append(write.Models[0].Document.Attributes,
				DocumentAttribute{Kind: DocAttrCustomEmoji, Alt: "🎁", TextColor: true})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			write := storedCollectibleWrite()
			mutate(&write)
			if err := ValidateStarGiftCollectibleWrite(write); !errors.Is(err, ErrStarGiftCollectibleInvalid) {
				t.Fatalf("err=%v, want ErrStarGiftCollectibleInvalid", err)
			}
		})
	}
}
