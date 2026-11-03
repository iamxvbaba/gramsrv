package files

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"reflect"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestCreateDocumentFromUploadNormalizesGIFToGIFv(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("fake-mp4"), Width: 320, Height: 240, Duration: 1.5}}
	svc := NewService(media, blobs, 2, WithGIFTranscoder(transcoder), WithVideoThumbnailer(nil))
	if _, err := svc.SaveFilePart(ctx, 10, 90, 0, []byte("GIF89a-fake")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	doc, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 90, Parts: 1, Name: "animation.gif"},
		domain.DocumentSpec{MimeType: "image/gif", Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrFilename, FileName: "animation.gif"},
			{Kind: domain.DocAttrImageSize, W: 320, H: 240},
		}})
	if err != nil {
		t.Fatalf("CreateDocumentFromUpload: %v", err)
	}
	if transcoder.calls != 1 || doc.MimeType != "video/mp4" || !doc.IsGif() || doc.Size != int64(len("fake-mp4")) {
		t.Fatalf("document = %+v calls=%d, want canonical GIFv", doc, transcoder.calls)
	}
	blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
	if err != nil || !ok || blob.MimeType != "video/mp4" {
		t.Fatalf("gifv blob = %+v ok=%v err=%v", blob, ok, err)
	}
	got, err := blobs.Get(ctx, blob.ObjectKey)
	if err != nil || !bytes.Equal(got, []byte("fake-mp4")) {
		t.Fatalf("gifv bytes=%q err=%v", got, err)
	}
}

func TestCreateDocumentFromUploadGIFFlags(t *testing.T) {
	tests := []struct {
		name      string
		spec      domain.DocumentSpec
		wantMime  string
		wantGIF   bool
		wantCalls int
	}{
		{name: "album nosound is normal video", spec: domain.DocumentSpec{MimeType: "image/gif", NosoundVideo: true}, wantMime: "video/mp4", wantGIF: false, wantCalls: 1},
		{name: "force file preserves original", spec: domain.DocumentSpec{MimeType: "image/gif", ForceFile: true}, wantMime: "image/gif", wantGIF: false, wantCalls: 0},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			media := newFakeMediaStore()
			blobs, err := NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("mp4"), Width: 10, Height: 12, Duration: 1}}
			svc := NewService(media, blobs, 2, WithGIFTranscoder(transcoder), WithVideoThumbnailer(nil))
			fileID := int64(910 + i)
			if _, err := svc.SaveFilePart(ctx, 10, fileID, 0, []byte("gif")); err != nil {
				t.Fatal(err)
			}
			doc, err := svc.CreateDocumentFromUpload(ctx, domain.UploadedFileRef{OwnerUserID: 10, FileID: fileID, Parts: 1}, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if doc.MimeType != tc.wantMime || doc.IsGif() != tc.wantGIF || transcoder.calls != tc.wantCalls {
				t.Fatalf("doc=%+v calls=%d", doc, transcoder.calls)
			}
		})
	}
}

func TestCreateDocumentFromUploadGeneratesVideoThumbWhenMissing(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	thumbBytes := testJPEG(t, 4, 2)
	thumbnailer := &fakeVideoThumbnailer{thumb: thumbBytes}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))

	if _, err := svc.SaveFilePart(ctx, 10, 100, 0, []byte("fake-video-bytes")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	doc, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 100, Parts: 1, Name: "video.mp4"},
		domain.DocumentSpec{
			MimeType:   "video/mp4",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 1}},
		})
	if err != nil {
		t.Fatalf("CreateDocumentFromUpload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Fatalf("thumbnailer calls = %d, want 1", thumbnailer.calls)
	}
	if len(doc.Thumbs) != 1 {
		t.Fatalf("thumbs = %+v, want one generated thumbnail", doc.Thumbs)
	}
	if got := doc.Thumbs[0]; got.Type != "m" || got.W != 4 || got.H != 2 || got.Size != len(thumbBytes) {
		t.Fatalf("thumb = %+v, want m 4x2 size=%d", got, len(thumbBytes))
	}
	blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("doc:%d:m", doc.ID))
	if err != nil || !ok {
		t.Fatalf("generated thumb blob ok=%v err=%v", ok, err)
	}
	gotBytes, err := blobs.Get(ctx, blob.ObjectKey)
	if err != nil {
		t.Fatalf("read generated thumb blob: %v", err)
	}
	if !bytes.Equal(gotBytes, thumbBytes) {
		t.Fatalf("generated thumb bytes mismatch")
	}
}

