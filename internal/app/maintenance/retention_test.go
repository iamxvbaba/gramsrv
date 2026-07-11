package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type fakeOutboxRetention struct {
	calls     int
	olderThan time.Duration
	limit     int
	deleted   int
}

func (f *fakeOutboxRetention) DeleteFailed(_ context.Context, olderThan time.Duration, limit int) (int, error) {
	f.calls++
	f.olderThan = olderThan
	f.limit = limit
	return f.deleted, nil
}

func TestRetentionWorkerUsesIndependentOutboxPoisonPolicyAndSignalsRelease(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	outbox := &fakeOutboxRetention{deleted: 2}
	w := NewRetentionWorker(outbox, nil, zap.New(core), 7*24*time.Hour, time.Hour, 73).
		WithDispatchOutboxPoisonPolicy(2*time.Minute, 7*time.Second)

	w.runOnce(context.Background())

	if outbox.calls != 1 || outbox.olderThan != 2*time.Minute || outbox.limit != 73 {
		t.Fatalf("outbox poison calls/args = %d/%v/%d, want 1/2m/73", outbox.calls, outbox.olderThan, outbox.limit)
	}
	entries := logs.FilterMessage("terminal failed dispatch_outbox 已结束隔离并释放用户 lane").All()
	if len(entries) != 1 {
		t.Fatalf("poison release error signals = %d, want 1", len(entries))
	}
	if got := entries[0].ContextMap()["signal"]; got != "dispatch_outbox_poison_released" {
		t.Fatalf("poison signal = %v", got)
	}
}

func TestRetentionWorkerOutboxPoisonPolicyDefaultsAreShort(t *testing.T) {
	outbox := &fakeOutboxRetention{}
	w := NewRetentionWorker(outbox, nil, zap.NewNop(), 168*time.Hour, time.Hour, 100).
		WithDispatchOutboxPoisonPolicy(0, 0)
	w.runOutboxPoisonOnce(context.Background())
	if outbox.olderThan != defaultOutboxPoisonRetention || w.outboxPoisonInterval != defaultOutboxPoisonInterval {
		t.Fatalf("default poison policy = %v/%v, want %v/%v", outbox.olderThan, w.outboxPoisonInterval, defaultOutboxPoisonRetention, defaultOutboxPoisonInterval)
	}
}

type fakeTempKeyRetention struct {
	calls         int
	expiredBefore int64
	limit         int
}

func (f *fakeTempKeyRetention) DeleteExpired(_ context.Context, expiredBefore int64, limit int) (int, error) {
	f.calls++
	f.expiredBefore = expiredBefore
	f.limit = limit
	return 3, nil
}

func TestRetentionWorkerReclaimsExpiredTempKeys(t *testing.T) {
	outbox := &fakeOutboxRetention{}
	temp := &fakeTempKeyRetention{}
	w := NewRetentionWorker(outbox, temp, zap.NewNop(), time.Hour, time.Hour, 100)

	w.runOnce(context.Background())

	if outbox.calls != 1 || temp.calls != 1 {
		t.Fatalf("calls outbox=%d temp=%d, want 1/1", outbox.calls, temp.calls)
	}
	if temp.limit != 100 {
		t.Fatalf("limit = %d, want batch 100", temp.limit)
	}
	wantBefore := time.Now().Add(-tempAuthKeyExpiryGrace).Unix()
	if diff := temp.expiredBefore - wantBefore; diff < -5 || diff > 5 {
		t.Fatalf("expiredBefore = %d, want ≈ now-grace (%d)", temp.expiredBefore, wantBefore)
	}
}

func TestRetentionWorkerSkipsNilTempKeyStore(t *testing.T) {
	outbox := &fakeOutboxRetention{}
	w := NewRetentionWorker(outbox, nil, zap.NewNop(), time.Hour, time.Hour, 100)
	w.runOnce(context.Background()) // 不应 panic
	if outbox.calls != 1 {
		t.Fatalf("outbox calls = %d, want 1", outbox.calls)
	}
}

type fakeBotAPIRetention struct {
	calls          int
	confirmedGrace time.Duration
	maxAge         time.Duration
	limit          int
}

type fakeLoginCodeDeliveryRetention struct {
	calls         int
	expiredBefore time.Time
	limit         int
}

func (f *fakeLoginCodeDeliveryRetention) DeleteExpiredLoginCodeDeliveries(_ context.Context, expiredBefore time.Time, limit int) (int, error) {
	f.calls++
	f.expiredBefore = expiredBefore
	f.limit = limit
	return 4, nil
}

