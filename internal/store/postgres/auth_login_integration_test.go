package postgres

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestAuthSignUpDoesNotWriteOfficialLoginMessagePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	phone := fmt.Sprintf("1555%d31", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE phone = $1", phone)
	})

	users := NewUserStore(pool)
	dialogs := NewDialogStore(pool)
	messages := NewMessageStore(pool)
	svc := appauth.NewService(
		users,
		NewAuthorizationStore(pool),
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		appauth.WithLoginCodeDelivery(messages),
	)

	var authKeyID [8]byte
	var authKeyBody [256]byte
	if _, err := rand.Read(authKeyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authKeyBody[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(pool).Save(ctx, store.AuthKeyData{ID: authKeyID, Value: authKeyBody}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(authKeyID))
	})
	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345"); err != nil || !needSignUp {
		t.Fatalf("SignIn needSignUp = %v err = %v, want need sign-up", needSignUp, err)
	}

	u, msg, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "PgLogin", "Test")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if u.Phone != phone || msg.ID != 0 {
		t.Fatalf("sign-up user/message = user %+v message %+v, want user and zero message", u, msg)
	}

	systemUser, found, err := users.ByID(ctx, domain.OfficialSystemUserID)
	if err != nil || !found || !systemUser.Verified || !systemUser.Support {
		t.Fatalf("official system user = %+v found=%v err=%v, want seeded verified support user", systemUser, found, err)
	}
	list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list.Dialogs) != 0 || len(list.Messages) != 0 || len(list.Users) != 0 {
		t.Fatalf("SignUp created official dialog state: %+v", list)
	}
}

func TestAuthSendCodeOfficialLoginMessagePreservesReadWatermarkBeforeSignInPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	phone := fmt.Sprintf("1555%d32", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE phone = $1", phone)
	})

	users := NewUserStore(pool)
	messages := NewMessageStore(pool)
	svc := appauth.NewService(
		users,
		NewAuthorizationStore(pool),
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		appauth.WithLoginCodeDelivery(messages),
	)

	var authKeyID [8]byte
	var authKeyBody [256]byte
	if _, err := rand.Read(authKeyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authKeyBody[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(pool).Save(ctx, store.AuthKeyData{ID: authKeyID, Value: authKeyBody}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(authKeyID))
	})

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345"); err != nil || !needSignUp {
		t.Fatalf("SignIn before signup needSignUp=%v err=%v, want true/nil", needSignUp, err)
	}
	u, signUpMessage, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "PgLogin", "Read")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if signUpMessage.ID != 0 {
		t.Fatalf("SignUp returned login message %+v, want zero", signUpMessage)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}

	assertOfficialDialog := func(wantTop, wantRead, wantUnread int) {
		t.Helper()
		var top, readMax, unread, computed int
		if err := pool.QueryRow(ctx, `
WITH target AS (
  SELECT top_message_id, read_inbox_max_id, unread_count
  FROM dialogs
  WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2
)
SELECT
  target.top_message_id,
  target.read_inbox_max_id,
  target.unread_count,
  (
    SELECT count(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = $1
      AND m.peer_type = 'user'
      AND m.peer_id = $2
      AND NOT m.outgoing
      AND NOT m.deleted
      AND m.box_id > target.read_inbox_max_id
  ) AS computed_unread
FROM target`, u.ID, domain.OfficialSystemUserID).Scan(&top, &readMax, &unread, &computed); err != nil {
			t.Fatalf("query official dialog state: %v", err)
		}
		if top != wantTop || readMax != wantRead || unread != wantUnread || computed != wantUnread {
			t.Fatalf("official dialog top=%d read=%d unread=%d computed=%d, want top=%d read=%d unread=%d",
				top, readMax, unread, computed, wantTop, wantRead, wantUnread)
		}
	}
	latestLoginMessage := func(wantCount int) domain.Message {
		t.Helper()
		history, err := messages.ListByUser(ctx, u.ID, domain.MessageFilter{
			HasPeer: true,
			Peer:    peer,
			Limit:   10,
		})
		if err != nil || len(history.Messages) != wantCount {
			t.Fatalf("official history count=%d err=%v, want %d", len(history.Messages), err, wantCount)
		}
		latest := history.Messages[0]
		for _, msg := range history.Messages[1:] {
			if msg.ID > latest.ID {
				latest = msg
			}
		}
		return latest
	}

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode first signin: %v", err)
	}
	first := latestLoginMessage(1)
	assertOfficialDialog(first.ID, 0, 1)
	_, lateSecond, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn first needSignUp=%v err=%v", needSignUp, err)
	}
	if lateSecond.ID != 0 {
		t.Fatalf("SignIn first returned late login message %+v, want zero", lateSecond)
	}
	assertOfficialDialog(first.ID, 0, 1)
	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: u.ID,
		Peer:        peer,
		MaxID:       domain.MaxMessageBoxID,
		Date:        int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("ReadHistory first login message: %v", err)
	}
	if read.MaxID != first.ID || read.StillUnreadCount != 0 {
		t.Fatalf("read first login message = %+v, want max_id %d unread 0", read, first.ID)
	}

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode second signin: %v", err)
	}
	second := latestLoginMessage(2)
	assertOfficialDialog(second.ID, first.ID, 1)
	_, lateThird, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn second needSignUp=%v err=%v", needSignUp, err)
	}
	if lateThird.ID != 0 {
		t.Fatalf("SignIn second returned late login message %+v, want zero", lateThird)
	}
	assertOfficialDialog(second.ID, first.ID, 1)
}