func TestCreateDocumentFromUploadKeepsClientThumb(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	thumbnailer := &fakeVideoThumbnailer{err: errors.New("should not be called")}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))
	clientThumb := testJPEG(t, 3, 5)

	if _, err := svc.SaveFilePart(ctx, 10, 200, 0, []byte("fake-video-bytes")); err != nil {
		t.Fatalf("SaveFilePart video: %v", err)
	}
	if _, err := svc.SaveFilePart(ctx, 10, 201, 0, clientThumb); err != nil {
		t.Fatalf("SaveFilePart thumb: %v", err)
	}
	doc, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 200, Parts: 1, Name: "video.mp4"},
		domain.DocumentSpec{
			MimeType:   "video/mp4",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 1}},
			Thumb:      &domain.UploadedFileRef{OwnerUserID: 10, FileID: 201, Parts: 1, Name: "thumb.jpg"},
		})
	if err != nil {
		t.Fatalf("CreateDocumentFromUpload: %v", err)
	}
	if thumbnailer.calls != 0 {
		t.Fatalf("thumbnailer calls = %d, want 0 when client thumb is available", thumbnailer.calls)
	}
	if len(doc.Thumbs) != 1 {
		t.Fatalf("thumbs = %+v, want client thumbnail", doc.Thumbs)
	}
	if got := doc.Thumbs[0]; got.W != 3 || got.H != 5 || got.Size != len(clientThumb) {
		t.Fatalf("thumb = %+v, want client thumb dimensions", got)
	}
}

func TestCreateDocumentFromUploadWithoutThumbnailerDoesNotBlockVideo(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil))

	if _, err := svc.SaveFilePart(ctx, 10, 300, 0, []byte("fake-video-bytes")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	doc, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 300, Parts: 1, Name: "video.mp4"},
		domain.DocumentSpec{
			MimeType:   "video/mp4",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 1}},
		})
	if err != nil {
		t.Fatalf("CreateDocumentFromUpload without thumbnailer: %v", err)
	}
	if len(doc.Thumbs) != 0 {
		t.Fatalf("thumbs = %+v, want no fallback thumbnail when thumbnailer is disabled", doc.Thumbs)
	}
}

func TestCreatePhotoFromBytesStoresDownloadableMessageSizes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)
	data := testJPEG(t, 16, 9)

	photo, err := svc.CreatePhotoFromBytes(ctx, data)
	if err != nil {
		t.Fatalf("CreatePhotoFromBytes: %v", err)
	}
	if photo.ID == 0 || photo.AccessHash == 0 || photo.DCID != 2 || len(photo.Sizes) != 2 {
		t.Fatalf("photo = %+v, want stored photo with message sizes", photo)
	}
	blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:x", photo.ID))
	if err != nil || !ok {
		t.Fatalf("photo blob ok=%v err=%v", ok, err)
	}
	got, err := blobs.Get(ctx, blob.ObjectKey)
	if err != nil {
		t.Fatalf("read photo blob: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("photo blob bytes mismatch")
	}
}

func TestCreateAvatarFromUploadStoresRealSizedRenditions(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)
	data := testJPEG(t, 640, 480)
	if _, err := svc.SaveFilePart(ctx, 10, 301, 0, data); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}

	photo, err := svc.CreateAvatarFromUpload(ctx, domain.UploadedFileRef{
		OwnerUserID: 10,
		FileID:      301,
		Parts:       1,
		Name:        "avatar.jpg",
	})
	if err != nil {
		t.Fatalf("CreateAvatarFromUpload: %v", err)
	}
	wants := map[string]image.Point{
		"s": {X: 150, Y: 150},
		"a": {X: 160, Y: 160},
		"c": {X: 640, Y: 480},
	}
	if len(photo.Sizes) != len(wants) {
		t.Fatalf("avatar sizes = %+v, want s/a/c", photo.Sizes)
	}
	objectKeys := map[string]struct{}{}
	for _, size := range photo.Sizes {
		want, ok := wants[size.Type]
		if !ok || size.W != want.X || size.H != want.Y {
			t.Fatalf("avatar size = %+v, want one of %v", size, wants)
		}
		assertAvatarImageSize(t, svc, photo.ID, size.Type, want.X, want.Y, "image/jpeg")
		blob, found, err := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:%s", photo.ID, size.Type))
		if err != nil || !found {
			t.Fatalf("avatar %s blob found=%v err=%v", size.Type, found, err)
		}
		if blob.Size != int64(size.Size) {
			t.Fatalf("avatar %s blob size=%d metadata size=%d", size.Type, blob.Size, size.Size)
		}
		objectKeys[blob.ObjectKey] = struct{}{}
	}
	if len(objectKeys) != 3 {
		t.Fatalf("avatar object keys = %v, want distinct s/a/c renditions", objectKeys)
	}
}

