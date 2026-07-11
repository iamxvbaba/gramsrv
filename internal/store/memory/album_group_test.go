package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

func albumIntent(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func albumReq(sender int64, peer domain.Peer, groupedID int64, items ...domain.AlbumGroupReservationItem) domain.AlbumGroupReservationRequest {
	return domain.AlbumGroupReservationRequest{
		SenderUserID:      sender,
		Peer:              peer,
		Items:             items,
		ProposedGroupedID: groupedID,
	}
}

func albumItem(randomID int64, label string) domain.AlbumGroupReservationItem {
	return domain.AlbumGroupReservationItem{RandomID: randomID, IntentHash: albumIntent(label)}
}

func TestAlbumGroupReservationFullThenSubsetAndIntentConflict(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	full := []domain.AlbumGroupReservationItem{albumItem(1, "one"), albumItem(2, "two"), albumItem(3, "three")}

	groupedID, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 101, full...))
	if err != nil || groupedID != 101 {
		t.Fatalf("reserve full = %d err=%v, want 101", groupedID, err)
	}
	replayed, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 202, full[1:]...))
	if err != nil || replayed != groupedID {
		t.Fatalf("reserve subset = %d err=%v, want original %d", replayed, err, groupedID)
	}
	for _, item := range full {
		got, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 303, item))
		if err != nil || got != groupedID {
			t.Fatalf("single random_id %d = %d err=%v, want %d", item.RandomID, got, err, groupedID)
		}
	}
	changed := albumItem(2, "changed payload")
	if _, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 404, changed)); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed intent err=%v, want ErrMessageRandomIDDuplicate", err)
	}
}

func TestAlbumGroupReservationConcurrentOverlapConverges(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 9001}
	requests := []domain.AlbumGroupReservationRequest{
		albumReq(1001, peer, 111, albumItem(11, "one"), albumItem(12, "shared")),
		albumReq(1001, peer, 222, albumItem(12, "shared"), albumItem(13, "three")),
	}
	results := make([]int64, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = messages.ReserveAlbumGroup(ctx, requests[i])
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] == 0 || results[0] != results[1] {
		t.Fatalf("concurrent results=%v errs=%v, want same non-zero group", results, errs)
	}
	for _, item := range []domain.AlbumGroupReservationItem{albumItem(11, "one"), albumItem(12, "shared"), albumItem(13, "three")} {
		got, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 333, item))
		if err != nil || got != results[0] {
			t.Fatalf("converged random_id %d = %d err=%v, want %d", item.RandomID, got, err, results[0])
		}
	}
}

func TestAlbumGroupReservationRejectsMixedOldGroupsAtomically(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	one := albumItem(21, "one")
	two := albumItem(22, "two")
	three := albumItem(23, "three")
	if _, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 121, one)); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 122, two)); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 123, one, two, three)); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("mixed old groups err=%v, want ErrMessageRandomIDDuplicate", err)
	}
	// 失败批次不能把尚未存在的 random_id 23 偷绑到任一旧组。
	got, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, peer, 124, three))
	if err != nil || got != 124 {
		t.Fatalf("post-conflict unbound item = %d err=%v, want fresh 124", got, err)
	}
}

func TestAlbumGroupReservationScopeIncludesPeer(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	item := albumItem(31, "same intent")
	tests := []struct {
		peer  domain.Peer
		group int64
	}{
		{peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2002}, group: 131},
		{peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2003}, group: 132},
		{peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 2002}, group: 133},
	}
	for _, tc := range tests {
		got, err := messages.ReserveAlbumGroup(ctx, albumReq(1001, tc.peer, tc.group, item))
		if err != nil || got != tc.group {
			t.Fatalf("peer %+v = %d err=%v, want isolated %d", tc.peer, got, err, tc.group)
		}
	}
}
