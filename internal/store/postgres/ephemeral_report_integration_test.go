package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestEphemeralReportStoreDurableEvidenceAndIdempotency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now()
	reporter := now.UnixNano()&0x3fffffff + 1000
	sender := reporter + 1
	messageID := int(now.UnixNano()&0x3fffffff) + 1
	message := domain.EphemeralMessage{
		ID: messageID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: reporter + 2},
		SenderUserID: sender, ReceiverUserID: reporter, Date: int(now.Unix()), RandomID: 99,
		Content:      domain.EphemeralContent{Message: "abuse evidence"},
		OriginDevice: domain.EphemeralDevice{UserID: reporter, BusinessAuthKeyID: [8]byte{1, 2, 3}, SessionID: 44},
		Version:      1, CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	report := domain.NewEphemeralAbuseReport(reporter, "spam", "review this", message, now)
	store := NewEphemeralReportStore(pool)
	created, err := store.CreateEphemeralReport(ctx, report)
	if err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM ephemeral_abuse_reports WHERE reporter_user_id = $1", reporter)
	})
	if created, err := store.CreateEphemeralReport(ctx, report); err != nil || created {
		t.Fatalf("retry create=%v err=%v", created, err)
	}
	var evidenceRaw []byte
	var count int
	if err := pool.QueryRow(ctx, `
SELECT evidence, count(*) OVER ()
FROM ephemeral_abuse_reports
WHERE reporter_user_id = $1 AND channel_id = $2 AND ephemeral_message_id = $3
`, reporter, message.Peer.ID, message.ID).Scan(&evidenceRaw, &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d", count)
	}
	var evidence map[string]any
	if err := json.Unmarshal(evidenceRaw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["MessageID"] != float64(message.ID) || evidence["Content"] == nil {
		t.Fatalf("evidence=%s", evidenceRaw)
	}
	if _, leaked := evidence["OriginDevice"]; leaked {
		t.Fatalf("device identity leaked into report evidence: %s", evidenceRaw)
	}
}
