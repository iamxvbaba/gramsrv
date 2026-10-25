package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"telesrv/internal/domain"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.Bytes()
}

func TestValidTGS(t *testing.T) {
	good := `{"tgs":1,"v":"5.5.2","fr":60,"ip":0,"op":180,"w":512,"h":512,"layers":[{"ty":4}]}`
	if !validTGS(gz(t, good)) {
		t.Fatal("valid lottie rejected")
	}
	for name, data := range map[string][]byte{
		"plain json":  []byte(good),
		"not lottie":  gz(t, `{"hello":"world"}`),
		"no layers":   gz(t, `{"v":"5","ip":0,"op":10,"w":512,"h":512,"layers":[]}`),
		"garbage":     []byte("PK\x03\x04zip"),
		"broken json": gz(t, `{"v":`),
	} {
		if validTGS(data) {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestTGSStickerAttributes(t *testing.T) {
	spec := domain.DocumentSpec{MimeType: "application/octet-stream", ForceFile: true,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "Duck.TGS"}}}
	if !looksLikeTGSUpload(spec) {
		t.Fatal(".tgs file name not recognised")
	}
	attrs := tgsStickerAttributes(spec.Attributes)
	doc := domain.Document{MimeType: "application/x-tgsticker", Attributes: attrs}
	if !doc.IsSticker() || len(attrs) != 3 || attrs[0].FileName != "Duck.TGS" {
		t.Fatalf("attributes = %+v", attrs)
	}
	if looksLikeTGSUpload(domain.DocumentSpec{MimeType: "application/zip",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "a.zip"}}}) {
		t.Fatal("zip treated as tgs")
	}
}

// TestCreateDocumentFromUploadTurnsTGSIntoSticker drives the whole upload
// path: only a real Lottie animation becomes a sticker, even when sent as a
// file, and an upload that already is a sticker keeps its own attributes.
func TestCreateDocumentFromUploadTurnsTGSIntoSticker(t *testing.T) {
	lottie := gz(t, `{"v":"5.5.2","fr":60,"ip":0,"op":180,"w":512,"h":512,"layers":[{"ty":4}]}`)
	customEmoji := domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, Alt: "x"}
	for i, tc := range []struct {
		name        string
		body        []byte
		spec        domain.DocumentSpec
		wantMime    string
		wantSticker bool
		wantAttrs   int
	}{
		{
			name: "valid tgs sent as a file", body: lottie, wantMime: "application/x-tgsticker", wantSticker: true, wantAttrs: 3,
			spec: domain.DocumentSpec{MimeType: "application/octet-stream", ForceFile: true,
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "Duck.TGS"}}},
		},
		{
			name: "gzip named tgs that is not lottie", body: gz(t, `{"a":1}`), wantMime: "application/octet-stream", wantAttrs: 1,
			spec: domain.DocumentSpec{MimeType: "application/octet-stream",
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "x.tgs"}}},
		},
		{
			name: "custom emoji upload keeps its attributes", body: lottie, wantMime: "application/x-tgsticker", wantAttrs: 1,
			spec: domain.DocumentSpec{MimeType: "application/x-tgsticker", Attributes: []domain.DocumentAttribute{customEmoji}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			blobs, err := NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil))
			fileID := int64(970 + i)
			if _, err := svc.SaveFilePart(ctx, 10, fileID, 0, tc.body); err != nil {
				t.Fatal(err)
			}
			doc, err := svc.CreateDocumentFromUpload(ctx, domain.UploadedFileRef{OwnerUserID: 10, FileID: fileID, Parts: 1}, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			sticker := false
			for _, attr := range doc.Attributes {
				if attr.Kind == domain.DocAttrSticker {
					sticker = true
				}
			}
			if doc.MimeType != tc.wantMime || sticker != tc.wantSticker || len(doc.Attributes) != tc.wantAttrs {
				t.Fatalf("doc mime=%q sticker=%v attrs=%+v", doc.MimeType, sticker, doc.Attributes)
			}
		})
	}
}
