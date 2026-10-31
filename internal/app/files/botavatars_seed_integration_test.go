package files

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"testing"

	"telesrv/internal/app/files/botavatars"
	"telesrv/internal/domain"
)

// countingThumbnailer records whether the video thumbnailer was invoked by the
// transaction-scoped service. If SeedTx dropped thumbs, the animated-avatar
// still would fall back to a generated placeholder and Extract would be 0.
// It returns a valid small PNG so the downstream avatar rendition pipeline
// (which decodes the still) can proceed.
type countingThumbnailer struct {
	extractCalls int
}

func (c *countingThumbnailer) Extract(_ context.Context, _ []byte, _ string) ([]byte, error) {
	c.extractCalls++
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// TestBotAvatarsSeedViaRealService integration test: call botavatars.Seed through a real
// files.Service, a real permanent-blob backend and a real, separate upload-staging backend. Seed
// builds a transactional service internally via SeedTx — this ensures fields such as uploadParts /
// thumbs are fully preserved on the transactional service and that upload parts are written to the
// separate staging backend (not the main blob backend). Otherwise premiumbot.mp4's
// CreateAvatarVideoFromBytes would fail because the upload part backend is unconfigured, or the video
// static frame would fall back to a generated placeholder.
func TestBotAvatarsSeedViaRealService(t *testing.T) {
	ctx := context.Background()

	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs blob backend: %v", err)
	}
	// Independent upload-staging backend: a separate instance/directory from the permanent-blob backend.
	staging := &countingUploadPartBackend{LocalFS: mustLocalFS(t, "staging backend")}
	// The fake thumbnailer records that SeedTx preserved thumbs for the video static frame.
	thumb := &countingThumbnailer{}
	svc := NewService(media, blobs, 2, WithUploadPartBackend(staging), WithVideoThumbnailer(thumb))

	if err := botavatars.Seed(ctx, svc, 1750000000); err != nil {
		t.Fatalf("botavatars.Seed failed: %v", err)
	}
	// The official system account is seeded separately from Seed (its avatar
	// follows Server identity), so seed it here too with the bundled default.
	if _, err := botavatars.SeedOfficialSystemAvatar(ctx, svc, nil, 1750000000); err != nil {
		t.Fatalf("botavatars.SeedOfficialSystemAvatar failed: %v", err)
	}

	// premiumbot.mp4 goes through CreateAvatarVideoFromBytes -> SaveFilePart -> PutUploadPart.
	// uploadParts must be preserved by SeedTx and actually land on the separate staging backend.
	if staging.putUploadPartCalls == 0 {
		t.Fatalf("staging backend was never used: uploadParts not carried into SeedTx")
	}
	// The video static frame must be extracted from the real thumbnailer rather than falling back to a
	// generated placeholder — proving that SeedTx preserved thumbs.
	if thumb.extractCalls == 0 {
		t.Fatalf("video thumbnailer never invoked: thumbs not carried into SeedTx")
	}

	// Every system bot/account should have an avatar album and a current photo.
	for peerID := range botavatarsPeersForSeed() {
		ref, err := media.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, []int64{peerID}, domain.ProfilePhotoKindProfile)
		if err != nil {
			t.Fatalf("current profile photos for peer %d: %v", peerID, err)
		}
		entry, found := ref[peerID]
		if !found {
			t.Fatalf("peer %d has no current profile photo after seed", peerID)
		}
		photo, ok, err := media.GetPhoto(ctx, entry.PhotoID)
		if err != nil {
			t.Fatalf("get photo %d: %v", entry.PhotoID, err)
		}
		if !ok {
			t.Fatalf("peer %d photo %d missing after seed", peerID, entry.PhotoID)
		}
		if photo.DCID != 2 {
			t.Fatalf("peer %d photo dc_id = %d, want 2", peerID, photo.DCID)
		}
		// Every photo size must have a real downloadable blob.
		for _, size := range photo.Sizes {
			blob, found, err := media.GetFileBlob(ctx, photoBlobKey(photo.ID, size.Type))
			if err != nil {
				t.Fatalf("peer %d blob %d:%s: %v", peerID, photo.ID, size.Type, err)
			}
			if !found {
				t.Fatalf("peer %d blob %d:%s missing after seed", peerID, photo.ID, size.Type)
			}
			chunk, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: photoBlobKey(photo.ID, size.Type), Offset: 0, Limit: 1})
			if err != nil {
				t.Fatalf("peer %d getfile %d:%s: %v", peerID, photo.ID, size.Type, err)
			}
			if !found || chunk.Total != blob.Size || len(chunk.Bytes) == 0 {
				t.Fatalf("peer %d getfile %d:%s = found:%v total:%d got:%d want %d", peerID, photo.ID, size.Type, found, chunk.Total, len(chunk.Bytes), blob.Size)
			}
		}
	}

	// Idempotent: a second Seed does not error and no longer generates new photos. (The official
	// system account's avatar is seeded separately by SeedOfficialSystemAvatar and is reset on every
	// call, so it does not participate in the idempotency assertion here.)
	before := len(media.photos)
	if err := botavatars.Seed(ctx, svc, 1750000001); err != nil {
		t.Fatalf("second botavatars.Seed failed: %v", err)
	}
	if after := len(media.photos); after != before {
		t.Fatalf("idempotent seed created new photos: before=%d after=%d", before, after)
	}
}

func botavatarsPeersForSeed() map[int64]string {
	return map[int64]string{
		domain.OfficialSystemUserID:         "telegram.jpg",
		domain.BotFatherUserID:              "botfather.jpg",
		domain.StickersBotUserID:            "stickers.jpg",
		domain.VerifyBotUserID:              "verifybot.jpg",
		domain.GifBotUserID:                 "gif.jpg",
		domain.PremiumBotConfiguredUserID(): "premiumbot.mp4",
	}
}

func mustLocalFS(t *testing.T, desc string) *LocalFS {
	t.Helper()
	lfs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs %s: %v", desc, err)
	}
	return lfs
}

func photoBlobKey(photoID int64, typ string) string {
	return "photo:" + itoa(photoID) + ":" + typ
}
