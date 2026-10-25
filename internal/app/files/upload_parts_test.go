package files

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestSaveFilePartQuotaTreatsRetryAsOverwrite(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	svc, blobs := newUploadPartTestService(t, media, domain.UploadPartQuota{MaxBytes: 4, MaxParts: 1, MaxFiles: 1})

	if _, err := svc.SaveFilePart(ctx, 10, 100, 0, []byte("1234")); err != nil {
		t.Fatalf("save first part: %v", err)
	}
	firstParts, err := media.LoadFileParts(ctx, 10, 100)
	if err != nil || len(firstParts) != 1 || firstParts[0].ObjectKey == "" {
		t.Fatalf("load first part metadata: parts=%+v err=%v", firstParts, err)
	}
	firstKey := firstParts[0].ObjectKey
	if _, err := svc.SaveFilePart(ctx, 10, 100, 0, []byte("1234")); err != nil {
		t.Fatalf("retry same part should overwrite without extra quota: %v", err)
	}
	parts, err := media.LoadFileParts(ctx, 10, 100)
	if err != nil {
		t.Fatalf("load parts: %v", err)
	}
	if len(parts) != 1 || parts[0].Size != 4 || parts[0].ObjectKey == "" || parts[0].ObjectKey == firstKey {
		t.Fatalf("parts after retry = %+v", parts)
	}
	if _, err := blobs.GetUploadPart(ctx, firstKey); err == nil {
		t.Fatalf("replaced upload part object %q still exists", firstKey)
	}
	data, err := svc.assembleUpload(ctx, 10, 100, 1)
	if err != nil {
		t.Fatalf("assemble upload: %v", err)
	}
	if string(data) != "1234" {
		t.Fatalf("assembled data = %q", data)
	}
}

func TestSaveFilePartQuotaRejectsNewFileOverLimit(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	svc, _ := newUploadPartTestService(t, media, domain.UploadPartQuota{MaxBytes: 8, MaxParts: 4, MaxFiles: 1})

	if _, err := svc.SaveFilePart(ctx, 10, 100, 0, []byte("1234")); err != nil {
		t.Fatalf("save first file part: %v", err)
	}
	_, err := svc.SaveFilePart(ctx, 10, 101, 0, []byte("12"))
	if !errors.Is(err, domain.ErrUploadQuotaExceeded) {
		t.Fatalf("save second file err = %v, want ErrUploadQuotaExceeded", err)
	}
}

func TestGeneratedBigUploadUnknownTotal(t *testing.T) {
	ctx := context.Background()
	for _, finalKnown := range []bool{false, true} {
		t.Run(strconv.FormatBool(finalKnown), func(t *testing.T) {
			media := newFakeMediaStore()
			svc, _ := newUploadPartTestService(t, media, domain.UploadPartQuota{})
			for _, part := range []int{1, 0, 1} {
				if _, err := svc.SaveBigFilePart(ctx, 10, 200, part, -1, []byte{byte('a' + part)}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.readUploadBytes(ctx, 10, 200, 3); !errors.Is(err, domain.ErrFilePartsInvalid) {
				t.Fatalf("missing final part: %v", err)
			}
			total := -1
			if finalKnown {
				total = 3
			}
			if _, err := svc.SaveBigFilePart(ctx, 10, 200, 2, total, []byte("c")); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.readUploadBytes(ctx, 11, 200, 3); !errors.Is(err, domain.ErrFilePartsInvalid) {
				t.Fatalf("other owner read: %v", err)
			}
			body, err := svc.readUploadBytes(ctx, 10, 200, 3)
			if err != nil || string(body) != "abc" {
				t.Fatalf("body=%q error=%v", body, err)
			}
			for _, invalid := range []int{-1, 0, MaxUploadParts + 1} {
				if _, err := svc.readUploadBytes(ctx, 10, 200, invalid); !errors.Is(err, domain.ErrFilePartsInvalid) {
					t.Fatalf("invalid final parts=%d: %v", invalid, err)
				}
			}
			if _, err := svc.SaveBigFilePart(ctx, 10, 200, 0, 4, []byte("a")); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.readUploadBytes(ctx, 10, 200, 3); !errors.Is(err, domain.ErrFilePartsInvalid) {
				t.Fatalf("conflicting total: %v", err)
			}
			for _, invalid := range []int{-2, 0, MaxUploadParts + 1} {
				if _, err := svc.SaveBigFilePart(ctx, 10, 200, 0, invalid, []byte("a")); !errors.Is(err, domain.ErrFilePartsInvalid) {
					t.Fatalf("total=%d: %v", invalid, err)
				}
			}
			if _, err := svc.SaveBigFilePart(ctx, 10, 200, 3, 3, []byte("a")); !errors.Is(err, domain.ErrFilePartsInvalid) {
				t.Fatalf("part beyond known total: %v", err)
			}
		})
	}
}

func TestSaveFilePartQuotaRejectsPartAndByteOverLimit(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	svc, _ := newUploadPartTestService(t, media, domain.UploadPartQuota{MaxBytes: 5, MaxParts: 1, MaxFiles: 2})

	if _, err := svc.SaveFilePart(ctx, 10, 100, 0, []byte("1234")); err != nil {
		t.Fatalf("save first part: %v", err)
	}
	_, err := svc.SaveFilePart(ctx, 10, 100, 1, []byte("12"))
	if !errors.Is(err, domain.ErrUploadQuotaExceeded) {
		t.Fatalf("save second part err = %v, want ErrUploadQuotaExceeded", err)
	}
	_, err = svc.SaveFilePart(ctx, 10, 100, 0, []byte("123456"))
	if !errors.Is(err, domain.ErrUploadQuotaExceeded) {
		t.Fatalf("grow retried part err = %v, want ErrUploadQuotaExceeded", err)
	}
}

func TestCreateDocumentFromUploadStreamsBodyAndCleansParts(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	blobs := &countingUploadPartBackend{LocalFS: local}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil))

	parts := []string{
		strings.Repeat("a", 1024),
		strings.Repeat("b", 1024),
		strings.Repeat("c", 1024),
	}
	for i, part := range parts {
		if _, err := svc.SaveBigFilePart(ctx, 10, 200, i, len(parts), []byte(part)); err != nil {
			t.Fatalf("SaveBigFilePart %d: %v", i, err)
		}
	}
	doc, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 200, Parts: len(parts), Name: "large.bin", Big: true},
		domain.DocumentSpec{MimeType: "application/octet-stream"},
	)
	if err != nil {
		t.Fatalf("CreateDocumentFromUpload: %v", err)
	}
	if doc.Size != int64(len(parts[0])+len(parts[1])+len(parts[2])) {
		t.Fatalf("doc size = %d", doc.Size)
	}
	if blobs.getUploadPartCalls != 0 {
		t.Fatalf("streaming document path called GetUploadPart %d times", blobs.getUploadPartCalls)
	}
	if remaining, err := media.LoadFileParts(ctx, 10, 200); err != nil || len(remaining) != 0 {
		t.Fatalf("upload parts after success = %+v err=%v", remaining, err)
	}
	blob, ok, err := media.GetFileBlob(ctx, "doc:"+strconv.FormatInt(doc.ID, 10))
	if err != nil || !ok {
		t.Fatalf("body file blob ok=%v err=%v", ok, err)
	}
	body, err := local.Get(ctx, blob.ObjectKey)
	if err != nil {
		t.Fatalf("read body blob: %v", err)
	}
	if string(body) != strings.Join(parts, "") {
		t.Fatalf("body blob mismatch")
	}
	replayed, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 200, Parts: len(parts), Name: "large.bin", Big: true},
		domain.DocumentSpec{MimeType: "application/octet-stream"},
	)
	if err != nil {
		t.Fatalf("replay CreateDocumentFromUpload after part cleanup: %v", err)
	}
	if replayed.ID != doc.ID || replayed.AccessHash != doc.AccessHash {
		t.Fatalf("replayed document = %d/%d, want original %d/%d", replayed.ID, replayed.AccessHash, doc.ID, doc.AccessHash)
	}
	if _, err := svc.CreateDocumentFromUpload(ctx,
		domain.UploadedFileRef{OwnerUserID: 10, FileID: 200, Parts: len(parts), Name: "large.bin", Big: true},
		domain.DocumentSpec{MimeType: "text/plain"},
	); !errors.Is(err, domain.ErrFilePartsInvalid) {
		t.Fatalf("changed materialization intent err = %v, want ErrFilePartsInvalid", err)
	}
}

