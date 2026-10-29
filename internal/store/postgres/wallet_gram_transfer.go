package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

// WalletTransferStore owns internal wallet debits and their chat receipts.
type WalletTransferStore struct {
	db            sqlcgen.DBTX
	messages      *MessageStore
	startingGrant int64
}

func NewWalletTransferStore(db sqlcgen.DBTX, messages *MessageStore, startingGrant int64) *WalletTransferStore {
	return &WalletTransferStore{db: db, messages: messages, startingGrant: startingGrant}
}

func walletTransferFingerprint(req domain.WalletGramTransferRequest) [32]byte {
	// Delivery origin/session and wall time may change on a transport retry.
	req.Date = 0
	req.OriginSessionID = 0
	req.Title = ""
	// Linked addresses are presentation data, not transfer intent. A recipient
	// may replace their wallet between a committed transfer and its retry.
	req.PeerAddress = ""
	data, _ := json.Marshal(req)
	return sha256.Sum256(data)
}
func walletTransferMessageRandomID(req domain.WalletGramTransferRequest) int64 {
	digest := sha256.Sum256([]byte("wallet-transfer-message:" + req.CommandKey))
	value := int64(binary.LittleEndian.Uint64(digest[:8]))
	if value == 0 {
		return 1
	}
	return value
}

// The grant is validated against live password/login versions and consumed in
// the very transaction that debits the balance. Rollback restores the grant.
// A receipt is checked first, so an identical retry needs no second password.
func consumeWalletTransferGrant(ctx context.Context, tx pgx.Tx, req domain.WalletGramTransferRequest) error {
	var enabled bool
	var passwordChangedAt time.Time
	err := tx.QueryRow(ctx, `SELECT has_password,password_changed_at FROM account_passwords WHERE user_id=$1 FOR SHARE`, req.FromUserID).Scan(&enabled, &passwordChangedAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return domain.ErrTmpPasswordInvalid
	}
	if err != nil {
		return err
	}
	var owner int64
	var pending bool
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT user_id,password_pending,created_at FROM authorizations WHERE auth_key_id=$1 FOR SHARE`, authKeyIDToInt64(req.OriginAuthKeyID)).Scan(&owner, &pending, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (owner != req.FromUserID || pending)) {
		return domain.ErrTmpPasswordInvalid
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM account_tmp_passwords WHERE user_id=$1 AND auth_key_id=$2 AND secret_hash=$3 AND password_changed_at=$4 AND authorization_created_at=$5 AND valid_until>clock_timestamp() AND created_at>clock_timestamp()-interval '120 seconds'`, req.FromUserID, authKeyIDToInt64(req.OriginAuthKeyID), req.AuthorizationHash[:], passwordChangedAt, createdAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrTmpPasswordInvalid
	}
	return nil
}

func (s *WalletTransferStore) TransferWalletGram(ctx context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error) {
	// Also enforce this at the debit boundary, not only during config loading.
	if s.startingGrant != 0 {
		return domain.WalletGramTransferResult{}, domain.ErrWalletTransferStartingGrant
	}
	fromUserID, toUserID, amountNanoton := req.FromUserID, req.ToUserID, req.AmountNanoton
	if fromUserID <= 0 || toUserID <= 0 || fromUserID == toUserID || amountNanoton <= 0 || req.Date <= 0 || strings.TrimSpace(req.CommandKey) == "" || req.AuthorizationHash == ([32]byte{}) || req.OriginAuthKeyID == ([8]byte{}) || s.messages == nil {
		return domain.WalletGramTransferResult{}, domain.ErrStarGiftOwnerInvalid
	}
	commandKey := strings.TrimSpace(req.CommandKey)
	fingerprint := walletTransferFingerprint(req)
	var result domain.WalletGramTransferResult
	if s.messages != nil {
		releaseLanes, err := s.messages.privateSendLanes.acquire(ctx, fromUserID, toUserID)
		if err != nil {
			return domain.WalletGramTransferResult{}, err
		}
		defer releaseLanes()
	}
	err := withTx(ctx, s.db, "transfer wallet gram", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, fromUserID, toUserID); err != nil {
			return err
		}
		if commandKey != "" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, commandKey); err != nil {
				return err
			}
			var storedFrom, storedTo, storedAmount int64
			var stored int64
			var storedTxn int64
			var storedHash []byte
			var messageID int64
			var transferDate int
			err := tx.QueryRow(ctx, `
SELECT balance_after, transaction_id, from_user_id, to_user_id, amount_nanoton,request_hash,COALESCE(message_random_id,0),COALESCE(transfer_date,0) FROM wallet_gram_commands WHERE command_key=$1`, commandKey).
				Scan(&stored, &storedTxn, &storedFrom, &storedTo, &storedAmount, &storedHash, &messageID, &transferDate)
			if err == nil {
				if storedFrom != fromUserID || storedTo != toUserID || storedAmount != amountNanoton || !bytes.Equal(storedHash, fingerprint[:]) {
					return domain.ErrStarGiftIdempotencyConflict
				}
				// The replay answers with the first attempt's result, so the
				// caller sees the same balance and the same ledger row.
				result.Balance = stored
				result.TransactionID = storedTxn
				result.Date = transferDate
				result.Duplicate = true
				messages := *s.messages
				messages.db, messages.q = tx, sqlcgen.New(tx)
				replay, found, err := messages.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{SenderUserID: fromUserID, RecipientUserID: toUserID, RandomID: messageID, IdempotencyFingerprint: fingerprint[:]})
				if err != nil {
					return err
				}
				if !found {
					return domain.ErrStarGiftIdempotencyConflict
				}
				result.Send = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// Recheck inside the transaction so non-RPC callers cannot bypass the
		// recipient's block list. Refusal leaves the approval token untouched.
		blocked, err := NewContactStore(tx).IsBlocked(ctx, toUserID, fromUserID)
		if err != nil {
			return err
		}
		if blocked {
			return domain.ErrWalletTransferRecipientBlocked
		}
		if err := consumeWalletTransferGrant(ctx, tx, req); err != nil {
			return err
		}
		result.Date = req.Date
		// Lock both balances in a fixed order. FOR UPDATE on the balance rows
		// serialises concurrent transfers between the same accounts.
		first, second := fromUserID, toUserID
		if second < first {
			first, second = second, first
		}
		for _, userID := range []int64{first, second} {
			if _, err := tx.Exec(ctx, `
SELECT balance_nanoton FROM ton_balances WHERE user_id=$1 FOR UPDATE`, userID); err != nil {
				return err
			}
		}
		var fromBalance int64
		if err := tx.QueryRow(ctx, `
UPDATE ton_balances SET balance_nanoton=balance_nanoton-$2,updated_at=now()
 WHERE user_id=$1 AND balance_nanoton>=$2 RETURNING balance_nanoton`, fromUserID, amountNanoton).Scan(&fromBalance); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarsInsufficient
			}
			return err
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO ton_balances(user_id,balance_nanoton,granted,updated_at)
VALUES($1,$2,false,now())
ON CONFLICT (user_id) DO UPDATE
SET balance_nanoton=ton_balances.balance_nanoton+EXCLUDED.balance_nanoton,updated_at=now()
RETURNING balance_nanoton`, toUserID, amountNanoton).Scan(&result.Balance); err != nil {
			return err
		}
		comment := strings.TrimSpace(req.Comment)
		if _, err := tx.Exec(ctx, `
INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date,comment)
VALUES($1,$2,$3,'user',$4,$5,$6)`,
			toUserID, amountNanoton, string(req.Reason), fromUserID, req.Date, comment); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date,comment)
VALUES($1,$2,$3,'user',$4,$5,$6) RETURNING id`,
			fromUserID, -amountNanoton, string(req.Reason), toUserID, req.Date, comment).Scan(&result.TransactionID); err != nil {
			return err
		}
		// The service message joins this transaction, so the chat and the
		// ledger cannot disagree.
		if s.messages != nil {
			sent, err := s.sendGramTransferMessage(ctx, tx, req, result.TransactionID, comment)
			if err != nil {
				return err
			}
			result.Send = sent
		}
		if commandKey == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO wallet_gram_commands(command_key,kind,from_user_id,to_user_id,amount_nanoton,balance_after,transaction_id,created_at,authorization_hash,request_hash,message_random_id,transfer_date)
VALUES($1,'transfer',$2,$3,$4,$5,$6,now(),$7,$8,$9,$10)`, commandKey, fromUserID, toUserID, amountNanoton, result.Balance, result.TransactionID, req.AuthorizationHash[:], fingerprint[:], walletTransferMessageRandomID(req), req.Date); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.WalletGramTransferResult{}, err
	}
	return result, nil
}

func (s *WalletTransferStore) sendGramTransferMessage(ctx context.Context, tx pgx.Tx, req domain.WalletGramTransferRequest, transactionID int64, comment string) (domain.SendPrivateTextResult, error) {
	fingerprint := walletTransferFingerprint(req)
	title := req.Title
	if title == "" {
		title = "Gram transfer"
	}
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionGramTransfer,
			GramTransfer: &domain.MessageGramTransferAction{
				Amount:      req.AmountNanoton,
				PeerAddress: strings.TrimSpace(req.PeerAddress),
				// The transaction id is what the client matches the bubble
				// against the wallet history with.
				TransactionID: strconv.FormatInt(transactionID, 10),
				Comment:       comment,
			},
		},
	}
	// The message store runs its own statements; pointing it at the caller's
	// transaction makes it a savepoint of it.
	messages := *s.messages
	messages.db = tx
	messages.q = sqlcgen.New(tx)
	if _, ok := messages.boxIDs.(pgBoxIDAllocator); ok {
		messages.boxIDs = pgBoxIDAllocator{s: &messages}
	}
	return messages.sendPrivateTextWithHooks(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    req.FromUserID,
		RecipientUserID: req.ToUserID,
		// The random id is derived from the transfer itself, so a retry of
		// the same movement reuses the same id and the message is delivered
		// once.
		RandomID:               walletTransferMessageRandomID(req),
		IdempotencyFingerprint: fingerprint[:],
		Media:                  media,
		Silent:                 false,
		Date:                   req.Date,
		OriginAuthKeyID:        req.OriginAuthKeyID,
		OriginSessionID:        req.OriginSessionID,
		OriginUserID:           req.OriginUserID,
	}, privateSendTxHooks{userLanesHeld: true})
}
