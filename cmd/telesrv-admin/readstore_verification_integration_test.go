package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// The verification review queue is read with hand-written SQL, so the only thing
// that can prove the column names, the array and nullable-timestamp scans, and the
// ownership predicates are right is running them against the real schema. Gated on
// TELESRV_TEST_POSTGRES_DSN, like every other integration test in the repo.

func verificationReadStore(t *testing.T) (*readStore, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TELESRV_TEST_POSTGRES_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(parsed.ConnConfig.Database), "test") {
		t.Fatalf("TELESRV_TEST_POSTGRES_DSN must name a dedicated test database, got %q", parsed.ConnConfig.Database)
	}
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newReadStore(pool), pool
}

// verificationFixture seeds one applicant who owns a bot and administers a public
// channel, plus one unrelated channel nobody controls, and files an application
// against each. It returns the applicant id and the three application ids.
type verificationFixture struct {
	applicant    int64
	bot          int64
	channel      int64
	foreign      int64
	botApp       int64
	channelApp   int64
	rejectedApp  int64
	searchSuffix string
}

func seedVerificationFixture(t *testing.T, pool *pgxpool.Pool) verificationFixture {
	t.Helper()
	ctx := context.Background()
	var fx verificationFixture
	now := time.Now().UTC().Truncate(time.Microsecond)
	// Usernames and channel ids are globally unique, so every run needs its own
	// suffix; tests in this package may run against a database another run left
	// rows in.
	unique := now.UnixNano() & 0x7fffffff
	suffix := strconv.FormatInt(unique, 10)
	// channels.id carries no sequence: the caller assigns it.
	nextChannelID := 1_000_000_000 + unique%100_000_000

	insertUser := func(name, username string, isBot, verified bool) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, last_name, username, is_bot, verified)
VALUES ($1, $2, $3, 'Reviewer', $4, $5, $6)
RETURNING id`, unique, "70"+strconv.FormatInt(unique, 10), name, username, isBot, verified).Scan(&id); err != nil {
			t.Fatalf("insert user %s: %v", name, err)
		}
		unique++
		return id
	}
	insertChannel := func(title, username string, verified bool) int64 {
		id := nextChannelID
		nextChannelID++
		if _, err := pool.Exec(ctx, `
