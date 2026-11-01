package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// boostClock is an advanceable monotonic clock for boost cache TTL tests (injected as readmodelcache.Config.Now).
type boostClock struct {
	mu sync.Mutex
	t  int64
}

func newBoostClock(unix int64) *boostClock { return &boostClock{t: unix} }

func (b *boostClock) now() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Unix(b.t, 0)
}

func (b *boostClock) set(unix int64) {
	b.mu.Lock()
	b.t = unix
	b.mu.Unlock()
}

func TestChannelBoostCacheNilDisabled(t *testing.T) {
	if c := NewChannelBoostCache(0, time.Second); c != nil {
		t.Fatalf("max<=0 should return nil (disabled), got %v", c)
	}
	if c := NewChannelBoostCache(16, 0); c != nil {
		t.Fatalf("ttl<=0 should return nil (disabled), got %v", c)
	}
	var nilCache *ChannelBoostCache
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 10}
	nilCache.put(1, peer, 2)
	nilCache.putPeerTotal(peer, 2)
	nilCache.delete(1, peer)
	nilCache.deletePeerTotal(peer)
	nilCache.deleteUser(1)
	nilCache.flush()
	if _, ok := nilCache.get(1, peer); ok {
		t.Fatalf("nil cache get must return ok=false")
	}
	if _, ok := nilCache.getPeerTotal(peer); ok {
		t.Fatalf("nil cache getPeerTotal must return ok=false")
	}
}

func TestChannelBoostCachePutGetExpireDelete(t *testing.T) {
	clk := newBoostClock(1000)
	c := newChannelBoostCache(16, 10*time.Second, clk.now)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 10}
	otherPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 11}

	c.put(100, peer, 3)
	c.put(100, otherPeer, 1)

	clk.set(1005) // < 1000+10: still valid
	if got, ok := c.get(100, peer); !ok || got != 3 {
		t.Fatalf("get before ttl = %d ok=%v, want 3/true", got, ok)
	}
	if got, ok := c.get(100, otherPeer); !ok || got != 1 {
		t.Fatalf("other before ttl = %d ok=%v, want 1/true", got, ok)
	}

	clk.set(1010) // == 1000+10: expiresAt<=now is treated as expired
	if _, ok := c.get(100, peer); ok {
		t.Fatalf("expiresAt==now should be treated as expired")
	}

	// delete invalidates exactly (rewrite both peers within the freshness window so expireAt=1020).
	c.put(100, peer, 4)
	c.put(100, otherPeer, 1)
	c.delete(100, peer)
	clk.set(1011)
	if _, ok := c.get(100, peer); ok {
		t.Fatalf("should not hit after delete")
	}
	if got, ok := c.get(100, otherPeer); !ok || got != 1 {
		t.Fatalf("delete exact should not affect other channels: %d ok=%v", got, ok)
	}

	c.deleteUser(100)
	if _, ok := c.get(100, otherPeer); ok {
		t.Fatalf("after deleteUser the same user's other channels should also be invalidated")
	}
}

func TestChannelBoostCachePeerTotalPutGetExpireDelete(t *testing.T) {
	clk := newBoostClock(1000)
	c := newChannelBoostCache(16, 10*time.Second, clk.now)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 10}
	otherPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 11}

	c.putPeerTotal(peer, 30)
	c.putPeerTotal(otherPeer, 11)

	clk.set(1005)
	if got, ok := c.getPeerTotal(peer); !ok || got != 30 {
		t.Fatalf("peer total before ttl = %d ok=%v, want 30/true", got, ok)
	}
	if got, ok := c.getPeerTotal(otherPeer); !ok || got != 11 {
		t.Fatalf("other peer total before ttl = %d ok=%v, want 11/true", got, ok)
	}

	clk.set(1010)
	if _, ok := c.getPeerTotal(peer); ok {
		t.Fatalf("peer total expiresAt==now should be treated as expired")
	}

	c.putPeerTotal(peer, 31)
	c.putPeerTotal(otherPeer, 12)
	c.deletePeerTotal(peer)
	clk.set(1011)
	if _, ok := c.getPeerTotal(peer); ok {
		t.Fatalf("should not hit after deletePeerTotal")
	}
	if got, ok := c.getPeerTotal(otherPeer); !ok || got != 12 {
		t.Fatalf("deletePeerTotal should not affect other channels: %d ok=%v", got, ok)
	}
}

func TestChannelBoostCacheCapFlush(t *testing.T) {
	c := NewChannelBoostCache(2, time.Minute)
	ch := func(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeChannel, ID: id} }
	c.put(1, ch(1), 1)
	c.put(1, ch(2), 1)
	c.put(1, ch(3), 1) // over cap=2: LRU evicts the oldest single entry ch1, not the whole table
	if _, ok := c.get(1, ch(1)); ok {
		t.Fatalf("over the limit the oldest entry (ch1) should be evicted")
	}
	if _, ok := c.get(1, ch(2)); !ok {
		t.Fatalf("single-entry LRU eviction should keep the next-newest entry (ch2), proving it is not a whole-table flush")
	}
	if got, ok := c.get(1, ch(3)); !ok || got != 1 {
		t.Fatalf("the newest entry (ch3) should be present: %d ok=%v", got, ok)
	}
	c.flush()
	if _, ok := c.get(1, ch(3)); ok {
		t.Fatalf("should not hit after flush")
	}
}

