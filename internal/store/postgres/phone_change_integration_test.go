package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func postgresPhoneChangeEffects(authKeyID [8]byte, sessionID int64) store.DeliveryEffectsBuilder[store.UserDeliverySnapshot] {
	return func(snapshot store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: snapshot.User.ID, ExcludeAuthKeyID: authKeyID,
			ExcludeSessionID: sessionID, Payload: []byte{1},
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

func TestPhoneChangeStoreUpdatesUserWithoutPTSPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	changes := NewPhoneChangeStore(pool)
	events := NewUpdateEventStore(pool)
	suffix := time.Now().UnixNano() % 1000
	// NANP numbers must be 11 digits ("1650555" + 3 subscriber digits) to pass
	// domain.ValidPhone's strict E.164 check; the suffix keeps each run's set
	// unique across repeated runs against the same database.
	oldPhone := fmt.Sprintf("16505550%03d", (suffix+1)%1000)
	occupiedPhone := fmt.Sprintf("16505550%03d", (suffix+2)%1000)
	newPhone := fmt.Sprintf("16505550%03d", (suffix+3)%1000)
	u1, err := users.Create(ctx, domain.User{AccessHash: 301, Phone: oldPhone, FirstName: "PhoneOne"})
	if err != nil {
		t.Fatalf("create user1: %v", err)
	}
	u2, err := users.Create(ctx, domain.User{AccessHash: 302, Phone: occupiedPhone, FirstName: "PhoneTwo"})
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM edge_delivery_outbox WHERE target_user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM user_update_watermarks WHERE user_id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{u1.ID, u2.ID})
	})

	authKeyID := [8]byte{7, 6, 5, 4}
	wantErr := errors.New("encode phone update failed")
	failedRequest := domain.PhoneChangeRequest{
		UserID: u1.ID, Phone: newPhone, Date: 1700000001,
		ExcludeAuthKeyID: authKeyID, ExcludeSessionID: 77,
	}
	if _, err := changes.ChangePhoneWithDelivery(ctx, failedRequest, func(store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("failed delivery change err = %v, want %v", err, wantErr)
	}
	unchanged, found, err := users.ByID(ctx, u1.ID)
	if err != nil || !found || unchanged.Phone != oldPhone {
		t.Fatalf("user after failed delivery = %+v found=%v err=%v", unchanged, found, err)
	}
	var failedDeliveries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, u1.ID).Scan(&failedDeliveries); err != nil || failedDeliveries != 0 {
		t.Fatalf("deliveries after failed change = %d err=%v, want 0", failedDeliveries, err)
	}

	result, err := changes.ChangePhoneWithDelivery(ctx, domain.PhoneChangeRequest{
		UserID: u1.ID, Phone: newPhone, Date: 1700000001,
		ExcludeAuthKeyID: authKeyID, ExcludeSessionID: 77,
	}, postgresPhoneChangeEffects(authKeyID, 77))
	if err != nil {
		t.Fatalf("change phone: %v", err)
	}
	if !result.Changed || result.User.Phone != newPhone {
		t.Fatalf("result = %+v", result)
	}
	loaded, found, err := users.ByID(ctx, u1.ID)
	if err != nil || !found || loaded.Phone != newPhone {
		t.Fatalf("loaded user = %+v found=%v err=%v", loaded, found, err)
	}
	storedEvents, err := events.ListAfter(ctx, u1.ID, 0, 10)
	if err != nil || len(storedEvents) != 0 {
		t.Fatalf("stored events = %+v err=%v", storedEvents, err)
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1`, u1.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if outboxCount != 0 {
		t.Fatalf("outbox count = %d, want 0", outboxCount)
	}
	var deliveryCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, u1.ID).Scan(&deliveryCount); err != nil || deliveryCount != 1 {
		t.Fatalf("edge delivery count = %d err=%v, want 1", deliveryCount, err)
	}
	var storedAuthKey []byte
	var storedSession int64
	if err := pool.QueryRow(ctx, `SELECT exclude_auth_key_id,exclude_session_id FROM edge_delivery_outbox WHERE target_user_id=$1`, u1.ID).Scan(&storedAuthKey, &storedSession); err != nil {
		t.Fatalf("query edge delivery: %v", err)
	}
	if string(storedAuthKey) != string(authKeyID[:]) || storedSession != 77 {
		t.Fatalf("edge exclusion = %x/%d", storedAuthKey, storedSession)
	}

	// 同号重试是幂等读，不得产生 PTS/event/outbox。
	retry, err := changes.ChangePhoneWithDelivery(ctx, domain.PhoneChangeRequest{UserID: u1.ID, Phone: newPhone, Date: 1700000002}, func(store.UserDeliverySnapshot) ([]store.DeliveryEffect, error) {
		t.Fatal("idempotent phone replay invoked delivery builder")
		return nil, nil
	})
	if err != nil || retry.Changed || retry.User.Phone != newPhone {
		t.Fatalf("idempotent retry = %+v err=%v", retry, err)
	}
	// 改号不分配账号 PTS（updateUserPhone 无 pts），重试同样不得产生
	// PTS/event/outbox。
	if pts, err := events.MaxContiguousPts(ctx, u1.ID); err != nil || pts != 0 {
		t.Fatalf("pts after retry = %d err=%v", pts, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, u1.ID).Scan(&deliveryCount); err != nil || deliveryCount != 1 {
		t.Fatalf("edge deliveries after idempotent retry = %d err=%v, want 1", deliveryCount, err)
	}

	// 冲突更新整体回滚：号码不变，也不产生 PTS/event。
	if _, err := changes.ChangePhoneWithDelivery(ctx, domain.PhoneChangeRequest{UserID: u2.ID, Phone: newPhone}, postgresPhoneChangeEffects([8]byte{}, 0)); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
		t.Fatalf("occupied change err = %v", err)
	}
	loaded2, found, err := users.ByID(ctx, u2.ID)
	if err != nil || !found || loaded2.Phone != occupiedPhone {
		t.Fatalf("occupied rollback user = %+v found=%v err=%v", loaded2, found, err)
	}
	if pts, err := events.MaxContiguousPts(ctx, u2.ID); err != nil || pts != 0 {
		t.Fatalf("occupied rollback pts = %d err=%v", pts, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, u2.ID).Scan(&deliveryCount); err != nil || deliveryCount != 0 {
		t.Fatalf("occupied rollback deliveries = %d err=%v, want 0", deliveryCount, err)
	}
}
