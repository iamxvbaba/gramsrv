package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"telesrv/internal/domain"
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
	assertDownloadableAvatarSize(t, svc, photo.ID, "a")
	assertDownloadableAvatarSize(t, svc, photo.ID, "c")
}

// TestCreateAvatarMarkupComposesEmojiThumbIntoStaticSizes 守护两个行为：
//  1. 普通彩色 emoji 合成进静态头像时保留原色（不得染白/变黑）；
//  2. 贴图含非满 alpha 像素（抗锯齿常态）时不得因预乘溢出整片变黑——曾因把
//     R=G=B=255、A<255 的非法预乘值喂给 draw.Over 溢出回绕，emoji 输出近黑色。
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

// TestCreateAvatarMarkupTintsTextColorEmojiWhite 守护 text_color custom emoji 的
// 白色剪影呈现：染色必须写合法预乘值（R=G=B=A），非满 alpha 像素不得溢出变黑。
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
	assertDownloadableAvatarSize(t, svc, photo.ID, "a")
	assertDownloadableAvatarSize(t, svc, photo.ID, "c")
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

// TestCreateAvatarVideoMarkupStillUsesVideoFirstFrame 守护动画头像静态尺寸优先取
// 上传视频首帧（客户端真实渲染画面），而不是服务端合成的近似 still。
func TestCreateAvatarVideoMarkupStillUsesVideoFirstFrame(t *testing.T) {
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
		LocationKey: fmt.Sprintf("photo:%d:a", photo.ID),
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
}

func assertDownloadableAvatarSize(t *testing.T, svc *Service, photoID int64, sizeType string) {
	t.Helper()
	chunk, found, err := svc.GetFile(context.Background(), domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("photo:%d:%s", photoID, sizeType),
		Offset:      0,
		Limit:       1 << 20,
	})
	if err != nil || !found {
		t.Fatalf("avatar %s blob found=%v err=%v", sizeType, found, err)
	}
	if len(chunk.Bytes) == 0 || chunk.MimeType != "image/png" {
		t.Fatalf("avatar %s chunk mime=%q bytes=%d, want image/png bytes", sizeType, chunk.MimeType, len(chunk.Bytes))
	}
}

// testTransparentThumbPNG 构造红色方块贴图：周边透明、中心 alpha=250（模拟抗锯齿
// 的非满 alpha），用于守护预乘溢出回归——溢出代码会把 alpha≠255 的像素整片渲染成黑。
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
