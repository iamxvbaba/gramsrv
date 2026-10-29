package files

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// epochChurnStore lands an unrelated sticker-set write while every lookup is in
// flight, which moves the cache-wide epoch before the lookup can publish.
type epochChurnStore struct {
	*fakeMediaStore
	svc   *Service
	reads atomic.Int64
}

func (s *epochChurnStore) GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	n := s.reads.Add(1)
	s.svc.stickerSetCache.put(domain.StickerSet{ID: 900 + n, ShortName: fmt.Sprintf("Other%d", n)}, nil)
	return s.fakeMediaStore.GetDocuments(ctx, ids)
}

// TestResolveStickerSetBoundsRetriesUnderEpochChurn pins that a cold lookup
// stops re-reading after stickerSetPublishAttempts when unrelated writes keep
// moving the epoch: it answers with its last read and leaves it uncached
// instead of looping until the request context ends.
func TestResolveStickerSetBoundsRetriesUnderEpochChurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	media := &epochChurnStore{fakeMediaStore: newFakeMediaStore()}
	if err := media.PutDocument(ctx, domain.Document{ID: 1000001}); err != nil {
		t.Fatal(err)
	}
	if err := media.PutStickerSet(ctx, domain.StickerSet{
		ID: 1, AccessHash: 2, CreatorUserID: 42, ShortName: "ChurnSet", Title: "Churn",
		Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{1000001},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(media, nil, 2)
	media.svc = svc
	ref := domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: 1, AccessHash: 2}

	set, docs, found, err := svc.ResolveStickerSet(ctx, ref)
	if err != nil {
		t.Fatalf("resolve under epoch churn: %v", err)
	}
	if !found || set.Title != "Churn" || len(docs) != 1 || docs[0].ID != 1000001 {
		t.Fatalf("resolve = set=%+v docs=%d found=%v, want the stored set", set, len(docs), found)
	}
	if got := media.reads.Load(); got != stickerSetPublishAttempts {
		t.Fatalf("document reads = %d, want %d bounded attempts", got, stickerSetPublishAttempts)
	}
	if _, _, cached := svc.stickerSetCache.get(ref); cached {
		t.Fatal("a snapshot read across concurrent writes was published to the cache")
	}
}