func TestCreateDocumentFromBytesStoresBodyAndAttributes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil))
	data := []byte("inline document body")
	spec := domain.DocumentSpec{
		MimeType: "application/pdf",
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrFilename, FileName: "inline.pdf"},
		},
	}

	doc, err := svc.CreateDocumentFromBytes(ctx, data, spec)
	if err != nil {
		t.Fatalf("CreateDocumentFromBytes: %v", err)
	}
	if doc.ID == 0 || doc.AccessHash == 0 || doc.Size != int64(len(data)) || doc.MimeType != "application/pdf" || len(doc.Attributes) != 1 {
		t.Fatalf("document = %+v, want stored document body and attributes", doc)
	}
	blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
	if err != nil || !ok {
		t.Fatalf("document blob ok=%v err=%v", ok, err)
	}
	got, err := blobs.Get(ctx, blob.ObjectKey)
	if err != nil {
		t.Fatalf("read document blob: %v", err)
	}
	if !bytes.Equal(got, data) || blob.MimeType != "application/pdf" {
		t.Fatalf("document blob mime=%q bytes=%q", blob.MimeType, got)
	}
}

func TestCreateDocumentFromBytesNormalizesExternalGIF(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("external-mp4"), Width: 20, Height: 10, Duration: 2}}
	svc := NewService(media, blobs, 2, WithGIFTranscoder(transcoder), WithVideoThumbnailer(nil))
	doc, err := svc.CreateDocumentFromBytes(ctx, []byte("external-gif"), domain.DocumentSpec{
		MimeType: "image/gif", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "x.gif"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !doc.IsGif() || doc.MimeType != "video/mp4" || transcoder.calls != 1 {
		t.Fatalf("doc=%+v calls=%d", doc, transcoder.calls)
	}
}

func TestCreateAvatarMarkupGeneratesDownloadableStaticSizes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)

	photo, err := svc.CreateAvatarMarkup(ctx, domain.PhotoSize{
		Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
		EmojiID:          99,
		BackgroundColors: []int{0xff3b30, 0x34c759},
	})
	if err != nil {
		t.Fatalf("CreateAvatarMarkup: %v", err)
	}
	if !domain.PhotoHasVideo(photo.Sizes) {
		t.Fatalf("avatar markup photo sizes = %+v, want video markup", photo.Sizes)
	}
	assertDownloadableAvatarSize(t, svc, photo.ID, "s", 150)
	assertDownloadableAvatarSize(t, svc, photo.ID, "a", 160)
	assertDownloadableAvatarSize(t, svc, photo.ID, "c", 640)
}

// TestCreateAvatarMarkupComposesEmojiThumbIntoStaticSizes guards two behaviors:
//  1. A regular color emoji composed into a static avatar keeps its original color (must not be
//     whitened/blackened);
//  2. An overlay with non-full alpha pixels (the norm for anti-aliasing) must not turn whole-black
//     from premultiplication overflow — feeding the illegal R=G=B=255, A<255 premultiplied value to
//     draw.Over once overflowed and wrapped, outputting the emoji as near-black.
func TestCreateAvatarMarkupComposesEmojiThumbIntoStaticSizes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	const emojiID = int64(99)
	if err := media.PutDocument(ctx, domain.Document{
		ID:       emojiID,
		MimeType: "application/x-tgsticker",
		Thumbs: []domain.PhotoSize{{
			Kind:  domain.PhotoSizeKindCached,
			Type:  "m",
			W:     64,
			H:     64,
			Bytes: testTransparentThumbPNG(t),
		}},
	}); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	svc := NewService(media, blobs, 2)

	photo, err := svc.CreateAvatarMarkup(ctx, domain.PhotoSize{
		Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
		EmojiID:          emojiID,
		BackgroundColors: []int{0x112233, 0x445566},
	})
	if err != nil {
		t.Fatalf("CreateAvatarMarkup: %v", err)
	}

	r, g, b, a := avatarStillCenterPixel(t, svc, photo.ID)
	if a < 250 {
		t.Fatalf("center pixel alpha=%d, want opaque still", a)
	}
	if r < 200 || g > 90 || b > 90 {
		t.Fatalf("center pixel rgb=(%d,%d,%d), want red emoji color preserved", r, g, b)
	}
}