func TestRetentionWorkerReclaimsExpiredLoginCodeDeliveryReceipts(t *testing.T) {
	loginCodes := &fakeLoginCodeDeliveryRetention{}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), 168*time.Hour, time.Hour, 83).
		WithLoginCodeDeliveryRetention(loginCodes)
	before := time.Now()
	w.runRetentionOnce(context.Background())
	after := time.Now()
	if loginCodes.calls != 1 || loginCodes.limit != 83 {
		t.Fatalf("login-code retention calls/limit = %d/%d, want 1/83", loginCodes.calls, loginCodes.limit)
	}
	if loginCodes.expiredBefore.Before(before) || loginCodes.expiredBefore.After(after) {
		t.Fatalf("login-code expiry boundary = %v, want within [%v,%v]", loginCodes.expiredBefore, before, after)
	}
}

func (f *fakeBotAPIRetention) DeleteDeliveredOrExpired(_ context.Context, confirmedGrace, maxAge time.Duration, limit int) (int, error) {
	f.calls++
	f.confirmedGrace = confirmedGrace
	f.maxAge = maxAge
	f.limit = limit
	return 5, nil
}

func TestRetentionWorkerReclaimsBotAPIUpdates(t *testing.T) {
	outbox := &fakeOutboxRetention{}
	botAPI := &fakeBotAPIRetention{}
	w := NewRetentionWorker(outbox, nil, zap.NewNop(), time.Hour, time.Hour, 100).
		WithBotAPIUpdateRetention(botAPI, 24*time.Hour)

	w.runOnce(context.Background())

	if botAPI.calls != 1 {
		t.Fatalf("bot api retention calls = %d, want 1", botAPI.calls)
	}
	if botAPI.confirmedGrace != botAPIConfirmedGrace || botAPI.maxAge != 24*time.Hour || botAPI.limit != 100 {
		t.Fatalf("bot api retention args = (%v, %v, %d), want (%v, 24h, 100)",
			botAPI.confirmedGrace, botAPI.maxAge, botAPI.limit, botAPIConfirmedGrace)
	}
}

func TestRetentionWorkerBotAPIRetentionDefaultsTo24h(t *testing.T) {
	botAPI := &fakeBotAPIRetention{}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), time.Hour, time.Hour, 100).
		WithBotAPIUpdateRetention(botAPI, 0)
	w.runOnce(context.Background())
	if botAPI.maxAge != 24*time.Hour {
		t.Fatalf("default bot api retention = %v, want 24h", botAPI.maxAge)
	}
}

type fakeUserUpdateRetention struct {
	calls     int
	olderThan time.Duration
	limit     int
}

func (f *fakeUserUpdateRetention) DeleteConfirmedPrefix(_ context.Context, olderThan time.Duration, limit int) (int, error) {
	f.calls++
	f.olderThan = olderThan
	f.limit = limit
	return 9, nil
}

func TestRetentionWorkerReclaimsOnlyConfirmedUserUpdatePrefix(t *testing.T) {
	const retention = 7 * 24 * time.Hour
	store := &fakeUserUpdateRetention{}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), retention, time.Hour, 91).
		WithUserUpdateRetention(store)
	w.runOnce(context.Background())
	if store.calls != 1 || store.olderThan != retention || store.limit != 91 {
		t.Fatalf("user update retention calls/args = %d/%v/%d, want 1/%v/91", store.calls, store.olderThan, store.limit, retention)
	}
}

type fakeChannelUpdateRetention struct {
	calls     int
	olderThan time.Duration
	limit     int
}

func (f *fakeChannelUpdateRetention) DeleteExpiredChannelUpdateEvents(_ context.Context, olderThan time.Duration, limit int) (int, error) {
	f.calls++
	f.olderThan = olderThan
	f.limit = limit
	return 7, nil
}

func TestRetentionWorkerReclaimsChannelUpdates(t *testing.T) {
	const (
		retention = 14 * 24 * time.Hour
		batch     = 321
	)
	channelUpdates := &fakeChannelUpdateRetention{}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), retention, time.Hour, batch).
		WithChannelUpdateRetention(channelUpdates)

	w.runOnce(context.Background())

	if channelUpdates.calls != 1 {
		t.Fatalf("channel update retention calls = %d, want 1", channelUpdates.calls)
	}
	if channelUpdates.olderThan != retention || channelUpdates.limit != batch {
		t.Fatalf("channel update retention args = (%v, %d), want (%v, %d)",
			channelUpdates.olderThan, channelUpdates.limit, retention, batch)
	}
}

