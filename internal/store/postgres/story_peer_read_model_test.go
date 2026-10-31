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

func (*fakeRPCProjectionReadModelCache) InvalidateRPCProjectionReadModelForViewer(int64) {}
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

type fakeUserProjectionFactCache struct {
	freezes []int64
	phones  []int64
	flushes int
}

func (f *fakeUserProjectionFactCache) InvalidateAccountFreezeFact(userID int64) {
	f.freezes = append(f.freezes, userID)
}

func (f *fakeUserProjectionFactCache) InvalidateCollectiblePhoneFact(userID int64) {
	f.phones = append(f.phones, userID)
}

func (f *fakeUserProjectionFactCache) FlushUserProjectionFactReadModel() {
	f.flushes++
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

func TestReadModelChangeListenerRoutesStoryHiddenList(t *testing.T) {
	stories := &fakeStoryReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{Stories: stories}, nil)

	listener.handlePayload(`{"model":"story_hidden_list","owner_user_id":777,"peer_type":"user","peer_id":777,"version":2}`)
	listener.handlePayload(`{"model":"story_hidden_list","owner_user_id":888,"peer_type":"channel","peer_id":888,"version":3}`)
	listener.handlePayload(`{"model":"story_hidden_list","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)

	stories.mu.Lock()
	viewers := append([]int64(nil), stories.viewers...)
	stories.mu.Unlock()
	if len(viewers) != 1 || viewers[0] != 777 {
		t.Fatalf("story hidden-list viewer invalidations = %v, want [777]", viewers)
	}
}

func TestReadModelChangeListenerRoutesUserVisibility(t *testing.T) {
	stories := &fakeStoryReadModelCache{}
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	facts := &fakeUserProjectionFactCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		Stories:             stories,
		RPCProjections:      rpcProjections,
		UserProjectionFacts: facts,
	}, nil)

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2}`)
	if len(rpcProjections.users) != 1 || rpcProjections.users[0] != 777 {
		t.Fatalf("RPC projection invalidations = %v, want [777]", rpcProjections.users)
	}
	if peers := stories.peersSnapshot(); len(peers) != 1 || peers[0] != (domain.Peer{Type: domain.PeerTypeUser, ID: 777}) {
		t.Fatalf("story projection invalidations = %+v, want user 777", peers)
	}
	if len(facts.freezes) != 1 || facts.freezes[0] != 777 {
		t.Fatalf("freeze fact invalidations = %v, want [777]", facts.freezes)
	}

	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3}`)
	listener.handlePayload(`{"model":"user_visibility","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4}`)
	if len(rpcProjections.users) != 1 || len(stories.peersSnapshot()) != 1 || len(facts.freezes) != 1 {
		t.Fatalf("invalid visibility events were not ignored: users=%v peers=%+v", rpcProjections.users, stories.peersSnapshot())
	}
}

func TestReadModelChangeListenerRoutesUserCollectiblePhone(t *testing.T) {
	facts := &fakeUserProjectionFactCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{UserProjectionFacts: facts}, nil)
	listener.handlePayload(`{"model":"user_collectible_phone","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2,"hash":101}`)
	listener.handlePayload(`{"model":"user_collectible_phone","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3,"hash":102}`)
	listener.handlePayload(`{"model":"user_collectible_phone","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4,"hash":103}`)
	if len(facts.phones) != 1 || facts.phones[0] != 777 {
		t.Fatalf("collectible phone fact invalidations = %v, want [777]", facts.phones)
	}
	listener.flush("test")
	if facts.flushes != 1 {
		t.Fatalf("user projection fact flushes = %d, want 1", facts.flushes)
	}
}

func TestReadModelChangeListenerRoutesPeerIdentity(t *testing.T) {
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{RPCProjections: rpcProjections}, nil)

	listener.handlePayload(`{"model":"peer_identity","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2,"hash":101}`)
	listener.handlePayload(`{"model":"peer_identity","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3,"hash":102}`)
	listener.handlePayload(`{"model":"peer_identity","owner_user_id":0,"peer_type":"chat","peer_id":999,"version":4,"hash":103}`)
	listener.handlePayload(`{"model":"peer_identity","owner_user_id":0,"peer_type":"user","peer_id":0,"version":5,"hash":104}`)

	if len(rpcProjections.users) != 1 || rpcProjections.users[0] != 777 {
		t.Fatalf("user peer identity invalidations = %v, want [777]", rpcProjections.users)
	}
	if len(rpcProjections.channels) != 1 || rpcProjections.channels[0] != 888 {
		t.Fatalf("channel peer identity invalidations = %v, want [888]", rpcProjections.channels)
	}
}

func TestReadModelChangeListenerRoutesLogicalUserDeletionAsCoarseInvalidation(t *testing.T) {
	rpcProjections := &fakeRPCProjectionReadModelCache{}
	baseUsers := &fakeBaseUserCache{}
	facts := &fakeUserProjectionFactCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{
		RPCProjections:      rpcProjections,
		BaseUsers:           baseUsers,
		UserProjectionFacts: facts,
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
	if len(facts.freezes) != 1 || facts.freezes[0] != 777 || len(facts.phones) != 1 || facts.phones[0] != 777 {
		t.Fatalf("user projection fact invalidations freezes=%v phones=%v, want [777]/[777]", facts.freezes, facts.phones)
	}

	listener.handlePayload(`{"model":"user_deleted","owner_user_id":888,"peer_type":"channel","peer_id":888,"version":1}`)
	if rpcProjections.flushes != 1 || len(baseUsers.deletedIDs) != 1 || len(facts.freezes) != 1 || len(facts.phones) != 1 {
		t.Fatalf("invalid user_deleted event was not ignored: flushes=%d base=%v freezes=%v phones=%v",
			rpcProjections.flushes, baseUsers.deletedIDs, facts.freezes, facts.phones)
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
		_, _ = pool.Exec(ctx, "DELETE FROM read_model_versions WHERE model='story_hidden_list' AND owner_user_id=$1", viewerID)
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
	if !waitUntil(3*time.Second, func() bool {
		stories.mu.Lock()
		defer stories.mu.Unlock()
		for _, viewer := range stories.viewers {
			if viewer == viewerID {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("after story_hidden_peers INSERT the story_hidden_list NOTIFY did not invalidate viewer=%d", viewerID)
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
	var hiddenListVersion int64
	if err := pool.QueryRow(ctx, `
SELECT version FROM read_model_versions
WHERE model='story_hidden_list' AND owner_user_id=$1 AND peer_type='user' AND peer_id=$1`, viewerID).Scan(&hiddenListVersion); err != nil {
		t.Fatalf("read story_hidden_list version: %v", err)
	}
	if hiddenListVersion < 1 {
		t.Fatalf("story_hidden_list version = %d, want >=1 after hidden write", hiddenListVersion)
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
