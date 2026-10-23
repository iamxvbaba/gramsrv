package files

import (
	"bytes"
	"compress/gzip"
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