type fakeOrphanAuthKeyRetention struct {
	calls     int
	olderThan time.Duration
	limit     int
	protected [][8]byte
}

func (f *fakeOrphanAuthKeyRetention) DeleteOrphaned(_ context.Context, olderThan time.Duration, limit int, protected [][8]byte) (int, error) {
	f.calls++
	f.olderThan = olderThan
	f.limit = limit
	f.protected = append([][8]byte(nil), protected...)
	return 2, nil
}

type fakeActiveRawAuthKeys struct{ ids [][8]byte }

func (f fakeActiveRawAuthKeys) ActiveRawAuthKeyIDs() [][8]byte {
	return append([][8]byte(nil), f.ids...)
}

func TestRetentionWorkerProtectsActiveRawAuthKeysFromOrphanGC(t *testing.T) {
	store := &fakeOrphanAuthKeyRetention{}
	active := fakeActiveRawAuthKeys{ids: [][8]byte{{1}, {2}}}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), time.Hour, time.Hour, 73).
		WithOrphanAuthKeyRetention(store, active, 24*time.Hour)

	w.runOnce(context.Background())

	if store.calls != 1 || store.olderThan != 24*time.Hour || store.limit != 73 {
		t.Fatalf("orphan retention calls/args = %d/%v/%d, want 1/24h/73", store.calls, store.olderThan, store.limit)
	}
	if len(store.protected) != 2 || store.protected[0] != ([8]byte{1}) || store.protected[1] != ([8]byte{2}) {
		t.Fatalf("protected raw auth keys = %v, want {1},{2}", store.protected)
	}
}

type fakeHeartbeatOrphanRetention struct {
	fakeOrphanAuthKeyRetention
	heartbeatCalls int
	heartbeatIDs   [][8]byte
	heartbeatErr   error
}

func (f *fakeHeartbeatOrphanRetention) TouchActiveRawAuthKeys(_ context.Context, ids [][8]byte) error {
	f.heartbeatCalls++
	f.heartbeatIDs = append([][8]byte(nil), ids...)
	return f.heartbeatErr
}

func TestRetentionWorkerHeartbeatsActiveKeysBeforeOrphanDelete(t *testing.T) {
	store := &fakeHeartbeatOrphanRetention{}
	active := fakeActiveRawAuthKeys{ids: [][8]byte{{3}, {4}}}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.NewNop(), time.Hour, 2*time.Hour, 19).
		WithOrphanAuthKeyRetention(store, active, 3*time.Hour)

	w.runRetentionOnce(context.Background())

	if store.heartbeatCalls != 1 || store.calls != 1 {
		t.Fatalf("heartbeat/delete calls = %d/%d, want 1/1", store.heartbeatCalls, store.calls)
	}
	if len(store.heartbeatIDs) != 2 || store.heartbeatIDs[0] != ([8]byte{3}) || store.heartbeatIDs[1] != ([8]byte{4}) {
		t.Fatalf("heartbeat ids = %v, want {3},{4}", store.heartbeatIDs)
	}
	// min(retention worker interval=2h, orphan retention/3=1h)
	if got := w.orphanHeartbeatInterval(); got != time.Hour {
		t.Fatalf("heartbeat interval = %v, want 1h", got)
	}
}

func TestRetentionWorkerSkipsOrphanDeleteWhenHeartbeatFails(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	store := &fakeHeartbeatOrphanRetention{heartbeatErr: errors.New("db unavailable")}
	active := fakeActiveRawAuthKeys{ids: [][8]byte{{5}}}
	w := NewRetentionWorker(&fakeOutboxRetention{}, nil, zap.New(core), time.Hour, 30*time.Minute, 11).
		WithOrphanAuthKeyRetention(store, active, 24*time.Hour)
	if got := w.orphanHeartbeatInterval(); got != 30*time.Minute {
		t.Fatalf("heartbeat interval = %v, want worker interval 30m", got)
	}

	w.runRetentionOnce(context.Background())

	if store.heartbeatCalls != 1 || store.calls != 0 {
		t.Fatalf("heartbeat/delete calls = %d/%d, want 1/0", store.heartbeatCalls, store.calls)
	}
	entries := logs.FilterMessage("刷新 active raw auth key heartbeat 失败，本轮跳过 orphan GC").All()
	if len(entries) != 1 || entries[0].ContextMap()["signal"] != "auth_key_heartbeat_failed" {
		t.Fatalf("heartbeat failure signals = %+v", entries)
	}
}
