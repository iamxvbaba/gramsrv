package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type fakeStoryReadModelCache struct {
	mu      sync.Mutex
	peers   []domain.Peer
	viewers []int64
	flushes int
}

type fakeRPCProjectionReadModelCache struct {
	users []int64
}

func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForViewer(int64) {}
func (f *fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForUser(id int64) {
	f.users = append(f.users, id)
}
func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForPeer(int64, domain.Peer) {
}
func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForChannel(int64) {}
func (*fakeRPCProjectionReadModelCache) FlushRPCProjectionReadModel()                     {}

func (f *fakeStoryReadModelCache) InvalidateStoryReadModelViewers(ids ...int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.viewers = append(f.viewers, ids...)
}

func (f *fakeStoryReadModelCache) InvalidateStoryReadModelPeer(peer domain.Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = append(f.peers, peer)
}

func (f *fakeStoryReadModelCache) FlushStoryReadModelCache() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

func (f *fakeStoryReadModelCache) peersSnapshot() []domain.Peer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Peer(nil), f.peers...)
}

func (f *fakeStoryReadModelCache) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

func countPeer(peers []domain.Peer, want domain.Peer) int {
	n := 0
	for _, p := range peers {
		if p == want {
			n++
		}
	}
	return n
}

func TestReadModelChangeListenerRoutesStoryPeer(t *testing.T) {
	stories := &fakeStoryReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{Stories: stories}, nil)

	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2}`)
	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3}`)
	peers := stories.peersSnapshot()
	if len(peers) != 2 ||
		peers[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 777}) ||
		peers[1] != (domain.Peer{Type: domain.PeerTypeChannel, ID: 888}) {
		t.Fatalf("story_peer routing = %+v, want user 777 + channel 888", peers)
	}

	// peer_id==0 或 peer_type 非法都不应触发失效。
	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)
	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"","peer_id":999,"version":5}`)
	if got := len(stories.peersSnapshot()); got != 2 {
		t.Fatalf("invalid story_peer events should be ignored: peers=%d, want 2", got)
	}
}

func TestReadModelChangeListenerRoutesUserVisibility(t *testing.T) {
	stories := &fakeStoryReadModelCache{}
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		Stories:        stories,
		RPCProjections: rpcProjections,
	}, nil)

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2}`)
	if len(rpcProjections.users) != 1 || rpcProjections.users[0] != 777 {
		t.Fatalf("RPC projection invalidations = %v, want [777]", rpcProjections.users)
	}
	if peers := stories.peersSnapshot(); len(peers) != 1 || peers[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 777}) {
		t.Fatalf("story projection invalidations = %+v, want user 777", peers)
	}

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3}`)
	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)
	if len(rpcProjections.users) != 1 || len(stories.peersSnapshot()) != 1 {
		t.Fatalf("invalid visibility events were not ignored: users=%v peers=%+v", rpcProjections.users, stories.peersSnapshot())
	}
}

// TestStoryPeerReadModelNotifyInvalidatesOnStoryWrite 验证 0135 触发器:写 stories /
// story_hidden_peers → story_peer bump → 统一 read-model NOTIFY → 按 owner peer 失效故事投影。
func TestStoryPeerReadModelNotifyInvalidatesOnStoryWrite(t *testing.T) {
	pool := testPool(t)
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	ctx := context.Background()

	const ownerID int64 = 913500777
	const viewerID int64 = 913500888
	cleanup := func() {
		_, _ = pool.Exec(ctx, "DELETE FROM stories WHERE owner_peer_type='user' AND owner_peer_id=$1", ownerID)
		_, _ = pool.Exec(ctx, "DELETE FROM story_hidden_peers WHERE owner_peer_type='user' AND owner_peer_id=$1", ownerID)
		_, _ = pool.Exec(ctx, "DELETE FROM read_model_versions WHERE model='story_peer' AND peer_id=$1", ownerID)
	}
	cleanup()
	t.Cleanup(cleanup)

	stories := &fakeStoryReadModelCache{}
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener := NewReadModelChangeListener(dsn, ReadModelCacheSet{Stories: stories}, nil)
	go listener.Run(lctx)
	if !waitUntil(2*time.Second, func() bool { return stories.flushCount() >= 1 }) {
		t.Fatal("read model listener 未在预期内连接并 flush")
	}

	wantPeer := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}

	// INSERT story → story_peer bump → NOTIFY → 失效 owner peer。
	if _, err := pool.Exec(ctx, `
INSERT INTO stories (owner_peer_type, owner_peer_id, story_id, date, expire_date)
VALUES ('user', $1, 1, 100, 200)`, ownerID); err != nil {
		t.Fatalf("insert story: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) >= 1 }) {
		t.Fatalf("stories INSERT 后 story_peer NOTIFY 未失效 owner peer; got %+v", stories.peersSnapshot())
	}

	// story_hidden_peers 写也走同一 owner peer 失效。
	before := countPeer(stories.peersSnapshot(), wantPeer)
	if _, err := pool.Exec(ctx, `
INSERT INTO story_hidden_peers (viewer_user_id, owner_peer_type, owner_peer_id)
VALUES ($1, 'user', $2)`, viewerID, ownerID); err != nil {
		t.Fatalf("insert story_hidden_peers: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) > before }) {
		t.Fatalf("story_hidden_peers INSERT 后未再失效 owner peer")
	}

	// 持久版本脊确实 bump 了 story_peer(owner_user_id=0, peer=user/ownerID)。
	var version int64
	if err := pool.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model='story_peer' AND owner_user_id=0 AND peer_type='user' AND peer_id=$1`, ownerID).Scan(&version); err != nil {
		t.Fatalf("read story_peer version: %v", err)
	}
	if version < 2 {
		t.Fatalf("story_peer version = %d, want >=2 (stories + hidden writes)", version)
	}

	// DELETE story 也应失效(用 OLD.owner_peer_*)。
	before = countPeer(stories.peersSnapshot(), wantPeer)
	if _, err := pool.Exec(ctx, `DELETE FROM stories WHERE owner_peer_type='user' AND owner_peer_id=$1`, ownerID); err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) > before }) {
		t.Fatalf("stories DELETE 后未失效 owner peer")
	}
}
