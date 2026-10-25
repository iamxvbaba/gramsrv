package files

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/domain"
)

func TestSeedSharedEffectPreservesPublishedStickerReference(t *testing.T) {
	for _, emoji := range []bool{false, true} {
		t.Run(map[bool]string{false: "sticker", true: "custom emoji"}[emoji], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			const setID, docID = int64(2562980648230846492), int64(7777777)
			const canonicalHash = int64(-4380212461388408925)
			setDir := filepath.Join(root, "telegram_stickers_export", "ReferencePack")
			writeInputSetTypeSeed(t, setDir, "", "ReferencePack", setID, docID, emoji)
			writeEffectsSeed(t, root, docID)
			path := filepath.Join(root, "telegram_effects_export", "effects.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the export's own JSON field names rather than reserializing the
			// decoder-only structs. This is the conflicting source-account reference.
			var effect map[string]any
			if err := json.Unmarshal(raw, &effect); err != nil {
				t.Fatal(err)
			}
			attrType := "DocumentAttributeSticker"
			if emoji {
				attrType = "DocumentAttributeCustomEmoji"
			}
			doc := effect["result"].(map[string]any)["documents"].([]any)[0].(map[string]any)
			doc["attributes"] = append(doc["attributes"].([]any), map[string]any{
				"_": attrType, "alt": "test", "stickerset": map[string]any{"id": setID, "access_hash": int64(1318408154723431293)},
			})
			raw, err = json.Marshal(effect)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			media := newFakeMediaStore()
			media.sets[setID] = domain.StickerSet{ID: setID, AccessHash: canonicalHash, Hash: 1}
			blobs, err := NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(media, blobs, 2)
			for i := 0; i < 2; i++ {
				if _, err := svc.SeedMedia(ctx, root, 0); err != nil {
					t.Fatal(err)
				}
				id, hash, ok := media.docs[docID].StickerSetRef()
				if !ok || id != setID || hash != canonicalHash || media.sets[setID].AccessHash != canonicalHash {
					t.Fatalf("seed %d: reference=%d/%d found=%v", i, id, hash, ok)
				}
			}
			before := svc.effectsHash
			d := media.docs[docID]
			for i := range d.Attributes {
				if d.Attributes[i].StickerSetID != 0 {
					d.Attributes[i].StickerSetAccessHash++
				}
			}
			media.docs[docID] = d
			if err := svc.seedEffects(ctx, root, &SeedStats{}); err != nil {
				t.Fatal(err)
			}
			if svc.effectsHash == before {
				t.Fatal("document reference change did not invalidate effects hash")
			}
		})
	}
}

type referenceFailureStore struct{ *fakeMediaStore }

func (referenceFailureStore) GetStickerSetByID(context.Context, int64) (domain.StickerSet, bool, error) {
	return domain.StickerSet{}, false, errors.New("catalog unavailable")
}

func TestSeedReferenceOwnerUnknownAndStoreFailure(t *testing.T) {
	ctx := context.Background()
	attrs := []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, StickerSetID: 10, StickerSetAccessHash: 20}}
	svc := NewService(newFakeMediaStore(), nil, 2)
	if err := svc.bindSeedStickerReferences(ctx, attrs, domain.StickerSetRef{}); err != nil {
		t.Fatal(err)
	}
	if attrs[0].StickerSetAccessHash != 20 {
		t.Fatal("invented reference for absent catalog")
	}
	if err := svc.bindSeedStickerReferences(ctx, attrs, domain.StickerSetRef{ID: 10, AccessHash: 30}); err != nil {
		t.Fatal(err)
	}
	if attrs[0].StickerSetAccessHash != 30 {
		t.Fatal("new owner credential not used")
	}
	svc = NewService(referenceFailureStore{newFakeMediaStore()}, nil, 2)
	if err := svc.bindSeedStickerReferences(ctx, attrs, domain.StickerSetRef{}); err == nil {
		t.Fatal("store failure swallowed")
	}
}
