package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestTGSWithoutThumbsGetsPlaceholder(t *testing.T) {
	doc, ok := tgDocument(domain.Document{ID: 1, AccessHash: 2, MimeType: "application/x-tgsticker"}).(*tg.Document)
	if !ok || len(doc.Thumbs) != 1 {
		t.Fatalf("thumbs = %+v", doc)
	}
	if p, ok := doc.Thumbs[0].(*tg.PhotoPathSize); !ok || p.Type != "j" {
		t.Fatalf("placeholder = %#v", doc.Thumbs[0])
	}
	plain, _ := tgDocument(domain.Document{ID: 1, AccessHash: 2, MimeType: "application/zip"}).(*tg.Document)
	if len(plain.Thumbs) != 0 {
		t.Fatal("non-sticker file got a thumbnail")
	}
}
