package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

func TestSeedPremiumPromoImportsDownloadsSkipsAndRepairs(t *testing.T) {
	ctx := context.Background()
	root, videoBytes, thumbBytes := writePremiumPromoFixture(t)
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 7, WithVideoThumbnailer(nil), WithGIFTranscoder(nil))

	first, err := svc.SeedPremiumPromo(ctx, root)
	if err != nil {
		t.Fatalf("SeedPremiumPromo first: %v", err)
	}
	if first.Skipped || first.Videos != 1 || first.Blobs != 2 {
		t.Fatalf("first stats = %+v, want one video and two blobs", first)
	}
	catalog, found, err := svc.PremiumPromo(ctx)
	if err != nil || !found {
		t.Fatalf("PremiumPromo found=%v err=%v", found, err)
	}
	if len(catalog.VideoSections) != 1 || catalog.VideoSections[0] != "no_ads" || len(catalog.Videos) != 1 {
		t.Fatalf("catalog = %+v", catalog)
	}
	doc := catalog.Videos[0]
	if doc.DCID != 7 || doc.MimeType != "video/mp4" || len(doc.Thumbs) != 1 || doc.Thumbs[0].Type != "m" {
		t.Fatalf("document = %+v", doc)
	}

	main, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("doc:%d", doc.ID),
		Limit:       len(videoBytes) + 1,
	})
	if err != nil || !ok {
		t.Fatalf("download main ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(main.Bytes, videoBytes) {
		t.Fatalf("downloaded main = %x, want %x", main.Bytes, videoBytes)
	}
	thumb, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("doc:%d:%s", doc.ID, doc.Thumbs[0].Type),
		Limit:       len(thumbBytes) + 1,
	})
	if err != nil || !ok {
		t.Fatalf("download thumb ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(thumb.Bytes, thumbBytes) {
		t.Fatalf("downloaded thumb differs: got %d bytes, want %d", len(thumb.Bytes), len(thumbBytes))
	}

	// Returned values are request-owned: mutating one response must not corrupt
	// the immutable catalog seen by later/concurrent requests.
	catalog.VideoSections[0] = "mutated"
	catalog.Videos[0].FileReference[0] ^= 0xff
	catalog.Videos[0].Thumbs[0].Type = "z"
	again, found, err := svc.PremiumPromo(ctx)
	if err != nil || !found {
		t.Fatalf("PremiumPromo again found=%v err=%v", found, err)
	}
	if again.VideoSections[0] != "no_ads" || again.Videos[0].Thumbs[0].Type != "m" || again.Videos[0].FileReference[0] != 0 {
		t.Fatalf("catalog was mutated through returned value: %+v", again)
	}

	var readers sync.WaitGroup
	readerErrors := make(chan error, 32)
	for i := 0; i < 32; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			got, found, err := svc.PremiumPromo(ctx)
			if err != nil || !found || len(got.Videos) != 1 {
				readerErrors <- fmt.Errorf("found=%v videos=%d err=%v", found, len(got.Videos), err)
				return
			}
			got.VideoSections[0] = "request-owned"
			got.Videos[0].FileReference[0] = 0x7f
		}()
	}
	readers.Wait()
	close(readerErrors)
	for err := range readerErrors {
		t.Error(err)
	}

	second, err := svc.SeedPremiumPromo(ctx, root)
	if err != nil {
		t.Fatalf("SeedPremiumPromo unchanged: %v", err)
	}
	if !second.Skipped || second.Videos != 1 || second.Blobs != 0 {
		t.Fatalf("unchanged stats = %+v, want skipped catalog", second)
	}

	mainKey := fmt.Sprintf("doc:%d", doc.ID)
	media.mu.Lock()
	delete(media.blobs, mainKey)
	media.mu.Unlock()
	repaired, err := svc.SeedPremiumPromo(ctx, root)
	if err != nil {
		t.Fatalf("SeedPremiumPromo repair: %v", err)
	}
	if repaired.Skipped || repaired.Videos != 1 || repaired.Blobs != 1 {
		t.Fatalf("repair stats = %+v, want one repaired blob", repaired)
	}
	if _, ok, err := media.GetFileBlob(ctx, mainKey); err != nil || !ok {
		t.Fatalf("repaired main blob ok=%v err=%v", ok, err)
	}
}

