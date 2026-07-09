package maintenance

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

type fakeOutboxRetention struct {
	calls int
}

func (f *fakeOutboxRetention) DeleteFailed(context.Context, time.Duration, int) (int, error) {
	f.calls++
	return 0, nil
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
