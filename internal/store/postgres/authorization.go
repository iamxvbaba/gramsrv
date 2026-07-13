package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AuthorizationStore 用 PostgreSQL 实现 store.AuthorizationStore。
type AuthorizationStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewAuthorizationStore 基于 pgx 连接池（或事务）创建 AuthorizationStore。
func NewAuthorizationStore(db sqlcgen.DBTX) *AuthorizationStore {
	return &AuthorizationStore{db: db, q: sqlcgen.New(db)}
}

func (s *AuthorizationStore) Bind(ctx context.Context, a domain.Authorization) error {
	if a.Hash == 0 {
		a.Hash = authorizationHash(a.AuthKeyID)
	}
	bind := func(db sqlcgen.DBTX) error {
		return bindAuthorization(ctx, db, a)
	}
	var err error
	if tx, ok := s.db.(pgx.Tx); ok {
		err = bind(tx)
	} else {
		err = withTx(ctx, s.db, "bind authorization", func(tx pgx.Tx) error {
			return bind(tx)
		})
	}
	if err != nil {
		return fmt.Errorf("upsert authorization: %w", err)
	}
	return nil
}

// bindAuthorization 把 auth_key→user 绑定和设备 update baseline 作为同一个状态边界提交。
//
// 锁顺序固定为：auth_keys 母行 → 目标 user_update_watermarks →
// user_update_retention → 目标 update_states。前两个 user 锁与
// pruneConfirmedUserPrefixTx 一致，使新授权的 observed baseline 和 retained floor 不会
// 交叉提交成静默空洞。母行锁又能在首次 authorization 尚不存在时串行化同一
// raw auth key 的并发登录/换号。
func bindAuthorization(ctx context.Context, db sqlcgen.DBTX, a domain.Authorization) error {
	keyID := authKeyIDToInt64(a.AuthKeyID)
	var (
		lockedKeyID int64
		expiresAt   int
	)
	if err := db.QueryRow(ctx, `
SELECT auth_key_id, expires_at
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE`, keyID).Scan(&lockedKeyID, &expiresAt); err != nil {
		return fmt.Errorf("lock auth key for authorization: %w", err)
	}
	if expiresAt != 0 {
		return store.ErrAuthKeyNotPermanent
	}

	if _, err := db.Exec(ctx, `
INSERT INTO user_update_watermarks (user_id, contiguous_pts)
VALUES ($1, 0)
ON CONFLICT (user_id) DO NOTHING`, a.UserID); err != nil {
		return fmt.Errorf("ensure authorization user update watermark: %w", err)
	}
	var currentPts int
	if err := db.QueryRow(ctx, `
SELECT contiguous_pts
FROM user_update_watermarks
WHERE user_id = $1
FOR UPDATE`, a.UserID).Scan(&currentPts); err != nil {
		return fmt.Errorf("lock authorization user update watermark: %w", err)
	}
	if _, err := db.Exec(ctx, `
INSERT INTO user_update_retention (user_id)
VALUES ($1)
ON CONFLICT (user_id) DO NOTHING`, a.UserID); err != nil {
		return fmt.Errorf("ensure authorization user update retention: %w", err)
	}
	var retainedFloor int
	if err := db.QueryRow(ctx, `
SELECT retained_through_pts
FROM user_update_retention
WHERE user_id = $1
FOR UPDATE`, a.UserID).Scan(&retainedFloor); err != nil {
		return fmt.Errorf("lock authorization user update retention: %w", err)
	}
	if retainedFloor > currentPts {
		return fmt.Errorf(
			"authorization update baseline invariant violation: user %d retained floor %d exceeds contiguous watermark %d",
			a.UserID, retainedFloor, currentPts,
		)
	}

	// 每次 Bind 都是一次显式登录 baseline：delivered pts 推进到已锁定的账号连续水位；
	// observed 只推进到已删除的 retained floor，不把 live tail 伪装成客户端确认。
	// 历史遗留的 state 若超出账号 contiguous watermark，必须 fail-fast；不得用
	// GREATEST 把非法 future cursor 保留下来。WHERE 也封住“预检后并发插入”的竞态。
	tag, err := db.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, qts, date, seq, observed_pts)
