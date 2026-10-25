package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// WHY: a plain-text quote reply has no entities; a projected messageReplyHeader
// must leave the quote_entities flag clear, since the layer-228 sparse encoder
// rejects a set flag holding a nil slice and then getHistory pages pulse INTERNAL.
func TestPlainTextQuoteReplyHeaderEncodesForLayer228(t *testing.T) {
	header, ok := tgMessageReplyHeader(domain.Message{
		ReplyTo: &domain.MessageReply{MessageID: 5, QuoteText: "hello"},
	}).(*tg.MessageReplyHeader)
	if !ok {
		t.Fatal("expected a reply header")
	}
	if _, set := header.GetQuoteEntities(); set {
		t.Fatal("quote_entities flag must stay clear without entities")
	}
	var buf bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile228, header, &buf); err != nil {
		t.Fatalf("layer 228 encode: %v", err)
	}
}

func TestPlainTextQuoteReplyHeaderEncodesEntityVariantForLayer228(t *testing.T) {
	header, ok := tgMessageReplyHeader(domain.Message{
		ReplyTo: &domain.MessageReply{
			MessageID:     6,
			QuoteText:     "styled",
			QuoteEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 4}},
		},
	}).(*tg.MessageReplyHeader)
	if !ok {
		t.Fatal("expected a reply header")
	}
	if entities, set := header.GetQuoteEntities(); !set || len(entities) != 1 {
		t.Fatalf("entities must be present: set=%v count=%d", set, len(entities))
	}
	var buf bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile228, header, &buf); err != nil {
		t.Fatalf("layer 228 encode: %v", err)
	}
}
