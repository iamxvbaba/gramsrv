package stargifts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

const validGiftLottie = `{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4,"nm":"gift"}],"assets":[]}`

func TestPrepareAnimationNormalizesLottieAndTGS(t *testing.T) {
	fromJSON, err := prepareAnimation("gift.lottie", []byte(" \n"+validGiftLottie+"\n"))
	if err != nil {
		t.Fatalf("prepare lottie: %v", err)
	}
	if fromJSON.SourceFormat != domain.StarGiftAnimationLottie || len(fromJSON.TGS) == 0 || fromJSON.Width != 512 || fromJSON.Height != 512 {
		t.Fatalf("prepared lottie = %+v", fromJSON)
	}
	fromTGS, err := prepareAnimation("gift.tgs", fromJSON.TGS)
	if err != nil {
		t.Fatalf("prepare tgs: %v", err)
	}
	if fromTGS.SourceFormat != domain.StarGiftAnimationTGS || string(fromTGS.JSON) != string(fromJSON.JSON) || hex.EncodeToString(fromTGS.SHA256) != hex.EncodeToString(fromJSON.SHA256) {
		t.Fatalf("tgs round trip differs: json=%v hash=%x/%x", string(fromTGS.JSON) == string(fromJSON.JSON), fromTGS.SHA256, fromJSON.SHA256)
	}
}

