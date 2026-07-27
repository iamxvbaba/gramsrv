package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// ratingTestUser inserts a user row with an explicit creation date so the account
// age signal is deterministic.
func ratingTestUser(t *testing.T, pool *pgxpool.Pool, seed int64, createdAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, created_at, updated_at)
VALUES ($1, $2, 'rating test', $3, $3)
RETURNING id`, seed, fmt.Sprintf("%d", seed), createdAt).Scan(&id); err != nil {
		t.Fatalf("insert rating test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// ratingTestCatalogRevision publishes a throwaway star gift so peer_star_gifts
// rows can satisfy their catalog revision foreign key.
func ratingTestCatalogRevision(t *testing.T, pool *pgxpool.Pool) (revisionID, giftID int64) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)
	docID := time.Now().UnixNano() & 0x7fffffffffffffff
	entry, err := NewStarGiftStore(pool).CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Stars: 50, ConvertStars: 50, Enabled: true,
		Document: domain.Document{
			ID: docID, AccessHash: docID + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob: domain.FileBlob{
			LocationKey: "doc:" + fmt.Sprint(docID), Backend: domain.MediaBackendLocalFS,
			ObjectKey: "rating-star-gift", Size: 4, SHA256: make([]byte, 32),
			MimeType: "application/x-tgsticker",
		},
		Animation: domain.StarGiftAnimation{
			JSON:   []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`),
			SHA256: make([]byte, 32), SourceFormat: domain.StarGiftAnimationTGS, Width: 512, Height: 512,
		},
		Actor: "test", CommandID: "rating-star-gift-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog revision: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT id FROM star_gift_catalog_revisions WHERE gift_id = $1 ORDER BY revision DESC LIMIT 1`,
		entry.Gift.ID).Scan(&revisionID); err != nil {
		t.Fatalf("read catalog revision id: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_catalog WHERE gift_id = $1`, entry.Gift.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_catalog_revisions WHERE gift_id = $1`, entry.Gift.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM file_blobs WHERE location_key = $1`, "doc:"+fmt.Sprint(docID))
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM documents WHERE id = $1`, docID)
	})
	return revisionID, entry.Gift.ID
}

// TestAccountRatingSaveVersionConflict covers the optimistic write: a first save
// creates the row, a stale version is refused without an error, and the next
// version wins.
func TestAccountRatingSaveVersionConflict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAccountRatingStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	userID := ratingTestUser(t, pool, 3_100_000_000+seed, time.Now().UTC().AddDate(0, 0, -30))

	if _, err := store.AccountRating(ctx, userID); !errors.Is(err, domain.ErrAccountRatingNotFound) {
		t.Fatalf("missing rating err = %v, want ErrAccountRatingNotFound", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	signals := domain.AccountRatingSignals{UserID: userID, StarsReceived: 900, MessagesSent: 10, AccountAgeDays: 30}
	computed := domain.ComputeAccountRating(signals, domain.DefaultAccountRatingWeights(), now)
	stored, changed, err := store.SaveAccountRating(ctx, computed)
	if err != nil || !changed {
		t.Fatalf("first save changed=%v err=%v", changed, err)
	}
	if stored.Version != 1 || stored.Stars != computed.Stars || stored.Level != computed.Level ||
		stored.HasNextLevel != computed.HasNextLevel || stored.NextLevelStars != computed.NextLevelStars {
		t.Fatalf("stored = %+v want %+v", stored, computed)
	}
	read, err := store.AccountRating(ctx, userID)
	if err != nil || read != stored {
		t.Fatalf("read = %+v stored = %+v err=%v", read, stored, err)
	}

	// A second writer that still believes version 0 is stale: no error, no write.
	stale := computed
	stale.Stars = 999_999
	conflicted, changed, err := store.SaveAccountRating(ctx, stale)
	if err != nil || changed {
		t.Fatalf("stale save changed=%v err=%v", changed, err)
	}
	if conflicted.Stars != stored.Stars || conflicted.Version != 1 {
		t.Fatalf("conflicted = %+v, stored row must win", conflicted)
	}

	// The recompute that carries the right version applies, including a pending
	// delta that must round-trip through the paired pending_stars/pending_date.
	next := domain.ResolveAccountRatingPending(stored,
		domain.ComputeAccountRating(domain.AccountRatingSignals{UserID: userID, StarsReceived: 40_000}, domain.DefaultAccountRatingWeights(), now),
		time.Hour, now)
	if next.PendingStars == 0 || next.PendingDate.IsZero() {
		t.Fatalf("expected a parked pending delta, got %+v", next)
	}
	applied, changed, err := store.SaveAccountRating(ctx, next)
	if err != nil || !changed {
		t.Fatalf("versioned save changed=%v err=%v", changed, err)
	}
	if applied.Version != 2 || applied.PendingStars != next.PendingStars ||
		!applied.PendingDate.Equal(next.PendingDate.UTC()) {
		t.Fatalf("applied = %+v want pending %d at %v", applied, next.PendingStars, next.PendingDate)
	}
	pending, ok := applied.PendingLevel()
	if !ok || pending.Stars <= applied.Stars {
		t.Fatalf("pending projection = %+v ok=%v", pending, ok)
	}

	batch, err := store.AccountRatingBatch(ctx, []int64{userID, userID + 1})
	if err != nil || len(batch) != 1 || batch[userID].Version != 2 {
		t.Fatalf("batch = %+v err=%v", batch, err)
	}
	list, err := store.ListAccountRatings(ctx, domain.AccountRatingFilter{UserID: userID, Limit: 10})
	if err != nil || len(list) != 1 || list[0].UserID != userID {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	stale2, err := store.StaleAccountRatings(ctx, now.Add(time.Minute).Unix(), 100)
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	if !containsInt64(stale2, userID) {
		t.Fatalf("stale ratings %v must contain %d", stale2, userID)
	}
	fresh, err := store.StaleAccountRatings(ctx, now.Add(-time.Hour).Unix(), 100)
	if err != nil {
		t.Fatalf("stale fresh: %v", err)
	}
	if containsInt64(fresh, userID) {
		t.Fatalf("rating computed at %v must not be stale before it", applied.ComputedAt)
	}
}

// TestAccountRatingAdjustmentIdempotency covers the manual ledger: a replayed
// command key records nothing new and the manual total feeds the recompute.
func TestAccountRatingAdjustmentIdempotency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAccountRatingStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	userID := ratingTestUser(t, pool, 3_200_000_000+seed, time.Now().UTC().AddDate(0, 0, -10))
	key := fmt.Sprintf("adjust-%d", seed)

	req := domain.AdjustAccountRatingRequest{
		UserID: userID, Amount: 750, Reason: "community award", Actor: "ops", CommandKey: key,
	}
	event, applied, err := store.AdjustAccountRating(ctx, req)
	if err != nil || !applied {
		t.Fatalf("adjust applied=%v err=%v", applied, err)
	}
	if event.ID == 0 || event.Kind != domain.AccountRatingEventManual || event.Amount != 750 ||
		event.CommandKey != key {
		t.Fatalf("event = %+v", event)
	}
	replay, applied, err := store.AdjustAccountRating(ctx, req)
	if err != nil || applied || replay.ID != event.ID {
		t.Fatalf("replay applied=%v event=%+v err=%v", applied, replay, err)
	}
	// A second, distinct adjustment accumulates rather than replacing.
	if _, applied, err := store.AdjustAccountRating(ctx, domain.AdjustAccountRatingRequest{
		UserID: userID, Amount: -250, Reason: "partial revoke", Actor: "ops",
		CommandKey: key + "-b",
	}); err != nil || !applied {
		t.Fatalf("second adjust applied=%v err=%v", applied, err)
	}
	events, err := store.AccountRatingEvents(ctx, userID, 10)
	if err != nil || len(events) != 2 || events[0].Amount != -250 || events[1].Amount != 750 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	if _, _, err := store.AdjustAccountRating(ctx, domain.AdjustAccountRatingRequest{
		UserID: userID, Amount: 0, CommandKey: key + "-c",
	}); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) {
		t.Fatalf("zero adjustment err = %v, want ErrAccountRatingAdjustmentInvalid", err)
	}
	signals, err := store.AccountRatingSignals(ctx, userID)
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if signals.Manual != 500 {
		t.Fatalf("manual signal = %d, want 500", signals.Manual)
	}
}

// TestAccountRatingSignalsSources pins where each contribution comes from: the
// Stars ledger sign split, saved gifts, upheld moderation cases, the peer flags,
// account age and the private-message count.
func TestAccountRatingSignalsSources(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAccountRatingStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	createdAt := time.Now().UTC().AddDate(0, 0, -45)
	userID := ratingTestUser(t, pool, 3_300_000_000+seed, createdAt)

	if _, err := pool.Exec(ctx, `
INSERT INTO stars_transactions (user_id, amount, reason, date)
VALUES ($1, 1500, 'gift', 0), ($1, 500, 'reaction', 0), ($1, -400, 'purchase', 0)`, userID); err != nil {
		t.Fatalf("seed stars: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM stars_transactions WHERE user_id = $1`, userID)
	})
	revisionID, giftID := ratingTestCatalogRevision(t, pool)
	if _, err := pool.Exec(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id, lifecycle_status, converted)