// TestCreateAvatarMarkupTintsTextColorEmojiWhite guards the white-silhouette presentation of a
// text_color custom emoji: tinting must write legal premultiplied values (R=G=B=A), and non-full
// alpha pixels must not overflow into black.
func TestCreateAvatarMarkupTintsTextColorEmojiWhite(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	const emojiID = int64(120)
	if err := media.PutDocument(ctx, domain.Document{
		ID:       emojiID,
		MimeType: "application/x-tgsticker",
		Attributes: []domain.DocumentAttribute{{
			Kind:      domain.DocAttrCustomEmoji,
			TextColor: true,
		}},
		Thumbs: []domain.PhotoSize{{
			Kind:  domain.PhotoSizeKindCached,
			Type:  "m",
			W:     64,
			H:     64,
			Bytes: testTransparentThumbPNG(t),
		}},
	}); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	svc := NewService(media, blobs, 2)

	photo, err := svc.CreateAvatarMarkup(ctx, domain.PhotoSize{
		Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
		EmojiID:          emojiID,
		BackgroundColors: []int{0x112233, 0x445566},
	})
	if err != nil {
		t.Fatalf("CreateAvatarMarkup: %v", err)
	}

	r, g, b, _ := avatarStillCenterPixel(t, svc, photo.ID)
	if r < 230 || g < 230 || b < 230 {
		t.Fatalf("center pixel rgb=(%d,%d,%d), want white silhouette for text_color emoji", r, g, b)
	}
}

func avatarStillCenterPixel(t *testing.T, svc *Service, photoID int64) (r, g, b, a uint32) {
	t.Helper()
	chunk, found, err := svc.GetFile(context.Background(), domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("photo:%d:c", photoID),
		Offset:      0,
		Limit:       1 << 20,
	})
	if err != nil || !found {
		t.Fatalf("avatar c blob found=%v err=%v", found, err)
	}
	img, _, err := image.Decode(bytes.NewReader(chunk.Bytes))
	if err != nil {
		t.Fatalf("decode avatar still: %v", err)
	}
	r, g, b, a = img.At(avatarStillSize/2, avatarStillSize/2).RGBA()
	return r >> 8, g >> 8, b >> 8, a >> 8
}

func TestCreateAvatarVideoMarkupGeneratesDownloadableStaticSizes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)

	if _, err := svc.SaveFilePart(ctx, 10, 400, 0, []byte("fake-profile-video")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	photo, err := svc.CreateAvatarVideoMarkupFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 400, Parts: 1, Name: "avatar.mp4"},
		0.25,
		domain.PhotoSize{
			Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
			EmojiID:          100,
			BackgroundColors: []int{0x536dfe, 0x26a69a},
		})
	if err != nil {
		t.Fatalf("CreateAvatarVideoMarkupFromUpload: %v", err)
	}
	assertDownloadableAvatarSize(t, svc, photo.ID, "s", 150)
	assertDownloadableAvatarSize(t, svc, photo.ID, "a", 160)
	assertDownloadableAvatarSize(t, svc, photo.ID, "c", 640)
	chunk, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("photo:%d:u", photo.ID),
		Offset:      0,
		Limit:       1024,
	})
	if err != nil || !found {
		t.Fatalf("video avatar blob found=%v err=%v", found, err)
	}
	if string(chunk.Bytes) != "fake-profile-video" {
		t.Fatalf("video avatar bytes = %q", chunk.Bytes)
	}
}

// TestCreateAvatarVideoMarkupFallsBackToVideoFirstFrame guards that a frame can still be extracted
// from the uploaded video when the markup document/thumb is unavailable, so an ordinary animated
// avatar does not lose its static sizes.
func TestCreateAvatarVideoMarkupFallsBackToVideoFirstFrame(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	frame := testJPEG(t, 640, 640)
	thumbnailer := &fakeVideoThumbnailer{thumb: frame}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))

	if _, err := svc.SaveFilePart(ctx, 10, 500, 0, []byte("fake-profile-video")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	photo, err := svc.CreateAvatarVideoMarkupFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 500, Parts: 1, Name: "avatar.mp4"},
		0,
		domain.PhotoSize{
			Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
			EmojiID:          77,
			BackgroundColors: []int{0x112233},
		})
	if err != nil {
		t.Fatalf("CreateAvatarVideoMarkupFromUpload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Fatalf("thumbnailer calls = %d, want 1", thumbnailer.calls)
	}
	chunk, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("photo:%d:c", photo.ID),
		Offset:      0,
		Limit:       1 << 20,
	})
	if err != nil || !found {
		t.Fatalf("avatar a blob found=%v err=%v", found, err)
	}
	if !bytes.Equal(chunk.Bytes, frame) {
		t.Fatalf("avatar still bytes != extracted first frame (got %d bytes, want %d)", len(chunk.Bytes), len(frame))
	}
	if chunk.MimeType != "image/jpeg" {
		t.Fatalf("avatar still mime = %q, want image/jpeg from extracted frame", chunk.MimeType)
	}
	assertAvatarImageSize(t, svc, photo.ID, "s", 150, 150, "image/jpeg")
	assertAvatarImageSize(t, svc, photo.ID, "a", 160, 160, "image/jpeg")
}

