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
	users    []int64
	channels []int64
	flushes  int
}

type fakeAccountFreezeReadModelCache struct {
	users   []int64
	flushes int
}

func (f *fakeAccountFreezeReadModelCache) InvalidateAccountFreezeReadModel(userID int64) {
	f.users = append(f.users, userID)
}

func (f *fakeAccountFreezeReadModelCache) FlushAccountFreezeReadModel() {
	f.flushes++
}

func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForViewer(int64) {}
func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForContactOwner(int64) {
}
func (f *fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForUser(id int64) {
	f.users = append(f.users, id)
}
func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForPeer(int64, domain.Peer) {
}
func (f *fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForChannel(id int64) {
	f.channels = append(f.channels, id)
}
func (f *fakeRPCProjectionReadModelCache) FlushRPCProjectionReadModel() {
	f.flushes++
}

type fakeBaseUserCache struct {
	deletedIDs []int64
}

func (f *fakeBaseUserCache) Delete(_ context.Context, ids []int64) error {
	f.deletedIDs = append(f.deletedIDs, ids...)
	return nil
}

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

	// Neither peer_id==0 nor an invalid peer_type should trigger invalidation.
	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)
	listener.handlePayload(`{"model":"story_peer","owner_user_id":0,"peer_type":"","peer_id":999,"version":5}`)
	if got := len(stories.peersSnapshot()); got != 2 {
		t.Fatalf("invalid story_peer events should be ignored: peers=%d, want 2", got)
	}
}

func TestReadModelChangeListenerRoutesUserVisibility(t *testing.T) {
	stories := &fakeStoryReadModelCache{}
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	accountFreezes := &fakeAccountFreezeReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		Stories:        stories,
		RPCProjections: rpcProjections,
		AccountFreezes: accountFreezes,
	}, nil)

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2}`)
	if len(rpcProjections.users) != 1 || rpcProjections.users[0] != 777 {
		t.Fatalf("RPC projection invalidations = %v, want [777]", rpcProjections.users)
	}
	if peers := stories.peersSnapshot(); len(peers) != 1 || peers[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 777}) {
		t.Fatalf("story projection invalidations = %+v, want user 777", peers)
	}
	if len(accountFreezes.users) != 1 || accountFreezes.users[0] != 777 {
		t.Fatalf("account freeze invalidations = %v, want [777]", accountFreezes.users)
	}

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3}`)
	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)
	if len(rpcProjections.users) != 1 || len(stories.peersSnapshot()) != 1 || len(accountFreezes.users) != 1 {
		t.Fatalf("invalid visibility events were not ignored: users=%v peers=%+v freezes=%v", rpcProjections.users, stories.peersSnapshot(), accountFreezes.users)
	}
	listener.flush()
	if accountFreezes.flushes != 1 {
		t.Fatalf("account freeze reconnect flushes=%d want=1", accountFreezes.flushes)
	}
}

func TestReadModelChangeListenerRoutesLogicalUserDeletionAsCoarseInvalidation(t *testing.T) {
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	baseUsers := &fakeBaseUserCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		RPCProjections: rpcProjections,
		BaseUsers:      baseUsers,
	}, nil)

	listener.handlePayload(`{"model":"user_deleted","owner_user_id":777,"peer_type":"user","peer_id":777,"version":1}`)
	if rpcProjections.flushes != 1 {
		t.Fatalf("RPC projection flushes = %d, want 1", rpcProjections.flushes)
	}
	if len(rpcProjections.users) != 0 {
		t.Fatalf("logical deletion used per-user scans: %v", rpcProjections.users)
	}
	if len(baseUsers.deletedIDs) != 1 || baseUsers.deletedIDs[0] != 777 {
		t.Fatalf("base user invalidations = %v, want [777]", baseUsers.deletedIDs)
	}

	listener.handlePayload(`{"model":"user_deleted","owner_user_id":888,"peer_type":"channel","peer_id":888,"version":1}`)
	if rpcProjections.flushes != 1 || len(baseUsers.deletedIDs) != 1 {
		t.Fatalf("invalid user_deleted event was not ignored: flushes=%d base=%v", rpcProjections.flushes, baseUsers.deletedIDs)
	}
}

// TestStoryPeerReadModelNotifyInvalidatesOnStoryWrite verifies the 0135 trigger: writing stories /
// story_hidden_peers -> story_peer bump -> unified read-model NOTIFY -> invalidate the story projection by owner peer.
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
		t.Fatal("read model listener did not connect and flush within the expected time")
	}

	wantPeer := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}

	// INSERT story -> story_peer bump -> NOTIFY -> invalidate the owner peer.
	if _, err := pool.Exec(ctx, `
INSERT INTO stories (owner_peer_type, owner_peer_id, story_id, date, expire_date)
VALUES ('user', $1, 1, 100, 200)`, ownerID); err != nil {
		t.Fatalf("insert story: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) >= 1 }) {
		t.Fatalf("after stories INSERT the story_peer NOTIFY did not invalidate the owner peer; got %+v", stories.peersSnapshot())
	}

	// A story_hidden_peers write also goes through the same owner peer invalidation.
	before := countPeer(stories.peersSnapshot(), wantPeer)
	if _, err := pool.Exec(ctx, `
INSERT INTO story_hidden_peers (viewer_user_id, owner_peer_type, owner_peer_id)
VALUES ($1, 'user', $2)`, viewerID, ownerID); err != nil {
		t.Fatalf("insert story_hidden_peers: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) > before }) {
		t.Fatalf("after story_hidden_peers INSERT the owner peer was not invalidated again")
	}

	// The durable version spine really did bump story_peer (owner_user_id=0, peer=user/ownerID).
	var version int64
	if err := pool.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model='story_peer' AND owner_user_id=0 AND peer_type='user' AND peer_id=$1`, ownerID).Scan(&version); err != nil {
		t.Fatalf("read story_peer version: %v", err)
	}
	if version < 2 {
		t.Fatalf("story_peer version = %d, want >=2 (stories + hidden writes)", version)
	}

	// DELETE story should invalidate too (using OLD.owner_peer_*).
	before = countPeer(stories.peersSnapshot(), wantPeer)
	if _, err := pool.Exec(ctx, `DELETE FROM stories WHERE owner_peer_type='user' AND owner_peer_id=$1`, ownerID); err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if !waitUntil(3*time.Second, func() bool { return countPeer(stories.peersSnapshot(), wantPeer) > before }) {
		t.Fatalf("after stories DELETE the owner peer was not invalidated")
	}
}