VALUES ('user', $1, 1, $2, 0, $3, 'active', false),
       ('user', $1, 2, $2, 0, $3, 'active', false),
       ('user', $1, 3, $2, 0, $3, 'converted', true)`, userID, giftID, revisionID); err != nil {
		t.Fatalf("seed gifts: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM peer_star_gifts WHERE owner_peer_type = 'user' AND owner_peer_id = $1`, userID)
	})
	// Only statuses that follow a violation decision count; 'dismissed' (which
	// covers no_violation and a granted appeal) and undecided states do not.
	now := time.Now().UTC()
	for _, status := range []string{"resolved", "action_failed", "dismissed", "open"} {
		if _, err := pool.Exec(ctx, `
INSERT INTO moderation_cases (
  target_peer_type, target_peer_id, status, severity, report_count,
  distinct_reporter_count, first_report_at, last_report_at, created_at, updated_at
) VALUES ('user', $1, $2, 1, 1, 1, $3, $3, $3, $3)`, userID, status, now); err != nil {
			t.Fatalf("seed moderation case %s: %v", status, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM moderation_cases WHERE target_peer_type = 'user' AND target_peer_id = $1`, userID)
	})
	if _, err := pool.Exec(ctx, `UPDATE users SET scam = true WHERE id = $1`, userID); err != nil {
		t.Fatalf("set scam: %v", err)
	}
	// Two private messages, each materialised as the sender's and the recipient's
	// box: the distinct count must report two, not four.
	peerID := ratingTestUser(t, pool, 3_350_000_000+seed, createdAt)
	for i := 1; i <= 2; i++ {
		var messageID int64
		if err := pool.QueryRow(ctx, `
INSERT INTO private_messages (sender_user_id, recipient_user_id, message_date, body)
VALUES ($1, $2, 0, 'hi')
RETURNING id`, userID, peerID).Scan(&messageID); err != nil {
			t.Fatalf("seed private message: %v", err)
		}
		for _, box := range []struct {
			owner    int64
			peer     int64
			outgoing bool
		}{{userID, peerID, true}, {peerID, userID, false}} {
			if _, err := pool.Exec(ctx, `
INSERT INTO message_boxes (
  owner_user_id, box_id, private_message_id, message_sender_id, peer_type, peer_id,
  from_user_id, message_date, outgoing, body
) VALUES ($1, $2, $3, $4, 'user', $5, $4, 0, $6, 'hi')`,
				box.owner, i, messageID, userID, box.peer, box.outgoing); err != nil {
				t.Fatalf("seed message box: %v", err)
			}
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM message_boxes WHERE message_sender_id = $1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM private_messages WHERE sender_user_id = $1`, userID)
	})

	signals, err := store.AccountRatingSignals(ctx, userID)
	if err != nil {
		t.Fatalf("signals: %v", err)
	}
	if signals.UserID != userID || signals.StarsReceived != 2000 || signals.StarsSpent != 400 ||
		signals.GiftsReceived != 2 || signals.ModerationCases != 2 || !signals.Scam || signals.Fake ||
		signals.MessagesSent != 2 || signals.AccountAgeDays != 45 || signals.Manual != 0 {
		t.Fatalf("signals = %+v", signals)
	}
	rating := domain.ComputeAccountRating(signals, domain.DefaultAccountRatingWeights(), now)
	if rating.PenaltyComponent == 0 || rating.StarsComponent != 2000+100 {
		t.Fatalf("computed rating = %+v", rating)
	}
	if _, err := store.AccountRatingSignals(ctx, userID+7_000_000); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("signals for unknown user err = %v, want ErrUserNotFound", err)
	}
}