VALUES ($1, $2, $3, 0, EXTRACT(EPOCH FROM now())::int, 0, $4)
ON CONFLICT (auth_key_id, user_id) DO UPDATE SET
  pts = GREATEST(update_states.pts, EXCLUDED.pts),
  qts = GREATEST(update_states.qts, EXCLUDED.qts),
  date = GREATEST(update_states.date, EXCLUDED.date),
  seq = GREATEST(update_states.seq, EXCLUDED.seq),
  observed_pts = GREATEST(update_states.observed_pts, EXCLUDED.observed_pts),
  updated_at = now()
WHERE update_states.pts >= 0
  AND update_states.pts <= $3
  AND update_states.observed_pts <= $3`, keyID, a.UserID, currentPts, retainedFloor)
	if err != nil {
		return fmt.Errorf("upsert authorization update baseline: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf(
			"authorization update baseline invariant violation: auth key %x user %d has pts or observed_pts outside contiguous watermark %d",
			a.AuthKeyID, a.UserID, currentPts,
		)
	}

	if _, err := db.Exec(ctx, `
DELETE FROM update_states
WHERE auth_key_id = $1
  AND user_id <> $2`, keyID, a.UserID); err != nil {
		return fmt.Errorf("delete stale cross-user update states: %w", err)
	}

	if _, err := db.Exec(ctx, `
INSERT INTO authorizations (auth_key_id, user_id, hash, layer, device_model, platform, system_version, api_id, app_version, ip, password_pending)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (auth_key_id) DO UPDATE SET
  user_id = EXCLUDED.user_id,
  hash = EXCLUDED.hash,
  layer = EXCLUDED.layer,
  device_model = EXCLUDED.device_model,
  platform = EXCLUDED.platform,
  system_version = EXCLUDED.system_version,
  api_id = EXCLUDED.api_id,
  app_version = EXCLUDED.app_version,
  ip = EXCLUDED.ip,
  password_pending = EXCLUDED.password_pending,
  active_at = now()`,
		keyID, a.UserID, a.Hash, int32(a.Layer), a.DeviceModel, a.Platform, a.SystemVersion, int32(a.APIID), a.AppVersion, a.IP, a.PasswordPending,
	); err != nil {
		return fmt.Errorf("write authorization: %w", err)
	}
	return nil
}

func (s *AuthorizationStore) ByAuthKey(ctx context.Context, id [8]byte) (domain.Authorization, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT user_id, hash, layer, device_model, platform, system_version, api_id, app_version, ip, password_pending, created_at, active_at
FROM authorizations WHERE auth_key_id = $1`, authKeyIDToInt64(id))
	a := domain.Authorization{AuthKeyID: id}
	if err := row.Scan(
		&a.UserID, &a.Hash, &a.Layer, &a.DeviceModel, &a.Platform, &a.SystemVersion,
		&a.APIID, &a.AppVersion, &a.IP, &a.PasswordPending, &a.CreatedAt, &a.ActiveAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Authorization{}, false, nil
		}
		return domain.Authorization{}, false, fmt.Errorf("get authorization: %w", err)
	}
	return a, true, nil
}