func TestCreateAvatarVideoMarkupRejectsDegeneratePreviewAndFallsBackToVideo(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	const emojiID = int64(78)
	if err := media.PutDocument(ctx, domain.Document{
		ID:       emojiID,
		MimeType: "application/x-tgsticker",
		Thumbs: []domain.PhotoSize{{
			Kind: domain.PhotoSizeKindCached, Type: "m", W: 1, H: 1,
			Bytes: testJPEG(t, 1, 1),
		}},
	}); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	frame := testJPEG(t, 640, 640)
	thumbnailer := &fakeVideoThumbnailer{thumb: frame}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))
	if _, err := svc.SaveFilePart(ctx, 10, 503, 0, []byte("profile-video-without-server-preview")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	photo, err := svc.CreateAvatarVideoMarkupFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 503, Parts: 1, Name: "avatar.mp4"},
		0,
		domain.PhotoSize{Kind: domain.PhotoSizeKindVideoEmojiMarkup, EmojiID: emojiID, BackgroundColors: []int{0x112233}})
	if err != nil {
		t.Fatalf("CreateAvatarVideoMarkupFromUpload: %v", err)
	}
	if thumbnailer.calls != 1 {
		t.Fatalf("thumbnailer calls = %d, want degenerate preview rejected and video fallback used", thumbnailer.calls)
	}
	chunk, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: fmt.Sprintf("photo:%d:c", photo.ID), Limit: 1 << 20})
	if err != nil || !found {
		t.Fatalf("avatar c blob found=%v err=%v", found, err)
	}
	if !bytes.Equal(chunk.Bytes, frame) {
		t.Fatal("avatar still did not use extracted video frame after rejecting degenerate preview")
	}
}

// TestCreateAvatarVideoMarkupPrefersComposedStill guards the boundary of the DrKLO emoji builder:
// the first frame of the client-generated MP4 may contain only a gradient background; as long as
// the markup thumb resolves, the static avatar must use the server-composed result, ensuring the
// emoji is visible both in the current session echo and on a cold start.
func TestCreateAvatarVideoMarkupPrefersComposedStill(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	const emojiID = int64(501)
	if err := media.PutDocument(ctx, domain.Document{
		ID:       emojiID,
		MimeType: "application/x-tgsticker",
		Thumbs: []domain.PhotoSize{{
			Kind:  domain.PhotoSizeKindCached,
			Type:  "m",
			W:     64,
			H:     64,
			Bytes: testTransparentThumbPNG(t),
		}},
	}); err != nil {
		t.Fatalf("PutDocument: %v", err)
	}
	thumbnailer := &fakeVideoThumbnailer{thumb: testJPEG(t, 320, 320)}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(thumbnailer))
	if _, err := svc.SaveFilePart(ctx, 10, 502, 0, []byte("background-only-profile-video")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}

	photo, err := svc.CreateAvatarVideoMarkupFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 502, Parts: 1, Name: "avatar.mp4"},
		0,
		domain.PhotoSize{
			Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
			EmojiID:          emojiID,
			BackgroundColors: []int{0x112233, 0x445566},
		})
	if err != nil {
		t.Fatalf("CreateAvatarVideoMarkupFromUpload: %v", err)
	}
	if thumbnailer.calls != 0 {
		t.Fatalf("thumbnailer calls = %d, want 0 when markup still is available", thumbnailer.calls)
	}
	r, g, b, _ := avatarStillCenterPixel(t, svc, photo.ID)
	if r < 200 || g > 90 || b > 90 {
		t.Fatalf("composed center pixel rgb=(%d,%d,%d), want visible red emoji overlay", r, g, b)
	}
	assertDownloadableAvatarSize(t, svc, photo.ID, "s", 150)
	assertDownloadableAvatarSize(t, svc, photo.ID, "a", 160)
	assertDownloadableAvatarSize(t, svc, photo.ID, "c", 640)
}

func assertDownloadableAvatarSize(t *testing.T, svc *Service, photoID int64, sizeType string, side int) {
	t.Helper()
	assertAvatarImageSize(t, svc, photoID, sizeType, side, side, "image/png")
}

func assertAvatarImageSize(t *testing.T, svc *Service, photoID int64, sizeType string, wantW, wantH int, wantMime string) {
	t.Helper()
	chunk, found, err := svc.GetFile(context.Background(), domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("photo:%d:%s", photoID, sizeType),
		Offset:      0,
		Limit:       1 << 20,
	})
	if err != nil || !found {
		t.Fatalf("avatar %s blob found=%v err=%v", sizeType, found, err)
	}
	if len(chunk.Bytes) == 0 || chunk.MimeType != wantMime {
		t.Fatalf("avatar %s chunk mime=%q bytes=%d, want %s bytes", sizeType, chunk.MimeType, len(chunk.Bytes), wantMime)
	}
	img, _, err := image.Decode(bytes.NewReader(chunk.Bytes))
	if err != nil {
		t.Fatalf("decode avatar %s: %v", sizeType, err)
	}
	if gotW, gotH := img.Bounds().Dx(), img.Bounds().Dy(); gotW != wantW || gotH != wantH {
		t.Fatalf("avatar %s pixels=%dx%d, want %dx%d", sizeType, gotW, gotH, wantW, wantH)
	}
}

