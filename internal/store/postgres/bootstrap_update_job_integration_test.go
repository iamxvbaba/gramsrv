package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestBootstrapUpdateJobPostgresSameAuthKeyReconnectTakesOverPendingSession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createLoginCodeDeliveryTestUser(t, ctx, pool, "bootstrap-reconnect")
	msg, err := NewMessageStore(pool).Create(ctx, domain.Message{
		OwnerUserID: user.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        int(time.Now().Unix()),
		Body:        "Login code: 12345",
	})
	if err != nil {
		t.Fatalf("create bootstrap message: %v", err)
	}
	bootstrap := NewBootstrapUpdateJobStore(pool)
	authKeyID := [8]byte{1, 3, 5, 7}
	const (
		oldSessionID = int64(11001)
		newSessionID = int64(22002)
	)
	job, err := bootstrap.EnqueueLoginMessage(ctx, domain.BootstrapUpdateJob{
		Kind: domain.BootstrapUpdateJobLoginMessage, UserID: user.ID,
		AuthKeyID: authKeyID, SessionID: oldSessionID, MessageBoxID: msg.ID,
	})
	if err != nil {
		t.Fatalf("enqueue bootstrap: %v", err)
	}
	if ready, err := bootstrap.MarkReadyForSession(ctx, user.ID, [8]byte{9}, newSessionID); err != nil || ready != 0 {
		t.Fatalf("different-auth ready=%d err=%v, want 0/nil", ready, err)
	}
	ready, err := bootstrap.MarkReadyForSession(ctx, user.ID, authKeyID, newSessionID)
	if err != nil || ready != 1 {
		t.Fatalf("same-auth reconnect ready=%d err=%v, want 1/nil", ready, err)
	}
	var status string
	var sessionID int64
	if err := pool.QueryRow(ctx, `SELECT status, session_id FROM bootstrap_update_jobs WHERE id = $1`, job.ID).Scan(&status, &sessionID); err != nil {
		t.Fatalf("load bootstrap job: %v", err)
	}
	if status != string(domain.BootstrapUpdateJobReady) || sessionID != newSessionID {
		t.Fatalf("bootstrap status/session = %s/%d, want ready/%d", status, sessionID, newSessionID)
	}
}
