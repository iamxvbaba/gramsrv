package files

import (
	"bytes"
	"testing"

	"telesrv/internal/domain"
)

func TestEffectsCatalogHashIncludesInlineThumbnailContents(t *testing.T) {
	for _, kind := range []domain.PhotoSizeKind{
		domain.PhotoSizeKindCached,
		domain.PhotoSizeKindPath,
		domain.PhotoSizeKindStripped,
	} {
		t.Run(string(kind), func(t *testing.T) {
			effects := []domain.AvailableEffect{{ID: 77, EffectStickerID: 1}}
			docs := []domain.Document{{ID: 1, Thumbs: []domain.PhotoSize{{Kind: kind, Type: "m", Bytes: []byte{1, 2, 3, 4}}}}}
			before := copyDocuments(docs)
			oldHash, err := effectsCatalogHash(effects, docs)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := effectsCatalogHash(effects, docs)
			if err != nil || repeated != oldHash {
				t.Fatalf("unchanged hash=%d, want %d, err=%v", repeated, oldHash, err)
			}
			if !bytes.Equal(docs[0].Thumbs[0].Bytes, before[0].Thumbs[0].Bytes) {
				t.Fatal("hash calculation modified the source thumbnail")
			}

			// The inline bytes of path/stripped are delivered directly; even with the same length the client catalog must be refreshed.
			docs[0].Thumbs[0].Bytes[3]++
			newHash, err := effectsCatalogHash(effects, docs)
			if err != nil {
				t.Fatal(err)
			}
			if newHash == oldHash {
				t.Fatalf("changed inline thumbnail kept catalog hash %d", newHash)
			}
		})
	}
}