// testTransparentThumbPNG builds a red square overlay: transparent around the edges, center
// alpha=250 (simulating anti-aliased non-full alpha), used to guard the premultiplication overflow
// regression — the overflowing code renders every pixel with alpha≠255 whole-black.
func testTransparentThumbPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 8; y < 56; y++ {
		for x := 8; x < 56; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 240, G: 30, B: 30, A: 250})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test thumb: %v", err)
	}
	return buf.Bytes()
}

type fakeVideoThumbnailer struct {
	calls int
	thumb []byte
	err   error
}

type fakeGIFTranscoder struct {
	result GIFVideo
	err    error
	calls  int
}

func (f *fakeGIFTranscoder) Transcode(_ context.Context, _ []byte) (GIFVideo, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeVideoThumbnailer) Extract(context.Context, []byte, string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.thumb...), nil
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(40 + x), G: uint8(80 + y), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestEnsureAvatarPhotoCreatesSeparateAvatarForMessagePhoto(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)

	source, err := svc.CreatePhotoFromBytes(ctx, testJPEG(t, 640, 480))
	if err != nil {
		t.Fatalf("CreatePhotoFromBytes: %v", err)
	}
	if hasAvatarSizes(source) {
		t.Fatalf("message photo sizes = %+v, want no avatar sizes", source.Sizes)
	}

	avatar, err := svc.EnsureAvatarPhoto(ctx, source)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto: %v", err)
	}
	if avatar.ID == source.ID || avatar.ID == 0 || avatar.AccessHash == 0 {
		t.Fatalf("avatar id=%d access_hash=%d, want a new photo distinct from %d", avatar.ID, avatar.AccessHash, source.ID)
	}
	gotTypes := make([]string, 0, len(avatar.Sizes))
	for _, size := range avatar.Sizes {
		gotTypes = append(gotTypes, size.Type)
		blob, found, err := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:%s", avatar.ID, size.Type))
		if err != nil || !found {
			t.Fatalf("avatar blob %s found=%v err=%v", size.Type, found, err)
		}
		data, err := blobs.Get(ctx, blob.ObjectKey)
		if err != nil || len(data) != size.Size {
			t.Fatalf("avatar blob %s len=%d err=%v, want %d bytes", size.Type, len(data), err, size.Size)
		}
	}
	if !reflect.DeepEqual(gotTypes, []string{"s", "a", "c"}) {
		t.Fatalf("avatar size types = %v, want [s a c]", gotTypes)
	}
	if c := avatar.Sizes[2]; c.W != 640 || c.H != 480 {
		t.Fatalf("avatar c = %dx%d, want the 640x480 source", c.W, c.H)
	}
	stored, found, err := media.GetPhoto(ctx, avatar.ID)
	if err != nil || !found || !reflect.DeepEqual(stored, avatar) {
		t.Fatalf("stored avatar = %+v found=%v err=%v, want %+v", stored, found, err, avatar)
	}

	storedSource, found, err := media.GetPhoto(ctx, source.ID)
	if err != nil || !found {
		t.Fatalf("source photo found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(storedSource, source) {
		t.Fatalf("source photo row = %+v, want unchanged %+v", storedSource, source)
	}
	for _, sizeType := range []string{"s", "a", "c"} {
		if _, found, _ := media.GetFileBlob(ctx, fmt.Sprintf("photo:%d:%s", source.ID, sizeType)); found {
			t.Fatalf("source photo gained blob %s", sizeType)
		}
	}

	photoCount := len(media.photos)
	again, err := svc.EnsureAvatarPhoto(ctx, avatar)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto(avatar): %v", err)
	}
	if !reflect.DeepEqual(again, avatar) || len(media.photos) != photoCount {
		t.Fatalf("EnsureAvatarPhoto(avatar) = %+v with %d photos, want the same photo and %d photos", again, len(media.photos), photoCount)
	}
}