func (s *AuthorizationStore) UpdateLayer(ctx context.Context, id [8]byte, layer int) error {
	if layer <= 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
UPDATE authorizations SET layer = $2, active_at = now() WHERE auth_key_id = $1`,
		authKeyIDToInt64(id), int32(layer)); err != nil {
		return fmt.Errorf("update authorization layer: %w", err)
	}
	return nil
}

func (s *AuthorizationStore) UpdateClientInfo(ctx context.Context, id [8]byte, info domain.AuthKeyClientInfo) error {
	if _, err := s.db.Exec(ctx, `
UPDATE authorizations SET
  layer = CASE WHEN $2 > 0 THEN $2 ELSE layer END,
  device_model = CASE WHEN $3 <> '' THEN $3 ELSE device_model END,
  platform = CASE WHEN $4 <> '' THEN $4 ELSE platform END,
  system_version = CASE WHEN $5 <> '' THEN $5 ELSE system_version END,
  api_id = CASE WHEN $6 <> 0 THEN $6 ELSE api_id END,
  app_version = CASE WHEN $7 <> '' THEN $7 ELSE app_version END,
  active_at = now()
WHERE auth_key_id = $1`,
		authKeyIDToInt64(id), int32(info.Layer), info.DeviceModel, info.Platform,
		info.SystemVersion, int32(info.APIID), info.AppVersion,
	); err != nil {
		return fmt.Errorf("update authorization client info: %w", err)
	}
	return nil
}

// MarkPasswordPassed 在两步验证通过后清除 password_pending，使 auth_key 转为完全授权。
func (s *AuthorizationStore) MarkPasswordPassed(ctx context.Context, id [8]byte) error {
	if _, err := s.db.Exec(ctx, `
UPDATE authorizations SET password_pending = false, active_at = now() WHERE auth_key_id = $1`, authKeyIDToInt64(id)); err != nil {
		return fmt.Errorf("mark authorization password passed: %w", err)
	}
	return nil
}

func (s *AuthorizationStore) ListByUser(ctx context.Context, userID int64) ([]domain.Authorization, error) {
	rows, err := s.q.ListAuthorizationsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list authorizations by user: %w", err)
	}
	out := make([]domain.Authorization, 0, len(rows))
	for _, row := range rows {
		out = append(out, authorizationFromRow(row))
	}
	return out, nil
}

func (s *AuthorizationStore) Delete(ctx context.Context, id [8]byte) error {
	if err := s.q.DeleteAuthorization(ctx, authKeyIDToInt64(id)); err != nil {
		return fmt.Errorf("delete authorization: %w", err)
	}
	return nil
}

func (s *AuthorizationStore) DeleteByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	row := s.db.QueryRow(ctx, `
DELETE FROM authorizations
WHERE user_id = $1 AND hash = $2
RETURNING auth_key_id, user_id, hash, layer, device_model, platform, system_version, api_id, app_version, ip, created_at, active_at`, userID, hash)
	var a domain.Authorization
	var authKeyID int64
	if err := row.Scan(
		&authKeyID, &a.UserID, &a.Hash, &a.Layer, &a.DeviceModel, &a.Platform, &a.SystemVersion,
		&a.APIID, &a.AppVersion, &a.IP, &a.CreatedAt, &a.ActiveAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Authorization{}, false, nil
		}
		return domain.Authorization{}, false, fmt.Errorf("delete authorization by hash: %w", err)
	}
	a.AuthKeyID = authKeyIDFromInt64(authKeyID)
	return a, true, nil
}

// RevokeByHash 删除协议 auth_key 作为远程踢设备的持久化事实入口。
// authorizations 通过 FK cascade 删除；update_states 没有 auth_keys FK，必须显式清理；
// 关联 temp auth key 也显式删除，避免 raw temp key 重连。
func (s *AuthorizationStore) RevokeByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var (
			a     domain.Authorization
			found bool
		)
		err := s.withRevocationTx(ctx, "revoke authorization by hash", func(tx pgx.Tx) error {
			var err error
			a, found, err = revokeByHashTx(ctx, tx, userID, hash)
			return err
		})
		if err == nil {
			return a, found, nil
		}
		if !isPermAuthKeyDeleteRace(err) {
			return domain.Authorization{}, false, err
		}
		if _, inTx := s.db.(pgx.Tx); inTx {
			return domain.Authorization{}, false, err
		}
	}
	return domain.Authorization{}, false, fmt.Errorf("revoke authorization by hash: permanent-key binding changed during all retries")
}

func (s *AuthorizationStore) withRevocationTx(ctx context.Context, op string, fn func(pgx.Tx) error) error {
	if tx, ok := s.db.(pgx.Tx); ok {
		return fn(tx)
	}
	return withTx(ctx, s.db, op, fn)
}

// revokeByHashTx deliberately uses separate READ COMMITTED statements. The first
// lookup is only a candidate. Bind locks auth_keys before changing authorization
// ownership, so revocation must lock the same parent row and then re-read the
// owner/hash from a fresh statement snapshot. Otherwise an A->B re-login that
// commits while revoke waits can be deleted using A's stale target snapshot.
func revokeByHashTx(ctx context.Context, tx pgx.Tx, userID, hash int64) (domain.Authorization, bool, error) {
	var candidate int64
	if err := tx.QueryRow(ctx, `
SELECT auth_key_id
FROM authorizations
WHERE user_id = $1 AND hash = $2`, userID, hash).Scan(&candidate); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Authorization{}, false, nil
		}
		return domain.Authorization{}, false, fmt.Errorf("select revoke candidate by hash: %w", err)
	}

	var locked int64
	if err := tx.QueryRow(ctx, `
SELECT auth_key_id
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE`, candidate).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Authorization{}, false, nil
		}
		return domain.Authorization{}, false, fmt.Errorf("lock revoke auth key by hash: %w", err)
	}

	a, found, err := scanRevokedAuthorization(tx.QueryRow(ctx, `
SELECT auth_key_id, user_id, hash, layer, device_model, platform, system_version,
       api_id, app_version, ip, password_pending, created_at, active_at
FROM authorizations
WHERE auth_key_id = $1 AND user_id = $2 AND hash = $3
FOR UPDATE`, candidate, userID, hash))
	if err != nil {
		return domain.Authorization{}, false, fmt.Errorf("revalidate revoke authorization by hash: %w", err)
	}
	if !found {
		return domain.Authorization{}, false, nil
	}
	if err := deleteRevocationTargetsTx(ctx, tx, []int64{candidate}); err != nil {
		return domain.Authorization{}, false, err
	}
	return a, true, nil
}

func (s *AuthorizationStore) DeleteByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	rows, err := s.db.Query(ctx, `
DELETE FROM authorizations
WHERE user_id = $1 AND auth_key_id <> $2
RETURNING auth_key_id, user_id, hash, layer, device_model, platform, system_version, api_id, app_version, ip, created_at, active_at`, userID, authKeyIDToInt64(keepAuthKeyID))
	if err != nil {
		return nil, fmt.Errorf("delete authorizations by user: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Authorization, 0)
	for rows.Next() {
		var a domain.Authorization
		var authKeyID int64
		if err := rows.Scan(
			&authKeyID, &a.UserID, &a.Hash, &a.Layer, &a.DeviceModel, &a.Platform, &a.SystemVersion,
			&a.APIID, &a.AppVersion, &a.IP, &a.CreatedAt, &a.ActiveAt,
		); err != nil {
			return nil, fmt.Errorf("scan deleted authorization: %w", err)
		}
		a.AuthKeyID = authKeyIDFromInt64(authKeyID)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deleted authorizations: %w", err)
	}
	return out, nil
}

// RevokeByUserExcept 批量删除协议 auth_key，保留 keepAuthKeyID 对应的当前设备。
func (s *AuthorizationStore) RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var out []domain.Authorization
		err := s.withRevocationTx(ctx, "revoke authorizations by user", func(tx pgx.Tx) error {
			var err error
			out, err = revokeByUserExceptTx(ctx, tx, userID, authKeyIDToInt64(keepAuthKeyID))
			return err
		})
		if err == nil {
			return out, nil
		}
		if !isPermAuthKeyDeleteRace(err) {
			return nil, err
		}
		if _, inTx := s.db.(pgx.Tx); inTx {
			return nil, err
		}
	}
	return nil, fmt.Errorf("revoke authorizations by user: permanent-key binding changed during all retries")
}

func revokeByUserExceptTx(ctx context.Context, tx pgx.Tx, userID, keepAuthKeyID int64) ([]domain.Authorization, error) {
	candidateRows, err := tx.Query(ctx, `
SELECT auth_key_id
FROM authorizations
WHERE user_id = $1 AND auth_key_id <> $2
ORDER BY auth_key_id`, userID, keepAuthKeyID)
	if err != nil {
		return nil, fmt.Errorf("select revoke candidates by user: %w", err)
	}
	candidates := make([]int64, 0)
	for candidateRows.Next() {
		var id int64
		if err := candidateRows.Scan(&id); err != nil {
			candidateRows.Close()
			return nil, fmt.Errorf("scan revoke candidate by user: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := candidateRows.Err(); err != nil {
		candidateRows.Close()
		return nil, fmt.Errorf("iterate revoke candidates by user: %w", err)
	}
	candidateRows.Close()
	if len(candidates) == 0 {
		return []domain.Authorization{}, nil
	}

	// Stable parent-row lock order matches every concurrent batch revocation and
	// serializes each candidate with Bind's auth_keys-first ownership change.
	lockRows, err := tx.Query(ctx, `
SELECT auth_key_id
FROM auth_keys
WHERE auth_key_id = ANY($1::bigint[])
ORDER BY auth_key_id
FOR UPDATE`, candidates)
	if err != nil {
		return nil, fmt.Errorf("lock revoke auth keys by user: %w", err)
	}
	for lockRows.Next() {
		var ignored int64
		if err := lockRows.Scan(&ignored); err != nil {
			lockRows.Close()
			return nil, fmt.Errorf("scan locked revoke auth key: %w", err)
		}
	}
	if err := lockRows.Err(); err != nil {
		lockRows.Close()
		return nil, fmt.Errorf("iterate locked revoke auth keys: %w", err)
	}
	lockRows.Close()

	// This is intentionally a new statement snapshot after all parent locks.
	// Keys that changed owner while waiting are omitted and must remain intact.
	rows, err := tx.Query(ctx, `
SELECT auth_key_id, user_id, hash, layer, device_model, platform, system_version,
       api_id, app_version, ip, password_pending, created_at, active_at
FROM authorizations
WHERE user_id = $1
  AND auth_key_id <> $2
  AND auth_key_id = ANY($3::bigint[])
ORDER BY created_at, auth_key_id
FOR UPDATE`, userID, keepAuthKeyID, candidates)
	if err != nil {
		return nil, fmt.Errorf("revalidate revoke authorizations by user: %w", err)
	}
	out := make([]domain.Authorization, 0)
	for rows.Next() {
		a, err := scanRevokedAuthorizationRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate revoked authorizations: %w", err)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	targets := make([]int64, len(out))
	for i := range out {
		targets[i] = authKeyIDToInt64(out[i].AuthKeyID)
	}
	if err := deleteRevocationTargetsTx(ctx, tx, targets); err != nil {
		return nil, err
	}
	return out, nil
}

func deleteRevocationTargetsTx(ctx context.Context, tx pgx.Tx, authKeyIDs []int64) error {
	if len(authKeyIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM auth_keys
WHERE auth_key_id IN (
	SELECT temp_auth_key_id
	FROM temp_auth_key_bindings
	WHERE perm_auth_key_id = ANY($1::bigint[])
)`, authKeyIDs); err != nil {
		return fmt.Errorf("delete revoked temporary auth keys: %w", err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM update_states
WHERE auth_key_id = ANY($1::bigint[])`, authKeyIDs); err != nil {
		return fmt.Errorf("delete revoked update states: %w", err)
	}
	tag, err := tx.Exec(ctx, `
DELETE FROM auth_keys
WHERE auth_key_id = ANY($1::bigint[])`, authKeyIDs)
	if err != nil {
		return fmt.Errorf("delete revoked permanent auth keys: %w", err)
	}
	if tag.RowsAffected() != int64(len(authKeyIDs)) {
		return fmt.Errorf("delete revoked permanent auth keys: deleted %d of %d locked targets", tag.RowsAffected(), len(authKeyIDs))
	}
	return nil
}

func authorizationFromRow(row sqlcgen.Authorization) domain.Authorization {
	return domain.Authorization{
		AuthKeyID:     authKeyIDFromInt64(row.AuthKeyID),
		UserID:        row.UserID,
		Hash:          row.Hash,
		Layer:         int(row.Layer),
		DeviceModel:   row.DeviceModel,
		Platform:      row.Platform,
		SystemVersion: row.SystemVersion,
		APIID:         int(row.ApiID),
		AppVersion:    row.AppVersion,
		IP:            row.Ip,
		CreatedAt:     row.CreatedAt.Time,
		ActiveAt:      row.ActiveAt.Time,
	}
}

type authorizationScanner interface {
	Scan(dest ...any) error
}

func scanRevokedAuthorization(row authorizationScanner) (domain.Authorization, bool, error) {
	a, err := scanRevokedAuthorizationRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Authorization{}, false, nil
		}
		return domain.Authorization{}, false, err
	}
	return a, true, nil
}

func scanRevokedAuthorizationRow(row authorizationScanner) (domain.Authorization, error) {
	var a domain.Authorization
	var authKeyID int64
	if err := row.Scan(
		&authKeyID, &a.UserID, &a.Hash, &a.Layer, &a.DeviceModel, &a.Platform, &a.SystemVersion,
		&a.APIID, &a.AppVersion, &a.IP, &a.PasswordPending, &a.CreatedAt, &a.ActiveAt,
	); err != nil {
		return domain.Authorization{}, err
	}
	a.AuthKeyID = authKeyIDFromInt64(authKeyID)
	return a, nil
}

func authorizationHash(authKeyID [8]byte) int64 {
	sum := sha256.Sum256(authKeyID[:])
	hash := int64(binary.LittleEndian.Uint64(sum[:8]))
	if hash == 0 {
		return 1
	}
	return hash
}
