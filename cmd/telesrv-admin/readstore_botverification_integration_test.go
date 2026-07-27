package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The third-party verification tables are read with hand-written SQL, so the only
// thing that can prove the column names, the CASE-per-peer-namespace projections
// and the `AND editable` username joins are right is running them against the real
// schema. Gated on TELESRV_TEST_POSTGRES_DSN, like every other integration test in
// the repo, and reusing verificationReadStore for the pool and the migration.

// botVerificationFixture seeds two verifier bots (one enabled, one switched off),
// a shared and a reserved icon, marks on a user peer and a channel peer, and
// applications in three states.
type botVerificationFixture struct {
	verifierBot  int64
	disabledBot  int64
	applicant    int64
	userPeer     int64
	channel      int64
	sharedIcon   int64
	reservedIcon int64
	sharedDoc    int64
	reservedDoc  int64
	userMark     int64
	channelMark  int64
	pendingReq   int64
	approvedReq  int64
	rejectedReq  int64
	suffix       string
}

func seedBotVerificationFixture(t *testing.T, pool *pgxpool.Pool) botVerificationFixture {
	t.Helper()
	ctx := context.Background()
	var fx botVerificationFixture
	now := time.Now().UTC().Truncate(time.Microsecond)
	// Usernames, channel ids and icon document ids are globally unique, so every run
	// needs its own suffix: this database may still hold rows another run left.
	unique := now.UnixNano() & 0x7fffffff
	suffix := strconv.FormatInt(unique, 10)
	fx.suffix = suffix
	nextChannelID := 1_200_000_000 + unique%100_000_000
	fx.sharedDoc = 7_000_000_000 + unique%1_000_000
	fx.reservedDoc = fx.sharedDoc + 1

	insertUser := func(first, username string, isBot bool) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, last_name, username, is_bot)
VALUES ($1, $2, $3, 'Fixture', $4, $5)
RETURNING id`, unique, "71"+strconv.FormatInt(unique, 10), first, username, isBot).Scan(&id); err != nil {
			t.Fatalf("insert user %s: %v", first, err)
		}
		unique++
		return id
	}

	fx.verifierBot = insertUser("Verifierbot", "verifierbot"+suffix, true)
	fx.disabledBot = insertUser("Disabledbot", "disabledbot"+suffix, true)
	fx.applicant = insertUser("Applicant", "bvapplicant"+suffix, false)
	fx.userPeer = insertUser("Marked", "markeduser"+suffix, false)

	fx.channel = nextChannelID
	if _, err := pool.Exec(ctx, `
INSERT INTO channels (
	id, access_hash, creator_user_id, title, username, broadcast, megagroup,
	participants_count, admins_count, top_message_id, pts, date
)
VALUES ($1, $2, $3, $4, $5, true, false, 1, 1, 1, 1, $6)`,
		fx.channel, unique, fx.applicant, "Fixture Marked News", "markednews"+suffix, int32(now.Unix())); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	unique++

	insertIcon := func(documentID, ownerBotID int64, name string, active bool) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO verification_icons (document_id, owner_bot_id, name, active, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
			documentID, ownerBotID, name, active, now).Scan(&id); err != nil {
			t.Fatalf("insert icon %s: %v", name, err)
		}
		return id
	}
	fx.sharedIcon = insertIcon(fx.sharedDoc, 0, "shared check "+suffix, true)
	// Reserved to the verifier bot and retired, so both the owner join and the
	// active filter have a case to answer.
	fx.reservedIcon = insertIcon(fx.reservedDoc, fx.verifierBot, "reserved check "+suffix, false)

	insertVerifier := func(botID, documentID int64, company string, enabled, canModify bool) {
		if _, err := pool.Exec(ctx, `