INSERT INTO channels (
	id, access_hash, creator_user_id, title, username, broadcast, megagroup,
	participants_count, admins_count, top_message_id, pts, date, verified
)
VALUES ($1, $2, $3, $4, $5, true, false, 1, 1, 1, 1, $6, $7)`,
			id, unique, fx.applicant, title, username, int32(now.Unix()), verified); err != nil {
			t.Fatalf("insert channel %s: %v", title, err)
		}
		unique++
		return id
	}

	fx.applicant = insertUser("Applicant", "applicant"+suffix, false, false)
	fx.bot = insertUser("Fixturebot", "fixturebot"+suffix, true, false)
	if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret) VALUES ($1, $2, 'secret')`,
		fx.bot, fx.applicant); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	fx.channel = insertChannel("Fixture News", "fixturenews"+suffix, true)
	fx.foreign = insertChannel("Foreign Channel", "foreignchannel"+suffix, false)
	if _, err := pool.Exec(ctx, `
INSERT INTO user_channel_member_index (user_id, channel_id, status, role, broadcast, public_username)
VALUES ($1, $2, 'active', 'creator', true, true)`, fx.applicant, fx.channel); err != nil {
		t.Fatalf("insert member index: %v", err)
	}

	insertApplication := func(
		targetType string, targetID int64, status, reviewer, reason string,
		reviewed bool,
	) int64 {
		var reviewedAt *time.Time
		if reviewed {
			reviewedAt = &now
		}
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO verification_applications (
	applicant_user_id, target_type, target_id, target_title, target_username,
	category, description, official_website, social_links, press_links, additional_note,
	status, reviewer_admin_id, decision_reason, internal_note, correlation_id,
	created_at, updated_at, submitted_at, reviewed_at, version
) VALUES (
	$1, $2, $3, $4, $5,
	'media', 'a description long enough to satisfy the domain bar for submission',
	'https://example.test', $6, $7, 'note',
	$8, $9, $10, 'operator only', $11,
	$12, $12, $12, $13, 3
) RETURNING id`,
			fx.applicant, targetType, targetID, "Snapshot "+targetType, "snapshot"+targetType+suffix,
			[]string{"https://social.example.test/a"},
			[]string{"https://press.example.test/a", "https://press.example.test/b"},
			status, reviewer, reason, "corr-"+status,
			now, reviewedAt,
		).Scan(&id); err != nil {
			t.Fatalf("insert %s application: %v", status, err)
		}
		return id
	}

	fx.channelApp = insertApplication("channel", fx.channel, "submitted", "", "", false)
	fx.botApp = insertApplication("bot", fx.bot, "in_review", "alice", "", false)
	// Filed as a user target against an id the applicant is not, so the ownership
	// predicate has a negative case to answer.
	fx.rejectedApp = insertApplication("user", fx.foreign, "rejected", "bob", "press links are self-published", true)
	fx.searchSuffix = suffix

	if _, err := pool.Exec(ctx, `
INSERT INTO verification_application_events
	(application_id, kind, from_status, to_status, actor, reason, note, correlation_id, created_at)
VALUES
	($1, 'submitted', 'draft', 'submitted', '', '', '', 'corr-submitted', $2),
	($1, 'claimed', 'submitted', 'in_review', 'alice', '', 'handover note', 'corr-claimed', $2)`,
		fx.channelApp, now); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	t.Cleanup(func() {
		ids := []int64{fx.channelApp, fx.botApp, fx.rejectedApp}
		_, _ = pool.Exec(ctx, "DELETE FROM verification_notification_outbox WHERE application_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM verification_application_events WHERE application_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM verification_applications WHERE id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_channel_member_index WHERE user_id = $1", fx.applicant)
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", []int64{fx.channel, fx.foreign})
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", fx.bot)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{fx.applicant, fx.bot})
	})
	return fx
}

func TestVerificationReadStoreQueue(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedVerificationFixture(t, pool)
	ctx := context.Background()

	rows, hasMore, err := store.ListVerificationApplications(ctx, "", "", "", "", 0, 200)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[int64]VerificationApplicationRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	channelRow, ok := byID[fx.channelApp]
	if !ok {
		t.Fatalf("submitted application missing from the queue (hasMore=%v)", hasMore)
	}
	// The applicant is resolved through the join, the arrays survive the scan, and
	// the live target badge is read from the peer rather than the snapshot.
	if channelRow.ApplicantUserID != fx.applicant || channelRow.ApplicantUsername != "applicant"+fx.searchSuffix {
		t.Fatalf("applicant projection = %+v", channelRow)
	}
	if !strings.Contains(channelRow.ApplicantName, "Applicant") {
		t.Fatalf("applicant name = %q", channelRow.ApplicantName)
	}
	if len(channelRow.SocialLinks) != 1 || len(channelRow.PressLinks) != 2 {
		t.Fatalf("link arrays = %+v / %+v", channelRow.SocialLinks, channelRow.PressLinks)
	}
	if !channelRow.TargetVerified {
		t.Fatal("target_verified did not follow the live channel record")
	}
	if channelRow.InternalNote != "operator only" || channelRow.CorrelationID != "corr-submitted" {
		t.Fatalf("operator fields = %+v", channelRow)
	}
	if channelRow.SubmittedAt.IsZero() || !channelRow.ReviewedAt.IsZero() {
		t.Fatalf("timestamps submitted=%v reviewed=%v, want an undecided application", channelRow.SubmittedAt, channelRow.ReviewedAt)
	}
	if decided := byID[fx.rejectedApp]; decided.ReviewedAt.IsZero() || decided.DecisionReason == "" {
		t.Fatalf("decided application = %+v", decided)
	}

	// Filters.
	filtered, _, err := store.ListVerificationApplications(ctx, "in_review", "", "alice", "", 0, 50)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	for _, row := range filtered {
		if row.Status != "in_review" || row.ReviewerAdminID != "alice" {
			t.Fatalf("status/reviewer filter leaked %+v", row)
		}
	}
	typed, _, err := store.ListVerificationApplications(ctx, "", "bot", "", "", 0, 50)
	if err != nil {
		t.Fatalf("target_type list: %v", err)
	}
	for _, row := range typed {
		if row.TargetType != "bot" {
			t.Fatalf("target_type filter leaked %+v", row)
		}
	}
	// q matches the application id, the target id and a username prefix.
	for _, query := range []string{
		strconv.FormatInt(fx.channelApp, 10),
		strconv.FormatInt(fx.channel, 10),
		"snapshotchannel" + fx.searchSuffix,
		"@applicant" + fx.searchSuffix,
	} {
		found, _, err := store.ListVerificationApplications(ctx, "", "", "", query, 0, 50)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(found) == 0 {
			t.Fatalf("search %q returned nothing", query)
		}
	}
	// The keyset cursor excludes the row it points at.
	after, _, err := store.ListVerificationApplications(ctx, "", "", "", "", fx.channelApp, 50)
	if err != nil {
		t.Fatalf("keyset list: %v", err)
	}
	for _, row := range after {
		if row.ID >= fx.channelApp {
			t.Fatalf("keyset page leaked id %d at or after the cursor %d", row.ID, fx.channelApp)
		}
	}
	// The page bound is honoured and reports more.
	page, more, err := store.ListVerificationApplications(ctx, "", "", "", "", 0, 1)
	if err != nil {
		t.Fatalf("bounded list: %v", err)
	}
	if len(page) != 1 || !more {
		t.Fatalf("bounded page len=%d hasMore=%v", len(page), more)
	}
}

func TestVerificationReadStoreDetailAndOwnership(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedVerificationFixture(t, pool)
	ctx := context.Background()

	detail, err := store.VerificationApplicationDetail(ctx, fx.channelApp)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Application.ID != fx.channelApp || len(detail.Events) != 2 {
		t.Fatalf("detail = %+v events=%d", detail.Application, len(detail.Events))
	}
	// Newest first, and the operator-only note travels with the event.
	if detail.Events[0].Kind != string(domain.VerificationEventClaimed) ||
		detail.Events[0].Note != "handover note" || detail.Events[0].Actor != "alice" {
		t.Fatalf("events = %+v", detail.Events)
	}
	if !detail.ApplicantControlsTarget {
		t.Fatal("channel creator was not recognised as controlling the target")
	}

	// A bot the applicant owns.
	botDetail, err := store.VerificationApplicationDetail(ctx, fx.botApp)
	if err != nil {
		t.Fatalf("bot detail: %v", err)
	}
	if !botDetail.ApplicantControlsTarget {
		t.Fatal("bot owner was not recognised as controlling the target")
	}

	// A target the applicant has nothing to do with: the application was filed as
	// a user target against a foreign channel id, so identity does not match.
	foreignDetail, err := store.VerificationApplicationDetail(ctx, fx.rejectedApp)
	if err != nil {
		t.Fatalf("foreign detail: %v", err)
	}
	if foreignDetail.ApplicantControlsTarget {
		t.Fatal("an unrelated target was reported as controlled")
	}

	if _, err := store.VerificationApplicationDetail(ctx, 0); err == nil {
		t.Fatal("detail of a missing application succeeded")
	}

	// A user target that is the applicant themself is controlled by definition.
	controls, err := store.applicantControlsVerificationTarget(ctx, fx.applicant, "user", fx.applicant)
	if err != nil || !controls {
		t.Fatalf("self target controls=%v err=%v", controls, err)
	}
	// BotFather is owned by nobody, whatever the bots table says.
	controls, err = store.applicantControlsVerificationTarget(ctx, fx.applicant, "bot", domain.BotFatherUserID)
	if err != nil || controls {
		t.Fatalf("botfather controls=%v err=%v", controls, err)
	}
	// An unmodelled target type is never controlled.
	controls, err = store.applicantControlsVerificationTarget(ctx, fx.applicant, "group", fx.channel)
	if err != nil || controls {
		t.Fatalf("unmodelled target controls=%v err=%v", controls, err)
	}
}

func TestVerificationReadStoreCounts(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedVerificationFixture(t, pool)
	ctx := context.Background()

	counts, err := store.VerificationStatusCounts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	// Every modelled status is present, so the panel never tells "none" from
	// "missing".
	for _, status := range []string{"draft", "submitted", "in_review", "approved", "rejected", "cancelled"} {
		if _, ok := counts[status]; !ok {
			t.Fatalf("counts %+v missing %q", counts, status)
		}
	}
	if counts["submitted"] == "0" || counts["in_review"] == "0" || counts["rejected"] == "0" {
		t.Fatalf("counts %+v did not see the seeded applications (%d/%d/%d)",
			counts, fx.channelApp, fx.botApp, fx.rejectedApp)
	}
}
