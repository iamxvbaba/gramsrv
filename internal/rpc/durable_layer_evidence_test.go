package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestDurableSessionLayerSurvivesRestartAndRejectsOldSelectorRollback(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{1}
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	olderID := int64(proto.NewMessageIDGen(func() time.Time { return now.Add(-time.Second) }).New(proto.MessageFromClient))
	newerID := int64(proto.NewMessageIDGen(func() time.Time { return now }).New(proto.MessageFromClient))
	first := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	if layer, msgID, publish, err := first.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, 10, 225, olderID); err != nil || layer != 225 || msgID != olderID || !publish {
		t.Fatalf("first evidence = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}
	if layer, msgID, publish, err := first.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, 10, 227, newerID); err != nil || layer != 227 || msgID != newerID || !publish {
		t.Fatalf("newer evidence = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}

	// A fresh Router has no process-local registry. The durable raw-session
	// watermark restores the exact codec before a naked same-session replay.
	restarted := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	layer, msgID, found, err := restarted.ResolveNegotiatedSessionLayerEvidence(ctx, authKeyID, 10)
	if err != nil || !found || layer != 227 || msgID != newerID {
		t.Fatalf("restart restore = (%d,%d,%v,%v)", layer, msgID, found, err)
	}
	layer, msgID, publish, err := restarted.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, 10, 225, olderID)
	if err != nil || layer != 227 || msgID != newerID || !publish {
		t.Fatalf("old selector after restart = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}
	key, found, err := keys.Get(ctx, authKeyID)
	if err != nil || !found || key.Layer != 227 {
		t.Fatalf("durable default rolled back: key=%+v found=%v err=%v", key, found, err)
	}
}

func TestDurableSessionLayerResolveRefreshesStaleRouterFromSharedStore(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{1, 0xa2}
	const sessionID = int64(102)
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	msgIDs := proto.NewMessageIDGen(func() time.Time { return now })
	oldMsgID := msgIDs.New(proto.MessageFromClient)
	newMsgID := msgIDs.New(proto.MessageFromClient)
	routerA := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	routerB := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	if layer, msgID, _, err := routerA.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, sessionID, 225, oldMsgID); err != nil || layer != 225 || msgID != oldMsgID {
		t.Fatalf("A advance = (%d,%d,%v)", layer, msgID, err)
	}
	if layer, msgID, found, err := routerA.ResolveNegotiatedSessionLayerEvidence(ctx, authKeyID, sessionID); err != nil || !found || layer != 225 || msgID != oldMsgID {
		t.Fatalf("A initial resolve = (%d,%d,%v,%v)", layer, msgID, found, err)
	}
	if layer, msgID, _, err := routerB.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, sessionID, 227, newMsgID); err != nil || layer != 227 || msgID != newMsgID {
		t.Fatalf("B advance = (%d,%d,%v)", layer, msgID, err)
	}

	// A still has 225 in its process-local accelerator. Durable-store mode must
	// read primary first and converge both the return value and that accelerator.
	if layer, msgID, found, err := routerA.ResolveNegotiatedSessionLayerEvidence(ctx, authKeyID, sessionID); err != nil || !found || layer != 227 || msgID != newMsgID {
		t.Fatalf("A refreshed resolve = (%d,%d,%v,%v), want B's 227", layer, msgID, found, err)
	}
	if layer, msgID, found := routerA.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !found || layer != 227 || msgID != newMsgID {
		t.Fatalf("A refreshed local cache = (%d,%d,%v)", layer, msgID, found)
	}
}

func TestDurableSessionLayerFutureProfileCanBeCorrectedByGreaterSelector(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{2}
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	futureID := int64(proto.NewMessageIDGen(func() time.Time { return now }).New(proto.MessageFromClient))
	correctID := int64(proto.NewMessageIDGen(func() time.Time { return now.Add(time.Second) }).New(proto.MessageFromClient))
	if _, applied, err := keys.AdvanceSessionLayer(ctx, authKeyID, 20, 229, futureID); err != nil || !applied {
		t.Fatalf("seed future evidence = applied %v err %v", applied, err)
	}
	r := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	layer, msgID, found, err := r.ResolveNegotiatedSessionLayerEvidence(ctx, authKeyID, 20)
	if err != nil || !found || layer != 229 || msgID != futureID {
		t.Fatalf("future restore = (%d,%d,%v,%v)", layer, msgID, found, err)
	}
	if _, _, cached := r.NegotiatedSessionLayerEvidence(authKeyID, 20); cached {
		t.Fatal("unsupported future profile polluted typed process cache")
	}
	layer, msgID, publish, err := r.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, 20, 228, correctID)
	if err != nil || layer != 228 || msgID != correctID || !publish {
		t.Fatalf("future self-heal = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}
}

func TestDurableSessionLayerExpiryCoversAcceptedFutureSkew(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{3}
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	msgID := int64(proto.NewMessageIDGen(func() time.Time { return now.Add(29 * time.Second) }).New(proto.MessageFromClient))
	r := New(Config{}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	if _, _, _, err := r.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, 30, 227, msgID); err != nil {
		t.Fatal(err)
	}
	value, found, err := keys.GetSessionLayer(ctx, authKeyID, 30)
	if err != nil || !found {
		t.Fatalf("future-skew evidence = found %v err %v", found, err)
	}
	wantMin := proto.MessageID(msgID).Time().Add(300 * time.Second)
	if value.ExpiresAt.Before(wantMin) {
		t.Fatalf("future-skew expiry = %v, want >= %v", value.ExpiresAt, wantMin)
	}
}

type unavailableSessionLayerStore struct {
	store.AuthKeySessionLayerStore
	err error
}

func (s unavailableSessionLayerStore) GetSessionLayer(context.Context, [8]byte, int64) (store.AuthKeySessionLayer, bool, error) {
	return store.AuthKeySessionLayer{}, false, s.err
}

type mutableSessionLayerStore struct {
	store.AuthKeySessionLayerStore
	value store.AuthKeySessionLayer
}

func (s *mutableSessionLayerStore) GetSessionLayer(context.Context, [8]byte, int64) (store.AuthKeySessionLayer, bool, error) {
	return s.value, true, nil
}

func TestDurableSessionLayerCacheOrdersRebuiltRowsByObservationID(t *testing.T) {
	now := time.Now().UTC()
	primary := &mutableSessionLayerStore{value: store.AuthKeySessionLayer{
		Layer: 227, MessageID: 10_000, ObservationID: 10, ExpiresAt: now.Add(time.Hour),
	}}
	r := New(Config{}, Deps{AuthKeySessionLayers: primary}, zaptest.NewLogger(t), clock.System)
	authKeyID := [8]byte{3, 0xb5}
	const sessionID = int64(305)
	if layer, msgID, found, err := r.ResolveNegotiatedSessionLayerEvidence(context.Background(), authKeyID, sessionID); err != nil || !found || layer != 227 || msgID != 10_000 {
		t.Fatalf("first row = (%d,%d,%v,%v)", layer, msgID, found, err)
	}

	// An expired durable row may be rebuilt with a lower client msg_id. The
	// globally increasing observation id, not msg_id, orders those row lifetimes.
	primary.value = store.AuthKeySessionLayer{
		Layer: 225, MessageID: 100, ObservationID: 11, ExpiresAt: now.Add(time.Hour),
	}
	if layer, msgID, found, err := r.ResolveNegotiatedSessionLayerEvidence(context.Background(), authKeyID, sessionID); err != nil || !found || layer != 225 || msgID != 100 {
		t.Fatalf("rebuilt row = (%d,%d,%v,%v)", layer, msgID, found, err)
	}
	if layer, msgID, found := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !found || layer != 225 || msgID != 100 {
		t.Fatalf("rebuilt local cache = (%d,%d,%v)", layer, msgID, found)
	}
}

func TestDurableSessionLayerAvailabilityErrorCarriesStructuralMarker(t *testing.T) {
	boom := errors.New("database unavailable")
	r := New(Config{}, Deps{AuthKeySessionLayers: unavailableSessionLayerStore{err: boom}}, zaptest.NewLogger(t), clock.System)
	_, _, _, err := r.ResolveNegotiatedSessionLayerEvidence(context.Background(), [8]byte{4}, 40)
	var marker interface{ LayerEvidenceDurabilityUnavailable() }
	if !errors.Is(err, boom) || !errors.As(err, &marker) {
		t.Fatalf("availability error = %v, marker=%v", err, marker != nil)
	}
}

func TestDurableInheritedLayerRevalidatesEachNewSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{5}
	keys := memory.NewAuthKeyStore()
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
		authKeyID: {Layer: 225, LayerObservationID: 1},
	}}
	r := New(Config{}, Deps{Auth: auth, AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)
	if layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, authKeyID); err != nil || !found || layer != 225 {
		t.Fatalf("initial default = (%d,%v,%v)", layer, found, err)
	}
	auth.authKeyClientInfos[authKeyID] = domain.AuthKeyClientInfo{Layer: 229, LayerObservationID: 2}
	if layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, authKeyID); err != nil || !found || layer != 0 {
		t.Fatalf("future authoritative default = (%d,%v,%v)", layer, found, err)
	}
	auth.authKeyClientInfos[authKeyID] = domain.AuthKeyClientInfo{Layer: 228, LayerObservationID: 3}
	if layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, authKeyID); err != nil || !found || layer != 228 {
		t.Fatalf("corrected default = (%d,%v,%v)", layer, found, err)
	}
	if auth.authKeyInfoLookups != 3 {
		t.Fatalf("auth_keys lookups = %d, want one per new session resolution", auth.authKeyInfoLookups)
	}
}