// TestAccountRatingLeaderboardPaging covers the keyset walk over
// (level DESC, stars DESC, user_id).
func TestAccountRatingLeaderboardPaging(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAccountRatingStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	now := time.Now().UTC()
	scores := []int64{100, 400, 900, 1600}
	ids := make([]int64, 0, len(scores))
	for i, score := range scores {
		userID := ratingTestUser(t, pool, 3_400_000_000+seed+int64(i), now.AddDate(0, 0, -1))
		ids = append(ids, userID)
		rating := domain.ComputeAccountRating(domain.AccountRatingSignals{UserID: userID, Manual: score},
			domain.DefaultAccountRatingWeights(), now)
		if _, changed, err := store.SaveAccountRating(ctx, rating); err != nil || !changed {
			t.Fatalf("save rating %d: changed=%v err=%v", userID, changed, err)
		}
	}
	page, err := store.ListAccountRatings(ctx, domain.AccountRatingFilter{MinLevel: 2, Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	if page[0].Level < page[1].Level {
		t.Fatalf("leaderboard must be level-descending: %+v", page)
	}
	next, err := store.ListAccountRatings(ctx, domain.AccountRatingFilter{
		MinLevel: 2, BeforeID: page[len(page)-1].UserID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	for _, item := range next {
		if item.UserID == page[0].UserID || item.UserID == page[1].UserID {
			t.Fatalf("keyset page repeated user %d", item.UserID)
		}
		if item.Level > page[len(page)-1].Level {
			t.Fatalf("keyset page went backwards: %+v after %+v", item, page)
		}
	}
	if _, err := store.ListAccountRatings(ctx, domain.AccountRatingFilter{MinLevel: -1}); !errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid) {
		t.Fatalf("negative level filter must be rejected")
	}
	_ = ids
}

// TestAccountRatingUnratedAccountsSeedsTheReadModel proves the SQL behind the
// bootstrap pass: only accounts with no projection are returned, bots and deleted
// tombstones are excluded, the walk is oldest-account-first, and a user drops out of
// the candidate set the moment a projection exists.
//
// Without this query the read model can never populate itself -- StaleAccountRatings
// walks account_rating and so cannot return a user who is not in it.
func TestAccountRatingUnratedAccountsSeedsTheReadModel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewAccountRatingStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)

	oldest := ratingTestUser(t, pool, 4_100_000_000+seed, base)
	middle := ratingTestUser(t, pool, 4_200_000_000+seed, base.Add(time.Hour))
	newest := ratingTestUser(t, pool, 4_300_000_000+seed, base.Add(2*time.Hour))
	bot := ratingTestUser(t, pool, 4_400_000_000+seed, base.Add(3*time.Hour))
	deleted := ratingTestUser(t, pool, 4_500_000_000+seed, base.Add(4*time.Hour))

	if _, err := pool.Exec(ctx, `UPDATE users SET is_bot = true WHERE id = $1`, bot); err != nil {
		t.Fatalf("mark bot: %v", err)
	}
	// A deleted account is a tombstone: every profile field is already cleared, so
	// there is no rating to show and no reason to compute one.
	if _, err := pool.Exec(ctx, `
UPDATE users SET deleted_at = now(), deletion_source = 'manual', deletion_reason = 'test',
	phone = '', first_name = '', last_name = '', username = '', country_code = '', about = '',
	verified = false, support = false, premium_expires_at = NULL,
	emoji_status_document_id = 0, emoji_status_until = 0,
	emoji_status_collectible_id = NULL, emoji_status_collectible = '{}'::jsonb,
	color_set = false, color = 0, color_background_emoji_id = 0,
	profile_color_set = false, profile_color = 0, profile_color_background_emoji_id = 0,
	birthday_day = 0, birthday_month = 0, birthday_year = 0,
	personal_channel_id = 0, last_seen_at = 0, account_delete_at = NULL
WHERE id = $1`, deleted); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	// The table is shared with every other account in the test database, so assert
	// on relative order and membership rather than on an exact page.
	positions := func(t *testing.T) (map[int64]int, []int64) {
		t.Helper()
		ids, err := store.UnratedAccounts(ctx, maxAccountRatingListLimit)
		if err != nil {
			t.Fatalf("UnratedAccounts: %v", err)
		}
		index := make(map[int64]int, len(ids))
		for i, id := range ids {
			index[id] = i
		}
		return index, ids
	}

	index, ids := positions(t)
	for _, id := range []int64{oldest, middle, newest} {
		if _, ok := index[id]; !ok {
			t.Fatalf("account %d with no projection is absent from %d candidates", id, len(ids))
		}
	}
	if _, ok := index[bot]; ok {
		t.Fatalf("bot %d was offered as a rating candidate", bot)
	}
	if _, ok := index[deleted]; ok {
		t.Fatalf("deleted account %d was offered as a rating candidate", deleted)
	}
	// The service accounts are infrastructure. The platform account in particular is
	// NOT flagged is_bot, so excluding bots alone would still have seeded it -- which
	// is exactly how it got a rating.
	for _, serviceID := range domain.SystemUserIDs() {
		if _, ok := index[serviceID]; ok {
			t.Fatalf("service account %d was offered as a rating candidate", serviceID)
		}
	}
	if !(index[oldest] < index[middle] && index[middle] < index[newest]) {
		t.Fatalf("candidate order = oldest %d, middle %d, newest %d; want oldest first",
			index[oldest], index[middle], index[newest])
	}

	// Seeding one account removes it from the candidate set, so the pass converges
	// instead of offering the same user every cycle.
	if _, changed, err := store.SaveAccountRating(ctx, domain.AccountRating{
		UserID: middle, Level: 1, Stars: 150,
		CurrentLevelStars: domain.AccountRatingLevelThreshold(1),
		ComputedAt:        time.Now().UTC(), Version: 1,
	}); err != nil || !changed {
		t.Fatalf("seed projection = %v changed=%v", err, changed)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM account_rating WHERE user_id = $1`, middle)
	})
	index, _ = positions(t)
	if _, ok := index[middle]; ok {
		t.Fatalf("account %d is still a candidate after being seeded", middle)
	}
	if _, ok := index[oldest]; !ok {
		t.Fatalf("seeding %d removed unrelated candidate %d", middle, oldest)
	}

	// The limit is honoured, so one cycle can never walk the whole users table.
	capped, err := store.UnratedAccounts(ctx, 2)
	if err != nil {
		t.Fatalf("UnratedAccounts with a limit: %v", err)
	}
	if len(capped) != 2 {
		t.Fatalf("limited candidates = %d, want 2", len(capped))
	}
}