func TestEnsureAvatarPhotoRendersFromLargestStoredSize(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)

	const photoID = int64(8101)
	small := testJPEG(t, 320, 240)
	large := testJPEG(t, 1280, 960)
	putTestPhotoBlob(t, media, blobs, photoID, "m", small)
	putTestPhotoBlob(t, media, blobs, photoID, "y", large)
	// 'a' without 'c' is not a usable avatar either.
	source := domain.Photo{ID: photoID, AccessHash: 8102, DCID: 2, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 320, H: 240, Size: len(small)},
		{Kind: domain.PhotoSizeKindProgressive, Type: "y", W: 1280, H: 960, Sizes: []int{len(large)}},
		{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 160, H: 160, Size: 100},
	}}

	avatar, err := svc.EnsureAvatarPhoto(ctx, source)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto: %v", err)
	}
	if avatar.ID == source.ID || len(avatar.Sizes) != 3 {
		t.Fatalf("avatar = %+v, want a new photo with s/a/c", avatar)
	}
	if c := avatar.Sizes[2]; c.Type != "c" || c.W != 1280 || c.H != 960 {
		t.Fatalf("avatar c = %+v, want 1280x960 from the largest size", c)
	}
}

func TestEnsureAvatarPhotoFallsBackToCachedBytes(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	svc := NewService(media, blobs, 2)

	cached := testJPEG(t, 90, 90)
	source := domain.Photo{ID: 8201, AccessHash: 8202, DCID: 2, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{1, 2, 3}},
		{Kind: domain.PhotoSizeKindCached, Type: "s", W: 90, H: 90, Bytes: cached},
	}}
	avatar, err := svc.EnsureAvatarPhoto(ctx, source)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto: %v", err)
	}
	if avatar.ID == source.ID || len(avatar.Sizes) != 3 || avatar.Sizes[2].W != 90 || avatar.Sizes[2].H != 90 {
		t.Fatalf("avatar = %+v, want a new 90x90 avatar", avatar)
	}
}

func TestEnsureAvatarPhotoSkipsSourcesOverTheCaps(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(media, blobs, 2)

	const photoID = int64(8301)
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: fmt.Sprintf("photo:%d:w", photoID),
		Backend:     domain.MediaBackendLocalFS,
		ObjectKey:   "missing",
		Size:        avatarSourceMaxBytes + 1,
	}); err != nil {
		t.Fatalf("PutFileBlob: %v", err)
	}
	putTestPhotoBlob(t, media, local, photoID, "x", testPNGHeader(t, 6000, 5000))
	small := testJPEG(t, 320, 240)
	putTestPhotoBlob(t, media, local, photoID, "m", small)
	source := domain.Photo{ID: photoID, AccessHash: 8302, DCID: 2, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 320, H: 240, Size: len(small)},
		{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 6000, H: 5000, Size: 64},
		{Kind: domain.PhotoSizeKindDefault, Type: "w", W: 8000, H: 6000, Size: 1024},
	}}

	avatar, err := svc.EnsureAvatarPhoto(ctx, source)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto: %v", err)
	}
	if c := avatar.Sizes[2]; c.W != 320 || c.H != 240 {
		t.Fatalf("avatar c = %dx%d, want 320x240 from 'm' after skipping 'w' and 'x'", c.W, c.H)
	}
	// 'x' and 'm' are read; the oversized 'w' is not.
	if calls := blobs.getRangeCalls.Load(); calls != 2 {
		t.Fatalf("GetRange calls = %d, want 2", calls)
	}

	cached := testJPEG(t, 90, 90)
	cachedOnly := domain.Photo{ID: 8303, AccessHash: 8304, DCID: 2, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindCached, Type: "x", W: 6000, H: 5000, Bytes: testPNGHeader(t, 6000, 5000)},
		{Kind: domain.PhotoSizeKindCached, Type: "s", W: 90, H: 90, Bytes: cached},
	}}
	avatar, err = svc.EnsureAvatarPhoto(ctx, cachedOnly)
	if err != nil {
		t.Fatalf("EnsureAvatarPhoto(cached): %v", err)
	}
	if c := avatar.Sizes[2]; c.W != 90 || c.H != 90 {
		t.Fatalf("cached avatar c = %dx%d, want 90x90 after skipping the oversized cached size", c.W, c.H)
	}
}