func TestChannelBoostCacheSingleflightsColdLoad(t *testing.T) {
	ctx := context.Background()
	c := NewChannelBoostCache(16, time.Minute)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 42}
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (int, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return 7, nil
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			boosts, err := c.getOrLoad(ctx, 100, peer, load)
			if err != nil {
				errs <- err
				return
			}
			if boosts != 7 {
				errs <- fmt.Errorf("boosts = %d, want 7", boosts)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
	if boosts, err := c.getOrLoad(ctx, 100, peer, load); err != nil || boosts != 7 {
		t.Fatalf("cached getOrLoad = %d/%v, want 7/nil", boosts, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache hit called load again: calls=%d", got)
	}
}

func TestChannelBoostCachePeerTotalSingleflightsColdLoad(t *testing.T) {
	ctx := context.Background()
	c := NewChannelBoostCache(16, time.Minute)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 42}
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	load := func() (int, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return 70, nil
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			boosts, err := c.getPeerTotalOrLoad(ctx, peer, load)
			if err != nil {
				errs <- err
				return
			}
			if boosts != 70 {
				errs <- fmt.Errorf("peer boosts = %d, want 70", boosts)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("peer total load calls = %d, want 1", got)
	}
	if boosts, err := c.getPeerTotalOrLoad(ctx, peer, load); err != nil || boosts != 70 {
		t.Fatalf("cached peer total = %d/%v, want 70/nil", boosts, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("peer total cache hit called load again: calls=%d", got)
	}
}

func TestReadModelChangeListenerInvalidatesChannelBoostCache(t *testing.T) {
	boosts := NewChannelBoostCache(16, time.Minute)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 7}
	otherPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 8}
	boosts.put(100, peer, 2)
	boosts.put(100, otherPeer, 3)
	boosts.put(200, peer, 4)
	boosts.putPeerTotal(peer, 10)
	boosts.putPeerTotal(otherPeer, 11)

	listener := NewReadModelChangeListener("", ReadModelCacheSet{ChannelBoosts: boosts}, nil)
	listener.handlePayload(`{"model":"channel_self_boosts","owner_user_id":100,"peer_type":"channel","peer_id":7}`)

	if _, ok := boosts.get(100, peer); ok {
		t.Fatalf("a channel_self_boosts event should invalidate the exact user/channel")
	}
	if _, ok := boosts.getPeerTotal(peer); ok {
		t.Fatalf("a channel_self_boosts event should invalidate the channel total")
	}
	if got, ok := boosts.get(100, otherPeer); !ok || got != 3 {
		t.Fatalf("other channels should not be invalidated: %d ok=%v", got, ok)
	}
	if got, ok := boosts.get(200, peer); !ok || got != 4 {
		t.Fatalf("other users should not be invalidated: %d ok=%v", got, ok)
	}
	if got, ok := boosts.getPeerTotal(otherPeer); !ok || got != 11 {
		t.Fatalf("other channels' total should not be invalidated: %d ok=%v", got, ok)
	}
}

func TestChannelStoreInvalidateBoostCacheForUserAndPeers(t *testing.T) {
	boosts := NewChannelBoostCache(16, time.Minute)
	store := &ChannelStore{boostCache: boosts}
	oldPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 7}
	newPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 8}
	otherPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 9}

	boosts.put(100, oldPeer, 1)
	boosts.put(100, newPeer, 1)
	boosts.put(200, oldPeer, 1)
	boosts.putPeerTotal(oldPeer, 10)
	boosts.putPeerTotal(newPeer, 11)
	boosts.putPeerTotal(otherPeer, 12)

	store.invalidateBoostCacheForUserAndPeers(100, map[domain.Peer]struct{}{
		oldPeer: {},
		newPeer: {},
	})

	if _, ok := boosts.get(100, oldPeer); ok {
		t.Fatalf("the current user's old peer self boost cache should be invalidated")
	}
	if _, ok := boosts.get(100, newPeer); ok {
		t.Fatalf("the current user's new peer self boost cache should be invalidated")
	}
	if got, ok := boosts.get(200, oldPeer); !ok || got != 1 {
		t.Fatalf("other users' self boost cache should not be invalidated: %d ok=%v", got, ok)
	}
	if _, ok := boosts.getPeerTotal(oldPeer); ok {
		t.Fatalf("the old peer total cache should be invalidated")
	}
	if _, ok := boosts.getPeerTotal(newPeer); ok {
		t.Fatalf("the new peer total cache should be invalidated")
	}
	if got, ok := boosts.getPeerTotal(otherPeer); !ok || got != 12 {
		t.Fatalf("other channels' total cache should not be invalidated: %d ok=%v", got, ok)
	}
}
