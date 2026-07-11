package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

func pgAlbumItem(randomID int64, label string) domain.AlbumGroupReservationItem {
	sum := sha256.Sum256([]byte(label))
	return domain.AlbumGroupReservationItem{RandomID: randomID, IntentHash: sum[:]}
}

func pgAlbumReq(sender int64, peer domain.Peer, group int64, items ...domain.AlbumGroupReservationItem) domain.AlbumGroupReservationRequest {
	return domain.AlbumGroupReservationRequest{
		SenderUserID:      sender,
		Peer:              peer,
		Items:             items,
		ProposedGroupedID: group,
	}
}

func TestAlbumGroupReservationConvergesAcrossPostgresInstances(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 7601, Phone: "+1760" + suffix + "01", FirstName: "AlbumSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 7602, Phone: "+1760" + suffix + "02", FirstName: "AlbumRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM album_group_reservations WHERE sender_user_id = $1", sender.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	privatePeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	full := []domain.AlbumGroupReservationItem{
		pgAlbumItem(76001, "one"),
		pgAlbumItem(76002, "two"),
		pgAlbumItem(76003, "three"),
	}
	firstStore := NewMessageStore(pool)
	groupedID, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 761, full...))
	if err != nil || groupedID != 761 {
		t.Fatalf("reserve full = %d err=%v, want 761", groupedID, err)
	}
	// 新 store 实例模拟另一进程；失败子集必须恢复首次整包的组。
	replayed, err := NewMessageStore(pool).ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 762, full[1:]...))
	if err != nil || replayed != groupedID {
		t.Fatalf("reserve subset = %d err=%v, want %d", replayed, err, groupedID)
	}
	if _, err := NewMessageStore(pool).ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 763, pgAlbumItem(76002, "changed"))); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed intent err=%v, want ErrMessageRandomIDDuplicate", err)
	}

	// 同 random_id 在不同 peer 作用域独立，不会误并相册。
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: recipient.ID}
	channelGroup, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, channelPeer, 764, full[0]))
	if err != nil || channelGroup != 764 {
		t.Fatalf("channel peer isolated group = %d err=%v, want 764", channelGroup, err)
	}

	// 两个实例同时预留部分重叠的批次，shared random_id 的 advisory lock 必须让
	// 两边串行收敛；最终 4/5/6 三个 item 全部同组。
	left := pgAlbumReq(sender.ID, privatePeer, 765, pgAlbumItem(76004, "four"), pgAlbumItem(76005, "shared"))
	right := pgAlbumReq(sender.ID, privatePeer, 766, pgAlbumItem(76005, "shared"), pgAlbumItem(76006, "six"))
	requests := []domain.AlbumGroupReservationRequest{left, right}
	results := make([]int64, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = NewMessageStore(pool).ReserveAlbumGroup(ctx, requests[i])
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0] == 0 || results[0] != results[1] {
		t.Fatalf("concurrent groups=%v errs=%v, want one non-zero group", results, errs)
	}
	for _, item := range []domain.AlbumGroupReservationItem{left.Items[0], left.Items[1], right.Items[1]} {
		got, err := NewMessageStore(pool).ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 767, item))
		if err != nil || got != results[0] {
			t.Fatalf("verify random_id %d = %d err=%v, want %d", item.RandomID, got, err, results[0])
		}
	}

	// 一个请求同时命中两个历史组必须整体失败，不能绑定其中的新 item。
	oldA := pgAlbumItem(76007, "old-a")
	oldB := pgAlbumItem(76008, "old-b")
	newItem := pgAlbumItem(76009, "new")
	if _, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 768, oldA)); err != nil {
		t.Fatal(err)
	}
	if _, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 769, oldB)); err != nil {
		t.Fatal(err)
	}
	if _, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 770, oldA, oldB, newItem)); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("mixed old groups err=%v, want ErrMessageRandomIDDuplicate", err)
	}
	fresh, err := firstStore.ReserveAlbumGroup(ctx, pgAlbumReq(sender.ID, privatePeer, 771, newItem))
	if err != nil || fresh != 771 {
		t.Fatalf("post-conflict fresh item = %d err=%v, want 771", fresh, err)
	}
}
