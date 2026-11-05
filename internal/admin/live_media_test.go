package admin

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"strings"
	"sync"
	"testing"

	"telesrv/internal/app/files"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// liveMediaBlobs serves file_blobs metadata from a map. files.Service.GetFile
// reads nothing else from the media store.
type liveMediaBlobs struct {
	store.MediaStore
	blobs map[string]domain.FileBlob
}

func (m *liveMediaBlobs) GetFileBlob(_ context.Context, key string) (domain.FileBlob, bool, error) {
	blob, ok := m.blobs[key]
	return blob, ok, nil
}

// recordingFiles is the real files service with every GetFile request recorded,
// so a test can prove which keys were looked up and how much was asked for.
type recordingFiles struct {
	*files.Service
	mu   sync.Mutex
	reqs []domain.FileDownloadRequest
}

func (r *recordingFiles) GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return r.Service.GetFile(ctx, req)
}

func (r *recordingFiles) requests() []domain.FileDownloadRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.FileDownloadRequest(nil), r.reqs...)
}

// zeroReader yields an endless run of zero bytes, so a large blob can be
// streamed into the backend without building it in memory first.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// LiveMedia serves message media to the review console on the panel origin, so
// the type it reports must come from the bytes and an allowlist, never from the
// uploader. This runs it over the real files service on a LocalFS backend.
func TestLiveMediaSniffsAllowlistsAndBoundsTheRead(t *testing.T) {
	ctx := context.Background()
	backend, err := files.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	meta := &liveMediaBlobs{blobs: map[string]domain.FileBlob{}}
	put := func(key, mimeType string, r io.Reader) {
		t.Helper()
		objectKey, size, _, err := backend.PutReader(ctx, r)
		if err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
		meta.blobs[key] = domain.FileBlob{
			LocationKey: key,
			Backend:     domain.MediaBackend(backend.Name()),
			ObjectKey:   objectKey,
			Size:        size,
			MimeType:    mimeType,
		}
	}
	rec := &recordingFiles{Service: files.NewService(meta, backend, 2, files.WithVideoThumbnailer(nil), files.WithGIFTranscoder(nil))}
	svc := NewService(Dependencies{Photos: rec})

	html := []byte(`<!DOCTYPE html><html><body><script>fetch('/api/actions/x',{method:'POST'})</script></body></html>`)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.domain)</script></svg>`)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	jpegHeader := []byte("\xFF\xD8\xFF\xE0")

	found := []struct {
		name     string
		key      string // stored location key
		lookup   string // key the console asks for
		stored   string // uploader-declared MIME type
		data     []byte
		wantType string
	}{
		{name: "html", key: "doc:1", stored: "text/html", data: html, wantType: "application/octet-stream"},
		{name: "svg", key: "doc:2", stored: "image/svg+xml", data: svg, wantType: "application/octet-stream"},
		{name: "xml svg", key: "doc:3", stored: "image/svg+xml", data: append([]byte(`<?xml version="1.0"?>`), svg...), wantType: "application/octet-stream"},
		{name: "empty mime", key: "doc:4", stored: "", data: html, wantType: "application/octet-stream"},
		{name: "pdf", key: "doc:5", stored: "application/pdf", data: []byte("%PDF-1.4\n1 0 obj<</OpenAction<</S/JavaScript/JS(app.alert(1))>>>>endobj"), wantType: "application/octet-stream"},
		{name: "png via sized variant", key: "photo:6:x", lookup: "photo:6", stored: "image/png", data: pngBuf.Bytes(), wantType: "image/png"},
		{name: "gif polyglot", key: "doc:7", stored: "text/html", data: append([]byte("GIF89a"), html...), wantType: "image/gif"},
		{name: "jpeg polyglot", key: "doc:8", stored: "text/html", data: append(append([]byte(nil), jpegHeader...), html...), wantType: "image/jpeg"},
		{name: "mp4", key: "doc:9", stored: "text/html", data: append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), html...), wantType: "video/mp4"},
		{name: "id3", key: "doc:10", stored: "text/html", data: append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), html...), wantType: "audio/mpeg"},
	}
	for _, tc := range found {
		put(tc.key, tc.stored, bytes.NewReader(tc.data))
	}
	// Exactly at the cap is served; one byte over is refused.
	put("doc:11", "image/jpeg", io.MultiReader(bytes.NewReader(jpegHeader), io.LimitReader(zeroReader{}, maxLiveMediaBytes-int64(len(jpegHeader)))))
	put("doc:12", "image/jpeg", io.MultiReader(bytes.NewReader(jpegHeader), io.LimitReader(zeroReader{}, maxLiveMediaBytes+1-int64(len(jpegHeader)))))
	put("doc:13", "text/plain", bytes.NewReader(nil))
	// Blobs exist under the refused keys too, so only the key checks keep them out.
	longKey := "doc:" + strings.Repeat("1", 512)
	for _, key := range []string{"enc:1", "file:/etc/passwd", "doc:../x", longKey} {
		put(key, "text/html", bytes.NewReader(html))
	}

	for _, tc := range found {
		t.Run(tc.name, func(t *testing.T) {
			lookup := tc.lookup
			if lookup == "" {
				lookup = tc.key
			}
			data, mimeType, ok, err := svc.LiveMedia(ctx, lookup)
			if err != nil || !ok {
				t.Fatalf("LiveMedia(%q) ok=%v err=%v, want found", lookup, ok, err)
			}
			if mimeType != tc.wantType {
				t.Fatalf("LiveMedia(%q) type=%q, want %q", lookup, mimeType, tc.wantType)
			}
			if !bytes.Equal(data, tc.data) {
				t.Fatalf("LiveMedia(%q) returned %d bytes, want the %d stored", lookup, len(data), len(tc.data))
			}
		})
	}

	t.Run("exactly the cap", func(t *testing.T) {
		data, mimeType, ok, err := svc.LiveMedia(ctx, "doc:11")
		if err != nil || !ok || int64(len(data)) != maxLiveMediaBytes || mimeType != "image/jpeg" {
			t.Fatalf("LiveMedia(16 MiB) len=%d type=%q ok=%v err=%v, want the whole object as image/jpeg", len(data), mimeType, ok, err)
		}
	})

	t.Run("one byte over the cap", func(t *testing.T) {
		data, _, ok, err := svc.LiveMedia(ctx, "doc:12")
		if !errors.Is(err, ErrLiveMediaTooLarge) || ok || data != nil {
			t.Fatalf("LiveMedia(16 MiB + 1) len=%d ok=%v err=%v, want ErrLiveMediaTooLarge", len(data), ok, err)
		}
		// The size is learned from a one-byte probe; the body is never read.
		for _, req := range rec.requests() {
			if req.LocationKey == "doc:12" && (req.Limit <= 0 || req.Limit > 1) {
				t.Fatalf("oversized object read with Limit=%d, want only the 1-byte probe", req.Limit)
			}
		}
	})

	t.Run("missing and empty", func(t *testing.T) {
		for _, key := range []string{"doc:404", "doc:13"} {
			if data, _, ok, err := svc.LiveMedia(ctx, key); err != nil || ok || data != nil {
				t.Fatalf("LiveMedia(%q) len=%d ok=%v err=%v, want not found", key, len(data), ok, err)
			}
		}
	})

	t.Run("refused keys never reach storage", func(t *testing.T) {
		before := len(rec.requests())
		for _, key := range []string{"enc:1", "file:/etc/passwd", "doc:../x", longKey} {
			if data, _, ok, err := svc.LiveMedia(ctx, key); err != nil || ok || data != nil {
				t.Fatalf("LiveMedia(%q) len=%d ok=%v err=%v, want not found", key, len(data), ok, err)
			}
		}
		if after := rec.requests(); len(after) != before {
			t.Fatalf("refused keys reached GetFile: %+v", after[before:])
		}
	})
}