func TestEnsureAvatarPhotoRejectsUnusableSource(t *testing.T) {
	ctx := context.Background()
	storageErr := errors.New("media store down")
	tests := []struct {
		name    string
		blob    func(t *testing.T, media *fakeMediaStore, blobs BlobBackend, photoID int64)
		failGet bool
		noRead  bool
		wantErr error
	}{
		{name: "no stored image", wantErr: domain.ErrPhotoInvalid},
		{
			name: "not an image",
			blob: func(t *testing.T, media *fakeMediaStore, blobs BlobBackend, photoID int64) {
				putTestPhotoBlob(t, media, blobs, photoID, "x", []byte("not an image"))
			},
			wantErr: domain.ErrPhotoInvalid,
		},
		{
			name: "too many pixels",
			blob: func(t *testing.T, media *fakeMediaStore, blobs BlobBackend, photoID int64) {
				putTestPhotoBlob(t, media, blobs, photoID, "x", testPNGHeader(t, 6000, 5000))
			},
			wantErr: domain.ErrPhotoInvalid,
		},
		{
			// The object does not exist, so reading it would also fail.
			name: "blob over the size cap",
			blob: func(t *testing.T, media *fakeMediaStore, _ BlobBackend, photoID int64) {
				if err := media.PutFileBlob(ctx, domain.FileBlob{
					LocationKey: fmt.Sprintf("photo:%d:x", photoID),
					Backend:     domain.MediaBackendLocalFS,
					ObjectKey:   "missing",
					Size:        avatarSourceMaxBytes + 1,
				}); err != nil {
					t.Fatalf("PutFileBlob: %v", err)
				}
			},
			noRead:  true,
			wantErr: domain.ErrPhotoInvalid,
		},
		{name: "storage error", failGet: true, wantErr: storageErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media := newFakeMediaStore()
			local, err := NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatalf("NewLocalFS: %v", err)
			}
			blobs := &countingBlobBackend{BlobBackend: local}
			const photoID = int64(8001)
			if tt.blob != nil {
				tt.blob(t, media, local, photoID)
			}
			var mediaStore store.MediaStore = media
			if tt.failGet {
				mediaStore = &failingBlobMediaStore{fakeMediaStore: media, err: storageErr}
			}
			svc := NewService(mediaStore, blobs, 2)
			source := domain.Photo{ID: photoID, AccessHash: 8002, DCID: 2, Sizes: []domain.PhotoSize{
				{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{1, 2, 3}},
				{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 6000, H: 5000, Size: 1024},
			}}
			if err := media.PutPhoto(ctx, source); err != nil {
				t.Fatalf("PutPhoto: %v", err)
			}
			blobCount := len(media.blobs)

			if _, err := svc.EnsureAvatarPhoto(ctx, source); !errors.Is(err, tt.wantErr) {
				t.Fatalf("EnsureAvatarPhoto err = %v, want %v", err, tt.wantErr)
			}
			if tt.noRead && blobs.getRangeCalls.Load() != 0 {
				t.Fatalf("read %d ranges of an oversized blob, want none", blobs.getRangeCalls.Load())
			}
			if len(media.photos) != 1 || len(media.blobs) != blobCount {
				t.Fatalf("store has %d photos and %d blobs, want only the source photo and %d blobs", len(media.photos), len(media.blobs), blobCount)
			}
		})
	}
}

func TestAvatarSourceDecodableCapsPixels(t *testing.T) {
	if !avatarSourceDecodable(testPNGHeader(t, 5000, 5000)) {
		t.Fatal("5000x5000 rejected, want it within avatarSourceMaxPixels")
	}
	if avatarSourceDecodable(testPNGHeader(t, 6000, 5000)) {
		t.Fatal("6000x5000 accepted, want it over avatarSourceMaxPixels")
	}
}

type failingBlobMediaStore struct {
	*fakeMediaStore
	err error
}

func (f *failingBlobMediaStore) GetFileBlob(context.Context, string) (domain.FileBlob, bool, error) {
	return domain.FileBlob{}, false, f.err
}

func putTestPhotoBlob(t *testing.T, media *fakeMediaStore, blobs BlobBackend, photoID int64, sizeType string, data []byte) {
	t.Helper()
	objectKey, err := blobs.Put(context.Background(), data)
	if err != nil {
		t.Fatalf("blobs.Put: %v", err)
	}
	if err := media.PutFileBlob(context.Background(), domain.FileBlob{
		LocationKey: fmt.Sprintf("photo:%d:%s", photoID, sizeType),
		Backend:     domain.MediaBackendLocalFS,
		ObjectKey:   objectKey,
		Size:        int64(len(data)),
		MimeType:    "image/jpeg",
	}); err != nil {
		t.Fatalf("PutFileBlob: %v", err)
	}
}

// testPNGHeader returns a PNG signature and IHDR chunk only: image.DecodeConfig reports w x h,
// but there is no pixel data to decode.
func testPNGHeader(t *testing.T, w, h int) []byte {
	t.Helper()
	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(w))
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(h))
	ihdr.Write([]byte{8, 2, 0, 0, 0}) // 8-bit RGB, default compression/filter, no interlace
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&out, binary.BigEndian, uint32(ihdr.Len()-4))
	out.Write(ihdr.Bytes())
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(ihdr.Bytes()))
	return out.Bytes()
}