func TestSeedPremiumPromoMissingAndInvalidSources(t *testing.T) {
	ctx := context.Background()
	newService := func(t *testing.T) *Service {
		t.Helper()
		blobs, err := NewLocalFS(t.TempDir())
		if err != nil {
			t.Fatalf("NewLocalFS: %v", err)
		}
		return NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil), WithGIFTranscoder(nil))
	}

	t.Run("missing directory falls back", func(t *testing.T) {
		svc := newService(t)
		stats, err := svc.SeedPremiumPromo(ctx, filepath.Join(t.TempDir(), "missing"))
		if err != nil || !stats.Skipped {
			t.Fatalf("stats=%+v err=%v, want optional-resource fallback", stats, err)
		}
		if _, found, err := svc.PremiumPromo(ctx); err != nil || found {
			t.Fatalf("PremiumPromo found=%v err=%v, want unavailable", found, err)
		}
	})

	t.Run("existing directory without manifest fails", func(t *testing.T) {
		svc := newService(t)
		if _, err := svc.SeedPremiumPromo(ctx, t.TempDir()); err == nil {
			t.Fatal("existing incomplete seed directory was accepted")
		}
	})

	t.Run("positional vectors must match", func(t *testing.T) {
		root, _, _ := writePremiumPromoFixture(t)
		rewritePremiumPromoManifest(t, root, func(m map[string]any) {
			m["video_sections"] = []string{"no_ads", "extra"}
		})
		if _, err := newService(t).SeedPremiumPromo(ctx, root); err == nil {
			t.Fatal("mismatched video_sections/videos was accepted")
		}
	})

	t.Run("manifest size must match file", func(t *testing.T) {
		root, _, _ := writePremiumPromoFixture(t)
		rewritePremiumPromoManifest(t, root, func(m map[string]any) {
			videos := m["videos"].([]any)
			videos[0].(map[string]any)["size"] = float64(999)
		})
		if _, err := newService(t).SeedPremiumPromo(ctx, root); err == nil {
			t.Fatal("wrong video size was accepted")
		}
	})

	t.Run("missing thumbnail fails", func(t *testing.T) {
		root, _, _ := writePremiumPromoFixture(t)
		thumbPath := filepath.Join(root, "thumbs", "1000000000000001.jpg")
		if err := os.Remove(thumbPath); err != nil {
			t.Fatal(err)
		}
		if _, err := newService(t).SeedPremiumPromo(ctx, root); err == nil {
			t.Fatal("missing thumbnail was accepted")
		}
	})
}

func TestSeedPremiumPromoFromRealExport(t *testing.T) {
	root := os.Getenv("TELESRV_REAL_PREMIUM_PROMO_SEED_DIR")
	if root == "" {
		t.Skip("TELESRV_REAL_PREMIUM_PROMO_SEED_DIR not set")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("seed dir %s not present: %v", root, err)
	}
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil), WithGIFTranscoder(nil))
	stats, err := svc.SeedPremiumPromo(context.Background(), root)
	if err != nil {
		t.Fatalf("SeedPremiumPromo: %v", err)
	}
	catalog, found, err := svc.PremiumPromo(context.Background())
	if err != nil || !found {
		t.Fatalf("PremiumPromo found=%v err=%v", found, err)
	}
	if stats.Videos != 31 || len(catalog.VideoSections) != 31 || len(catalog.Videos) != 31 {
		t.Fatalf("stats=%+v sections=%d videos=%d, want 31", stats, len(catalog.VideoSections), len(catalog.Videos))
	}
	t.Logf("real premium promo seed: videos=%d blobs=%d", stats.Videos, stats.Blobs)
}

func writePremiumPromoFixture(t *testing.T) (string, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "thumbs"), 0o755); err != nil {
		t.Fatal(err)
	}
	const documentID int64 = 1000000000000001
	videoBytes := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(root, "documents", fmt.Sprintf("%d.mp4", documentID)), videoBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 160, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 160; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x88, A: 0xff})
		}
	}
	var thumb bytes.Buffer
	if err := jpeg.Encode(&thumb, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	thumbBytes := thumb.Bytes()
	if err := os.WriteFile(filepath.Join(root, "thumbs", fmt.Sprintf("%d.jpg", documentID)), thumbBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := premiumPromoSeedJSON{
		APICall:       "help.getPremiumPromo",
		StatusText:    "ignored source status",
		VideoSections: []string{"no_ads"},
		Videos: []seedDocumentJSON{{
			ID:            documentID,
			AccessHash:    -7,
			FileReference: "00112233445566778899aabbccddeeff",
			Date:          "2026-01-02T03:04:05Z",
			MimeType:      "video/mp4",
			Size:          int64(len(videoBytes)),
			DCID:          4,
			Attributes: []seedAttrJSON{
				{Type: "DocumentAttributeFilename", FileName: "promo.mp4"},
				{Type: "DocumentAttributeVideo", W: 720, H: 1070, Duration: 5, SupportsStreaming: true},
				{Type: "DocumentAttributeAnimated"},
			},
		}},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, premiumPromoManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, append([]byte(nil), videoBytes...), append([]byte(nil), thumbBytes...)
}

func rewritePremiumPromoManifest(t *testing.T, root string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, premiumPromoManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	mutate(manifest)
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