func TestCreatePhotoFromUploadReceiptReplaysAfterPartCleanup(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	svc, _ := newUploadPartTestService(t, media, domain.UploadPartQuota{})
	file := domain.UploadedFileRef{OwnerUserID: 10, FileID: 201, Parts: 1, Name: "photo.jpg"}
	if _, err := svc.SaveFilePart(ctx, file.OwnerUserID, file.FileID, 0, []byte("image-bytes")); err != nil {
		t.Fatalf("SaveFilePart: %v", err)
	}
	first, err := svc.CreatePhotoFromUpload(ctx, file)
	if err != nil {
		t.Fatalf("CreatePhotoFromUpload: %v", err)
	}
	if remaining, err := media.LoadFileParts(ctx, file.OwnerUserID, file.FileID); err != nil || len(remaining) != 0 {
		t.Fatalf("upload parts after photo materialization = %+v err=%v", remaining, err)
	}
	replayed, err := svc.CreatePhotoFromUpload(ctx, file)
	if err != nil {
		t.Fatalf("replay CreatePhotoFromUpload: %v", err)
	}
	if replayed.ID != first.ID || replayed.AccessHash != first.AccessHash {
		t.Fatalf("replayed photo = %d/%d, want original %d/%d", replayed.ID, replayed.AccessHash, first.ID, first.AccessHash)
	}
	changed := file
	changed.Name = "different.jpg"
	if _, err := svc.CreatePhotoFromUpload(ctx, changed); !errors.Is(err, domain.ErrFilePartsInvalid) {
		t.Fatalf("changed photo intent err = %v, want ErrFilePartsInvalid", err)
	}
}

type countingUploadPartBackend struct {
	*LocalFS
	getUploadPartCalls    int
	putUploadPartCalls    int
	deleteUploadPartCalls int
}

func (c *countingUploadPartBackend) GetUploadPart(ctx context.Context, objectKey string) ([]byte, error) {
	c.getUploadPartCalls++
	return c.LocalFS.GetUploadPart(ctx, objectKey)
}

func (c *countingUploadPartBackend) PutUploadPart(ctx context.Context, ownerUserID, fileID int64, part int, data []byte) (UploadPartObject, error) {
	c.putUploadPartCalls++
	return c.LocalFS.PutUploadPart(ctx, ownerUserID, fileID, part, data)
}

func (c *countingUploadPartBackend) DeleteUploadPart(ctx context.Context, objectKey string) error {
	c.deleteUploadPartCalls++
	return c.LocalFS.DeleteUploadPart(ctx, objectKey)
}

func newUploadPartTestService(t *testing.T, media *fakeMediaStore, quota domain.UploadPartQuota) (*Service, *LocalFS) {
	t.Helper()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	return NewService(media, blobs, 2,
		WithVideoThumbnailer(nil),
		WithUploadPartQuota(quota),
	), blobs
}
