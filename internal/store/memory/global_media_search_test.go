package memory

import (
	"telesrv/internal/domain"
	"testing"
)

func TestGlobalMediaSearchFiltersBeforeSeekAndBounds(t *testing.T) {
	photo := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1, AccessHash: 1}}
	messages := []domain.Message{
		{ID: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 30}, Date: 100, Media: photo},
		{ID: 2, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 20}, Date: 100, Media: photo},
		{ID: 3, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 10}, Date: 100, Body: "text"},
		{ID: 4, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 10}, Date: 101, Body: "new text"},
	}
	f := domain.MessageFilter{GlobalSearch: true, MediaCategories: []domain.MediaCategory{domain.MediaCategoryPhoto}, Limit: 1}
	first := filterMessageList(messages, f)
	if len(first.Messages) != 1 || first.Messages[0].ID != 1 || first.Count != 2 {
		t.Fatalf("first %+v", first)
	}
	f.OffsetPeer = first.Messages[0].Peer
	f.OffsetDate = 100
	f.OffsetID = 1
	next := filterMessageList(messages, f)
	if len(next.Messages) != 1 || next.Messages[0].ID != 2 {
		t.Fatalf("next %+v", next)
	}
	f.OffsetPeer = domain.Peer{Type: domain.PeerTypeChannel, ID: 20}
	f.OffsetID = 99
	next = filterMessageList(messages, f)
	if len(next.Messages) != 1 || next.Messages[0].ID != 2 {
		t.Fatalf("equal numeric channel must precede private peer: %+v", next)
	}
	f.MinDate = 100
	if got := filterMessageList(messages, f); len(got.Messages) != 0 {
		t.Fatalf("date boundary %+v", got)
	}
}