INSERT INTO bot_verifier_settings (
	bot_id, icon_document_id, company_name, default_description,
	can_modify_custom_description, enabled, granted_by, grant_reason,
	created_at, updated_at, version
) VALUES ($1, $2, $3, 'verified by the fixture', $4, $5, 'alice', 'partner programme', $6, $6, 4)`,
			botID, documentID, company, canModify, enabled, now); err != nil {
			t.Fatalf("insert verifier %d: %v", botID, err)
		}
	}
	insertVerifier(fx.verifierBot, fx.sharedDoc, "Fixture Trust "+suffix, true, true)
	insertVerifier(fx.disabledBot, fx.sharedDoc, "Switched Off "+suffix, false, false)

	insertMark := func(peerType string, peerID int64, description string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO custom_verifications (
	verifier_bot_id, peer_type, peer_id, icon_document_id, description,
	granted_by_user_id, created_at, updated_at, version
) VALUES ($1, $2, $3, $4, $5, $1, $6, $6, 2) RETURNING id`,
			fx.verifierBot, peerType, peerID, fx.sharedDoc, description, now).Scan(&id); err != nil {
			t.Fatalf("insert %s mark: %v", peerType, err)
		}
		return id
	}
	fx.userMark = insertMark("user", fx.userPeer, "verified individual")
	fx.channelMark = insertMark("channel", fx.channel, "verified outlet")

	insertRequest := func(peerType string, peerID int64, status, reason string) int64 {
		var approvedAt, rejectedAt *time.Time
		decisionReason := ""
		decidedBy := ""
		switch status {
		case "approved":
			approvedAt = &now
			decidedBy = "alice"
		case "rejected":
			rejectedAt = &now
			decidedBy = "bob"
			decisionReason = reason
		}
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO custom_verification_requests (
	verifier_bot_id, applicant_user_id, peer_type, peer_id, peer_title, peer_username,
	reason, requested_description, status, decided_by, decision_reason, internal_note,
	correlation_id, created_at, updated_at, approved_at, rejected_at, version
) VALUES (
	$1, $2, $3, $4, $5, $6, 'we are the outlet', 'verified partner', $7, $8, $9,
	'operator only', $10, $11, $11, $12, $13, 3
) RETURNING id`,
			fx.verifierBot, fx.applicant, peerType, peerID,
			"Snapshot "+peerType, "snapshot"+peerType+suffix,
			status, decidedBy, decisionReason, "bvcorr-"+status,
			now, approvedAt, rejectedAt,
		).Scan(&id); err != nil {
			t.Fatalf("insert %s request: %v", status, err)
		}
		return id
	}
	// One live application per (verifier, peer) pair, so the three seeded rows have
	// to name three different peers: the partial unique index enforces it.
	fx.pendingReq = insertRequest("channel", fx.channel, "pending", "")
	fx.approvedReq = insertRequest("user", fx.userPeer, "approved", "")
	// Filed against a peer that does not exist, so the live-peer join has a negative
	// case and the snapshot fallback is exercised.
	fx.rejectedReq = insertRequest("user", fx.userPeer+9_000_000, "rejected", "not an outlet")

	t.Cleanup(func() {
		reqIDs := []int64{fx.pendingReq, fx.approvedReq, fx.rejectedReq}
		_, _ = pool.Exec(ctx, "DELETE FROM custom_verification_requests WHERE id = ANY($1::bigint[])", reqIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM custom_verifications WHERE id = ANY($1::bigint[])",
			[]int64{fx.userMark, fx.channelMark})
		_, _ = pool.Exec(ctx, "DELETE FROM bot_verifier_settings WHERE bot_id = ANY($1::bigint[])",
			[]int64{fx.verifierBot, fx.disabledBot})
		_, _ = pool.Exec(ctx, "DELETE FROM verification_icons WHERE id = ANY($1::bigint[])",
			[]int64{fx.sharedIcon, fx.reservedIcon})
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", fx.channel)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])",
			[]int64{fx.verifierBot, fx.disabledBot, fx.applicant, fx.userPeer})
	})
	return fx
}

func TestBotVerificationReadStoreVerifiers(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedBotVerificationFixture(t, pool)
	ctx := context.Background()

	rows, err := store.ListBotVerifiers(ctx, false, 200)
	if err != nil {
		t.Fatalf("list verifiers: %v", err)
	}
	byID := map[int64]BotVerifierRow{}
	for _, row := range rows {
		byID[row.BotID] = row
	}
	verifier, ok := byID[fx.verifierBot]
	if !ok {
		t.Fatal("enabled verifier missing from the listing")
	}
	// The bot account is resolved through the join, and the icon's catalogue label
	// comes from the entry the document id points at.
	if verifier.BotUsername != "verifierbot"+fx.suffix || verifier.IconName != "shared check "+fx.suffix {
		t.Fatalf("verifier projection = %+v", verifier)
	}
	if verifier.BotName == "" || verifier.CompanyName != "Fixture Trust "+fx.suffix {
		t.Fatalf("verifier names = %+v", verifier)
	}
	if !verifier.Enabled || !verifier.CanModifyCustomDescription || verifier.Version != 4 ||
		verifier.GrantedBy != "alice" || verifier.GrantReason != "partner programme" {
		t.Fatalf("verifier settings = %+v", verifier)
	}
	// Both seeded marks belong to this verifier, and mark_count is what would
	// cascade away with a revocation.
	if verifier.MarkCount != 2 {
		t.Fatalf("mark count = %d, want 2", verifier.MarkCount)
	}
	if disabled := byID[fx.disabledBot]; disabled.Enabled || disabled.MarkCount != 0 {
		t.Fatalf("disabled verifier = %+v", disabled)
	}

	// enabled_only hides the switched-off verifier without dropping its row.
	enabled, err := store.ListBotVerifiers(ctx, true, 200)
	if err != nil {
		t.Fatalf("list enabled verifiers: %v", err)
	}
	for _, row := range enabled {
		if !row.Enabled {
			t.Fatalf("enabled_only leaked %+v", row)
		}
		if row.BotID == fx.disabledBot {
			t.Fatal("enabled_only returned the switched-off verifier")
		}
	}

	// The detail read reuses the list scanner, so one column order serves both.
	one, err := store.BotVerifier(ctx, fx.verifierBot)
	if err != nil {
		t.Fatalf("get verifier: %v", err)
	}
	if one.BotID != fx.verifierBot || one.MarkCount != 2 || one.IconName != verifier.IconName {
		t.Fatalf("verifier detail = %+v", one)
	}
	if _, err := store.BotVerifier(ctx, fx.applicant); err == nil {
		t.Fatal("a non-verifier resolved as one")
	}

	// The page bound is honoured.
	page, err := store.ListBotVerifiers(ctx, false, 1)
	if err != nil {
		t.Fatalf("bounded list: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("bounded page len=%d", len(page))
	}
}

func TestBotVerificationReadStoreIcons(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedBotVerificationFixture(t, pool)
	ctx := context.Background()

	rows, err := store.ListVerificationIcons(ctx, false, 200)
	if err != nil {
		t.Fatalf("list icons: %v", err)
	}
	byID := map[int64]VerificationIconRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	shared, ok := byID[fx.sharedIcon]
	if !ok {
		t.Fatal("shared icon missing from the catalogue listing")
	}
	if shared.OwnerBotID != 0 || shared.OwnerBotUsername != "" || !shared.Active {
		t.Fatalf("shared icon = %+v, want no owner", shared)
	}
	// Both seeded verifiers point at the shared document, so retiring it is a
	// decision the operator has to make knowingly.
	if shared.UsedByVerifiers != 2 {
		t.Fatalf("shared icon used_by_verifiers = %d, want 2", shared.UsedByVerifiers)
	}
	reserved, ok := byID[fx.reservedIcon]
	if !ok {
		t.Fatal("reserved icon missing from the catalogue listing")
	}
	if reserved.OwnerBotID != fx.verifierBot || reserved.OwnerBotUsername != "verifierbot"+fx.suffix {
		t.Fatalf("reserved icon = %+v, want the owner resolved", reserved)
	}
	if reserved.Active || reserved.UsedByVerifiers != 0 {
		t.Fatalf("reserved icon = %+v", reserved)
	}

	// active_only hides the retired entry.
	active, err := store.ListVerificationIcons(ctx, true, 200)
	if err != nil {
		t.Fatalf("list active icons: %v", err)
	}
	for _, row := range active {
		if !row.Active {
			t.Fatalf("active_only leaked %+v", row)
		}
		if row.ID == fx.reservedIcon {
			t.Fatal("active_only returned the retired entry")
		}
	}
	// Newest first.
	if len(rows) >= 2 && rows[0].ID < rows[1].ID {
		t.Fatalf("catalogue is not ordered newest first: %d before %d", rows[0].ID, rows[1].ID)
	}
}

func TestBotVerificationReadStoreMarks(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedBotVerificationFixture(t, pool)
	ctx := context.Background()

	rows, _, err := store.ListCustomVerifications(ctx, fx.verifierBot, "", "", 0, 200)
	if err != nil {
		t.Fatalf("list marks: %v", err)
	}
	byID := map[int64]CustomVerificationRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	// A user peer resolves through users; the verifier's company comes from its
	// settings row.
	userMark, ok := byID[fx.userMark]
	if !ok {
		t.Fatal("user mark missing from the listing")
	}
	if userMark.PeerType != "user" || userMark.PeerID != fx.userPeer ||
		userMark.PeerUsername != "markeduser"+fx.suffix {
		t.Fatalf("user mark peer = %+v", userMark)
	}
	if userMark.PeerTitle == "" || userMark.VerifierBotUsername != "verifierbot"+fx.suffix ||
		userMark.CompanyName != "Fixture Trust "+fx.suffix {
		t.Fatalf("user mark projection = %+v", userMark)
	}
	if userMark.IconDocumentID != fx.sharedDoc || userMark.Description != "verified individual" ||
		userMark.Version != 2 {
		t.Fatalf("user mark = %+v", userMark)
	}
	// A channel peer resolves through channels: the CASE picks the right namespace.
	channelMark, ok := byID[fx.channelMark]
	if !ok {
		t.Fatal("channel mark missing from the listing")
	}
	if channelMark.PeerType != "channel" || channelMark.PeerTitle != "Fixture Marked News" ||
		channelMark.PeerUsername != "markednews"+fx.suffix {
		t.Fatalf("channel mark peer = %+v", channelMark)
	}

	// Filters.
	typed, _, err := store.ListCustomVerifications(ctx, fx.verifierBot, "channel", "", 0, 50)
	if err != nil {
		t.Fatalf("peer_type list: %v", err)
	}
	for _, row := range typed {
		if row.PeerType != "channel" {
			t.Fatalf("peer_type filter leaked %+v", row)
		}
	}
	other, _, err := store.ListCustomVerifications(ctx, fx.disabledBot, "", "", 0, 50)
	if err != nil {
		t.Fatalf("verifier filter list: %v", err)
	}
	for _, row := range other {
		if row.VerifierBotID != fx.disabledBot {
			t.Fatalf("verifier filter leaked %+v", row)
		}
	}

	// q matches a mark id, a peer id, a verifier id and a username or title prefix.
	for _, query := range []string{
		strconv.FormatInt(fx.channelMark, 10),
		strconv.FormatInt(fx.channel, 10),
		strconv.FormatInt(fx.verifierBot, 10),
		"markednews" + fx.suffix,
		"@markeduser" + fx.suffix,
		"fixture marked",
	} {
		found, _, err := store.ListCustomVerifications(ctx, 0, "", query, 0, 50)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(found) == 0 {
			t.Fatalf("search %q returned nothing", query)
		}
	}

	// The keyset cursor excludes the row it points at.
	after, _, err := store.ListCustomVerifications(ctx, fx.verifierBot, "", "", fx.channelMark, 50)
	if err != nil {
		t.Fatalf("keyset list: %v", err)
	}
	for _, row := range after {
		if row.ID >= fx.channelMark {
			t.Fatalf("keyset page leaked id %d at or after the cursor %d", row.ID, fx.channelMark)
		}
	}
	// The page bound is honoured and reports more.
	page, more, err := store.ListCustomVerifications(ctx, fx.verifierBot, "", "", 0, 1)
	if err != nil {
		t.Fatalf("bounded list: %v", err)
	}
	if len(page) != 1 || !more {
		t.Fatalf("bounded page len=%d hasMore=%v", len(page), more)
	}
}

func TestBotVerificationReadStoreRequestsAndDetail(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedBotVerificationFixture(t, pool)
	ctx := context.Background()

	rows, _, err := store.ListCustomVerificationRequests(ctx, "", fx.verifierBot, "", "", 0, 200)
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	byID := map[int64]CustomVerificationRequestRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	pending, ok := byID[fx.pendingReq]
	if !ok {
		t.Fatal("pending application missing from the queue")
	}
	if pending.ApplicantUserID != fx.applicant || pending.ApplicantUsername != "bvapplicant"+fx.suffix ||
		pending.VerifierBotUsername != "verifierbot"+fx.suffix {
		t.Fatalf("applicant/verifier projection = %+v", pending)
	}
	// The peer is read live, not from the snapshot columns: an operator has to see
	// the peer as it is now.
	if pending.PeerTitle != "Fixture Marked News" || pending.PeerUsername != "markednews"+fx.suffix {
		t.Fatalf("live peer projection = %+v, want the channel as it is now", pending)
	}
	if pending.Status != "pending" || pending.InternalNote != "operator only" ||
		pending.CorrelationID != "bvcorr-pending" || pending.Version != 3 {
		t.Fatalf("operator fields = %+v", pending)
	}
	if !pending.ApprovedAt.IsZero() || !pending.RejectedAt.IsZero() {
		t.Fatalf("timestamps approved=%v rejected=%v, want an undecided application",
			pending.ApprovedAt, pending.RejectedAt)
	}
	if approved := byID[fx.approvedReq]; approved.ApprovedAt.IsZero() || approved.DecidedBy != "alice" {
		t.Fatalf("approved application = %+v", approved)
	}
	if rejected := byID[fx.rejectedReq]; rejected.RejectedAt.IsZero() || rejected.DecisionReason == "" {
		t.Fatalf("rejected application = %+v", rejected)
	}
	// A peer that does not exist falls back to the snapshot the applicant filed
	// with, so the row still renders as something the reviewer recognises.
	if gone := byID[fx.rejectedReq]; gone.PeerTitle != "Snapshot user" ||
		gone.PeerUsername != "snapshotuser"+fx.suffix {
		t.Fatalf("missing peer = %+v, want the snapshot fallback", gone)
	}

	// Filters.
	filtered, _, err := store.ListCustomVerificationRequests(ctx, "pending", 0, "", "", 0, 50)
	if err != nil {
		t.Fatalf("status list: %v", err)
	}
	for _, row := range filtered {
		if row.Status != "pending" {
			t.Fatalf("status filter leaked %+v", row)
		}
	}
	typed, _, err := store.ListCustomVerificationRequests(ctx, "", 0, "channel", "", 0, 50)
	if err != nil {
		t.Fatalf("peer_type list: %v", err)
	}
	for _, row := range typed {
		if row.PeerType != "channel" {
			t.Fatalf("peer_type filter leaked %+v", row)
		}
	}
	// q matches an application id, a peer id, the applicant id, and username or
	// title prefixes on both the live peer and the snapshot.
	for _, query := range []string{
		strconv.FormatInt(fx.pendingReq, 10),
		strconv.FormatInt(fx.channel, 10),
		strconv.FormatInt(fx.applicant, 10),
		"markednews" + fx.suffix,
		"snapshotchannel" + fx.suffix,
		"@bvapplicant" + fx.suffix,
		"snapshot ",
	} {
		found, _, err := store.ListCustomVerificationRequests(ctx, "", 0, "", query, 0, 50)
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(found) == 0 {
			t.Fatalf("search %q returned nothing", query)
		}
	}

	// The keyset cursor excludes the row it points at, and the bound reports more.
	after, _, err := store.ListCustomVerificationRequests(ctx, "", fx.verifierBot, "", "", fx.pendingReq, 50)
	if err != nil {
		t.Fatalf("keyset list: %v", err)
	}
	for _, row := range after {
		if row.ID >= fx.pendingReq {
			t.Fatalf("keyset page leaked id %d at or after the cursor %d", row.ID, fx.pendingReq)
		}
	}
	page, more, err := store.ListCustomVerificationRequests(ctx, "", fx.verifierBot, "", "", 0, 1)
	if err != nil {
		t.Fatalf("bounded list: %v", err)
	}
	if len(page) != 1 || !more {
		t.Fatalf("bounded page len=%d hasMore=%v", len(page), more)
	}

	// The detail read carries the verifier and the live mark state.
	detail, err := store.CustomVerificationRequestDetail(ctx, fx.approvedReq)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Request.ID != fx.approvedReq || detail.Verifier.BotID != fx.verifierBot ||
		detail.Verifier.CompanyName != "Fixture Trust "+fx.suffix {
		t.Fatalf("detail = %+v verifier=%+v", detail.Request, detail.Verifier)
	}
	// The approved application's peer really carries the mark.
	if !detail.MarkActive {
		t.Fatal("mark_active did not follow the granted mark")
	}
	// The pending application names the channel, which the fixture also marks, so
	// the channel side of the EXISTS probe is covered too.
	pendingDetail, err := store.CustomVerificationRequestDetail(ctx, fx.pendingReq)
	if err != nil {
		t.Fatalf("pending detail: %v", err)
	}
	if !pendingDetail.MarkActive {
		t.Fatal("the channel mark was not seen by the detail read")
	}
	// The rejected one names a peer nobody marked: mark_active must say so, which is
	// what tells "approved" apart from "approved and since stripped".
	goneDetail, err := store.CustomVerificationRequestDetail(ctx, fx.rejectedReq)
	if err != nil {
		t.Fatalf("rejected detail: %v", err)
	}
	if goneDetail.MarkActive {
		t.Fatal("an unmarked peer was reported as carrying a mark")
	}

	if _, err := store.CustomVerificationRequestDetail(ctx, 0); err == nil {
		t.Fatal("detail of a missing application succeeded")
	}
}

func TestBotVerificationReadStoreCounts(t *testing.T) {
	store, pool := verificationReadStore(t)
	fx := seedBotVerificationFixture(t, pool)
	ctx := context.Background()

	counts, err := store.CustomVerificationRequestCounts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	// Every modelled status is present, so the panel never tells "none" from
	// "missing".
	for _, status := range []string{"pending", "approved", "rejected", "revoked"} {
		if _, ok := counts[status]; !ok {
			t.Fatalf("counts %+v missing %q", counts, status)
		}
	}
	if counts["pending"] == "0" || counts["approved"] == "0" || counts["rejected"] == "0" {
		t.Fatalf("counts %+v did not see the seeded applications (%d/%d/%d)",
			counts, fx.pendingReq, fx.approvedReq, fx.rejectedReq)
	}
}
