package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"telesrv/internal/domain"
)

// The original ledger/concurrency fixtures now obtain a real database grant.
// Authorization rejection tests below call the raw store without this helper.
type authorizedWalletTransferFixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	store  *WalletTransferStore
	mu     sync.Mutex
	proofs map[string]domain.WalletGramTransferRequest
}

func newAuthorizedWalletTransferStore(t *testing.T, pool *pgxpool.Pool, messages *MessageStore) *authorizedWalletTransferFixture {
	return &authorizedWalletTransferFixture{t: t, pool: pool, store: NewWalletTransferStore(pool, messages, 0), proofs: make(map[string]domain.WalletGramTransferRequest)}
}

func (f *authorizedWalletTransferFixture) prepare(ctx context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.OriginSessionID == 0 {
		req.OriginSessionID = 1
	}
	if req.CommandKey == "" {
		req.CommandKey = fmt.Sprintf("auth-test:%d:%d:%d", req.FromUserID, req.ToUserID, time.Now().UnixNano())
	}
	if prior, ok := f.proofs[req.CommandKey]; ok {
		req.OriginAuthKeyID = prior.OriginAuthKeyID
		req.AuthorizationHash = prior.AuthorizationHash
		return req, nil
	}
	if req.FromUserID <= 0 || req.ToUserID <= 0 || req.FromUserID == req.ToUserID || req.AmountNanoton <= 0 {
		return req, nil
	}
	var key [8]byte
	if _, err := rand.Read(key[:]); err != nil {
		return req, err
	}
	saveRevokeTestAuthKey(f.t, ctx, NewAuthKeyStore(f.pool), key, 0)
	auths := NewAuthorizationStore(f.pool)
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: key, UserID: req.FromUserID}); err != nil {
		return req, err
	}
	a, _, err := auths.ByAuthKey(ctx, key)
	if err != nil {
		return req, err
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO account_passwords(user_id,has_password) VALUES($1,true) ON CONFLICT(user_id) DO UPDATE SET has_password=true`, req.FromUserID); err != nil {
		return req, err
	}
	var version time.Time
	if err = f.pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id=$1`, req.FromUserID).Scan(&version); err != nil {
		return req, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return req, err
	}
	req.OriginAuthKeyID = key
	req.AuthorizationHash = sha256.Sum256(secret)
	clear(secret)
	err = NewTmpPasswordStore(f.pool).PutTmpPassword(ctx, domain.TmpPasswordGrant{UserID: req.FromUserID, AuthKeyID: key, AuthorizationCreatedAt: a.CreatedAt, PasswordChangedAt: version, SecretHash: req.AuthorizationHash, ValidUntil: time.Now().Add(time.Minute)})
	if err != nil {
		return req, err
	}
	f.proofs[req.CommandKey] = req
	return req, nil
}

func (f *authorizedWalletTransferFixture) TransferWalletGram(ctx context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error) {
	req, err := f.prepare(ctx, req)
	if err != nil {
		return domain.WalletGramTransferResult{}, err
	}
	return f.store.TransferWalletGram(ctx, req)
}

func TestWalletTransferAuthorizationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	sender := createTestUser(t, ctx, users, "+1791"+suffix, "SecureSender", "")
	receiver := createTestUser(t, ctx, users, "+1792"+suffix, "SecureRecipient", "")
	fundWalletGram(t, ctx, pool, sender.ID, 1000)
	f := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))
	base := domain.WalletGramTransferRequest{FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 10, Reason: domain.StarsReasonTransfer, Date: int(time.Now().Unix()), Comment: "authorized"}
	for _, name := range []string{"missing", "wrong proof", "other session", "expired", "old proof", "password changed", "login changed"} {
		t.Run(name, func(t *testing.T) {
			req := base
			req.CommandKey = "secure:" + suffix + ":" + name
			req, err := f.prepare(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				req.AuthorizationHash = [32]byte{}
			case "wrong proof":
				req.AuthorizationHash[0] ^= 1
			case "other session":
				req.OriginAuthKeyID = [8]byte{99}
			case "expired":
				_, err = pool.Exec(ctx, `UPDATE account_tmp_passwords SET valid_until=now()-interval '1 second' WHERE auth_key_id=$1`, authKeyIDToInt64(req.OriginAuthKeyID))
			case "old proof":
				_, err = pool.Exec(ctx, `UPDATE account_tmp_passwords SET created_at=now()-interval '121 seconds' WHERE auth_key_id=$1`, authKeyIDToInt64(req.OriginAuthKeyID))
			case "password changed":
				// The password-version trigger advances on an actual SRP verifier change.
				_, err = pool.Exec(ctx, `UPDATE account_passwords SET srp_verifier=decode('07','hex') WHERE user_id=$1`, sender.ID)
			case "login changed":
				_, err = pool.Exec(ctx, `SELECT pg_sleep(0.01)`)
				if err != nil {
					t.Fatal(err)
				}
				err = NewAuthorizationStore(pool).Bind(ctx, domain.Authorization{AuthKeyID: req.OriginAuthKeyID, UserID: sender.ID})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.TransferWalletGram(ctx, req); err == nil {
				t.Fatal("unauthorized transfer accepted")
			}
			if walletGramBalance(t, ctx, pool, sender.ID) != 1000 || walletGramBalance(t, ctx, pool, receiver.ID) != 0 {
				t.Fatal("rejected proof changed balances")
			}
		})
	}
	base.CommandKey = "secure:" + suffix + ":once"
	req, err := f.prepare(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.store.TransferWalletGram(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replay := req
	replay.Date += 5
	replay.OriginSessionID = 999
	second, err := f.store.TransferWalletGram(ctx, replay)
	if err != nil || !second.Duplicate || second.Send.SenderMessage.ID == 0 || second.Send.SenderMessage.ID != first.Send.SenderMessage.ID || second.Date != first.Date {
		t.Fatalf("receipt replay: duplicate=%v msg=%d err=%v", second.Duplicate, second.Send.SenderMessage.ID, err)
	}
	changed := req
	changed.CommandKey += "-captured-replay"
	if _, err = f.store.TransferWalletGram(ctx, changed); !errors.Is(err, domain.ErrTmpPasswordInvalid) {
		t.Fatalf("spent proof reused: %v", err)
	}
	changed = req
	changed.Comment = "different purpose"
	if _, err = f.store.TransferWalletGram(ctx, changed); err == nil {
		t.Fatal("changed payload replayed")
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != 990 || walletGramBalance(t, ctx, pool, receiver.ID) != 10 {
		t.Fatal("replay changed balances")
	}
	// An insufficient-balance rollback must not burn the approval token.
	base.CommandKey = "secure:" + suffix + ":rollback"
	base.AmountNanoton = 2000
	req, err = f.prepare(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.TransferWalletGram(ctx, req); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatal(err)
	}
	fundWalletGram(t, ctx, pool, sender.ID, 3000)
	if _, err = f.store.TransferWalletGram(ctx, req); err != nil {
		t.Fatalf("grant lost on rollback: %v", err)
	}
}