func TestPrepareAnimationRejectsExternalAssetAndExpression(t *testing.T) {
	for name, raw := range map[string]string{
		"external":   `{"v":"5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{}],"assets":[{"p":"https://example.test/x.png"}]}`,
		"expression": `{"v":"5.7","fr":30,"ip":0,"op":30,"w":512,"h":512,"layers":[{"ks":{"o":{"x":"time*10"}}}]}`,
		"wrong-size": `{"v":"5.7","fr":30,"ip":0,"op":30,"w":256,"h":256,"layers":[{}]}`,
		"frame-rate": `{"v":"5.7","fr":121,"ip":0,"op":30,"w":512,"h":512,"layers":[{}]}`,
		"duration":   `{"v":"5.7","fr":30,"ip":0,"op":901,"w":512,"h":512,"layers":[{}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareAnimation("gift.json", []byte(raw)); !errors.Is(err, domain.ErrStarGiftFileInvalid) {
				t.Fatalf("err=%v, want ErrStarGiftFileInvalid", err)
			}
		})
	}
}

type testGiftBlob struct{ data map[string][]byte }

func (b *testGiftBlob) Name() string { return "localfs" }
func (b *testGiftBlob) Put(_ context.Context, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	b.data[key] = append([]byte(nil), data...)
	return key, nil
}
func (b *testGiftBlob) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), b.data[key]...), nil
}

func TestCreateCatalogRevisionPreservesHistoricalRevision(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStarGiftStore()
	svc := NewService(store, &testGiftBlob{data: map[string][]byte{}}, 2)
	animation, err := svc.PrepareAnimation("gift.json", []byte(validGiftLottie))
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Stars: 50, ConvertStars: 25, Enabled: true, SortOrder: 1, Title: "Telegram Pin", Animation: animation,
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		GiftID: first.Gift.ID, Stars: 80, ConvertStars: 40, Enabled: true, SortOrder: 1, Title: "Second", Animation: animation,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	current, found, _ := svc.GiftByID(ctx, first.Gift.ID)
	if !found || current.RevisionID != second.Gift.RevisionID || current.Stars != 80 {
		t.Fatalf("current=%+v found=%v", current, found)
	}
	historical, found, _ := svc.GiftRevisionByID(ctx, first.Gift.RevisionID)
	if !found || historical.Stars != 50 || historical.Title != "Telesrv Pin" {
		t.Fatalf("historical=%+v found=%v", historical, found)
	}
	if _, err := svc.SetCatalogEnabled(ctx, first.Gift.ID+999, false); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("disable missing err=%v, want ErrStarGiftNotFound", err)
	}
}

func TestCreateCatalogBundleRejectsMismatchedOfficialProvenance(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStarGiftStore()
	svc := NewService(store, &testGiftBlob{data: map[string][]byte{}}, 2)
	animation, err := svc.PrepareAnimation("gift.json", []byte(validGiftLottie))
	if err != nil {
		t.Fatal(err)
	}
	hash := make([]byte, sha256.Size)
	_, err = svc.CreateCatalogBundle(ctx, domain.StarGiftCatalogBundleWrite{
		Catalog: domain.StarGiftCatalogWrite{
			Stars: 50, ConvertStars: 25, Enabled: true, Title: "Official", Animation: animation,
			OfficialGiftID: 10, SourceManifestSHA256: hash, OfficialSourceJSON: []byte(`{"id":10}`),
		},
		Collectible: &domain.StarGiftCollectibleWrite{
			OfficialGiftID: 11, SourceManifestSHA256: hash,
		},
	})
	if !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("mismatched provenance err=%v, want ErrStarGiftCollectibleInvalid", err)
	}
}

func TestCreateCatalogBundleMaterializesPublishableCollectibleDocuments(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStarGiftStore()
	svc := NewService(store, &testGiftBlob{data: map[string][]byte{}}, 2)
	animation, err := svc.PrepareOfficialAnimation("official.json", []byte(validGiftLottie))
	if err != nil {
		t.Fatal(err)
	}
	manifestSHA := make([]byte, sha256.Size)
	result, err := svc.CreateCatalogBundle(ctx, domain.StarGiftCatalogBundleWrite{
		Catalog: domain.StarGiftCatalogWrite{
			Title: "Official", Stars: 50, ConvertStars: 25, Enabled: true, Animation: animation,
			Actor: "test", CommandID: "official-catalog", OfficialGiftID: 10,
			SourceManifestSHA256: manifestSHA, OfficialSourceJSON: []byte(`{"id":10}`),
		},
		Collectible: &domain.StarGiftCollectibleWrite{
			UpgradeStars: 100, SupplyTotal: 1000, SlugPrefix: "official-10",
			Models: []domain.StarGiftCollectibleAttribute{{
				Kind: domain.StarGiftCollectibleModel, Name: "Model", RarityKind: domain.StarGiftRarityPermille,
				RarityPermille: 1000, Animation: &animation,
			}},
			Patterns: []domain.StarGiftCollectibleAttribute{{
				Kind: domain.StarGiftCollectiblePattern, Name: "Pattern", RarityKind: domain.StarGiftRarityPermille,
				RarityPermille: 1000, Animation: &animation,
			}},
			Backdrops: []domain.StarGiftCollectibleAttribute{{
				Kind: domain.StarGiftCollectibleBackdrop, Name: "Backdrop", RarityKind: domain.StarGiftRarityPermille,
				RarityPermille: 1000,
			}},
			Actor: "test", CommandID: "official-pool", OfficialGiftID: 10,
			SourceManifestSHA256: manifestSHA,
		},
	})
	if err != nil {
		t.Fatalf("create official collectible bundle: %v", err)
	}
	if result.Collectible == nil || len(result.Collectible.Models) != 1 || len(result.Collectible.Patterns) != 1 {
		t.Fatalf("collectible result = %+v", result.Collectible)
	}
	model := result.Collectible.Models[0].Document
	pattern := result.Collectible.Patterns[0].Document
	if model == nil || !model.IsSticker() || model.IsCustomEmoji() {
		t.Fatalf("model document = %+v, want ordinary sticker", model)
	}
	if pattern == nil || pattern.IsSticker() || !pattern.IsCustomEmoji() || len(pattern.Thumbs) != 1 ||
		pattern.Thumbs[0].Kind != domain.PhotoSizeKindPath || len(pattern.Thumbs[0].Bytes) == 0 {
		t.Fatalf("pattern document = %+v, want text-color custom emoji with inline path", pattern)
	}
	if !pattern.Attributes[1].TextColor {
		t.Fatalf("pattern render attribute = %+v, want text_color", pattern.Attributes[1])
	}
}
