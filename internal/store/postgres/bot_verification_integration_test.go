package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/observability/dbtrace"
)

// botVerificationTestUser inserts a throwaway user row and registers the cleanup
// for everything the third-party verification tables may hang off it. Marks
// cascade from the verifier row, but the applications reference users directly,
// so they have to go first.
func botVerificationTestUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name)
VALUES ($1, $2, 'bot verification test')
RETURNING id`, time.Now().UnixNano()&0x7fffffffffffffff, "9"+randomSuffix(t)).Scan(&id); err != nil {
		t.Fatalf("insert bot verification test user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM custom_verification_requests
WHERE verifier_bot_id = $1 OR applicant_user_id = $1`, id)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM custom_verifications WHERE verifier_bot_id = $1`, id)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM bot_verifier_settings WHERE bot_id = $1`, id)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// botVerificationTestDocumentID hands out a custom emoji document id no other
// test run collides on, because verification_icons.document_id is unique.
func botVerificationTestDocumentID(t *testing.T) int64 {
	t.Helper()
	raw, err := strconv.ParseInt(randomSuffix(t), 16, 64)
	if err != nil {
		t.Fatalf("parse random document id: %v", err)
	}
	return raw + 1
}

// botVerificationTestIcon adds a catalogue entry and removes it afterwards; the
// catalogue is deployment-wide, so it is not covered by the user cleanup.
func botVerificationTestIcon(t *testing.T, pool *pgxpool.Pool, s *BotVerificationStore, name string, ownerBotID int64) domain.VerificationIcon {
	t.Helper()
	icon, err := s.UpsertVerificationIcon(context.Background(), domain.VerificationIcon{
		DocumentID: botVerificationTestDocumentID(t),
		OwnerBotID: ownerBotID,
		Name:       name,
		Active:     true,
	})
	if err != nil {
		t.Fatalf("upsert verification icon %q: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM verification_icons WHERE id = $1`, icon.ID)
	})
	return icon
}

// botVerificationTestVerifier grants verifier status the way the admin edge does.
func botVerificationTestVerifier(t *testing.T, s *BotVerificationStore, botID, iconDocumentID int64) domain.BotVerifierSettings {
	t.Helper()
	settings, err := s.UpsertBotVerifierSettings(context.Background(), domain.BotVerifierSettings{
		BotID:                      botID,
		IconDocumentID:             iconDocumentID,
		CompanyName:                fmt.Sprintf("Verifier %d", botID),
		DefaultDescription:         "verified by the test verifier",
		CanModifyCustomDescription: true,
		Enabled:                    true,
		GrantedBy:                  "operator",
		GrantReason:                "test fixture",
	})
	if err != nil {
		t.Fatalf("grant verifier %d: %v", botID, err)
	}
	return settings
}

func botVerificationTestRequest(verifier, applicant int64, peer domain.Peer, username string) domain.CustomVerificationRequest {
	return domain.CustomVerificationRequest{
		VerifierBotID:        verifier,
		ApplicantUserID:      applicant,
		Peer:                 peer,
		PeerTitle:            "Target " + username,
		PeerUsername:         username,
		Reason:               "we run the official account for this brand",
		RequestedDescription: "official brand account",
		CorrelationID:        fmt.Sprintf("corr-%d", peer.ID),
	}
}

// botVerificationTxGrant is the callback shape the app layer is expected to use:
// the mark is written through the transaction that is deciding the application,
// so the two writes commit or roll back together.
func botVerificationTxGrant() func(context.Context, domain.CustomVerificationRequest) error {
	return func(ctx context.Context, req domain.CustomVerificationRequest) error {
		tx, ok := VerificationTxFromContext(ctx)
		if !ok {
			return fmt.Errorf("decision context carries no transaction")
		}
		_, _, err := NewBotVerificationStore(tx).GrantCustomVerification(ctx,
			domain.CustomVerification{
				VerifierBotID:   req.VerifierBotID,
				Peer:            req.Peer,
				Description:     req.RequestedDescription,
				GrantedByUserID: req.ApplicantUserID,
			})
		return err
	}
}

func botVerificationTxRevoke() func(context.Context, domain.CustomVerificationRequest) error {
	return func(ctx context.Context, req domain.CustomVerificationRequest) error {
		tx, ok := VerificationTxFromContext(ctx)
		if !ok {
			return fmt.Errorf("decision context carries no transaction")
		}
		_, err := NewBotVerificationStore(tx).RevokeCustomVerification(ctx,
			req.VerifierBotID, req.Peer)
		return err
	}
}

func botVerificationUserPeer(id int64) domain.Peer {
	return domain.Peer{Type: domain.PeerTypeUser, ID: id}
}

func botVerificationChannelPeer(id int64) domain.Peer {
	return domain.Peer{Type: domain.PeerTypeChannel, ID: id}
}

// botVerificationIconIndex locates an entry in a catalogue listing. The
// catalogue is deployment-wide, so a test asserts on positions of its own rows
// rather than on the length of the page.
func botVerificationIconIndex(icons []domain.VerificationIcon, iconID int64) int {
	for i, icon := range icons {
		if icon.ID == iconID {
			return i
		}
	}
	return -1
}

// TestBotVerificationIconCataloguePostgres covers the catalogue against the real
// schema: an entry is keyed by document id, retiring one keeps it readable, and
// the listing pages newest first with the activeOnly filter honoured.
func TestBotVerificationIconCataloguePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)

	first := botVerificationTestIcon(t, pool, s, "  Blue check  ", 0)
	if first.ID == 0 || first.Name != "Blue check" || !first.Active {
		t.Fatalf("stored icon = %+v", first)
	}
	if first.CreatedAt.IsZero() || !first.UpdatedAt.Equal(first.CreatedAt) ||
		first.CreatedAt.Location() != time.UTC {
		t.Fatalf("icon timestamps = %v / %v", first.CreatedAt, first.UpdatedAt)
	}
	second := botVerificationTestIcon(t, pool, s, "Reserved", 777)
	if second.OwnerBotID != 777 {
		t.Fatalf("reserved icon = %+v", second)
	}

	// document_id is the identity of an entry: a second upsert edits in place.
	edited, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{
		DocumentID: first.DocumentID, OwnerBotID: 42, Name: "Blue check v2", Active: true,
	})
	if err != nil {
		t.Fatalf("re-upsert icon: %v", err)
	}
	if edited.ID != first.ID || edited.Name != "Blue check v2" || edited.OwnerBotID != 42 {
		t.Fatalf("edited icon = %+v, want id %d", edited, first.ID)
	}
	if !edited.CreatedAt.Equal(first.CreatedAt) || !edited.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("edited timestamps = %v / %v", edited.CreatedAt, edited.UpdatedAt)
	}
	if _, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{
		DocumentID: 0, Name: "no document",
	}); !errors.Is(err, domain.ErrVerificationIconInvalid) {
		t.Fatalf("upsert without document err = %v, want ErrVerificationIconInvalid", err)
	}

	retired, err := s.SetVerificationIconActive(ctx, second.ID, false)
	if err != nil {
		t.Fatalf("retire icon: %v", err)
	}
	if retired.Active || !retired.UpdatedAt.After(second.UpdatedAt) {
		t.Fatalf("retired icon = %+v", retired)
	}
	if _, err := s.SetVerificationIconActive(ctx, 0, false); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("retire icon 0 err = %v, want ErrVerificationIconNotFound", err)
	}
	if _, err := s.SetVerificationIconActive(ctx, second.ID+1<<40, false); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("retire unknown err = %v, want ErrVerificationIconNotFound", err)
	}

	byDocument, err := s.VerificationIconByDocument(ctx, second.DocumentID)
	if err != nil || byDocument.ID != second.ID || byDocument.Active {
		t.Fatalf("icon by document = %+v err=%v", byDocument, err)
	}
	if _, err := s.VerificationIconByDocument(ctx, -1); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("icon by bad document err = %v, want ErrVerificationIconNotFound", err)
	}
	byID, err := s.VerificationIcon(ctx, first.ID)
	if err != nil || byID.DocumentID != first.DocumentID {
		t.Fatalf("icon by id = %+v err=%v", byID, err)
	}
	if _, err := s.VerificationIcon(ctx, first.ID+1<<40); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("unknown icon err = %v, want ErrVerificationIconNotFound", err)
	}

	all, err := s.ListVerificationIcons(ctx, false, maxBotVerificationListLimit)
	if err != nil {
		t.Fatalf("list icons: %v", err)
	}
	firstAt, secondAt := botVerificationIconIndex(all, first.ID), botVerificationIconIndex(all, second.ID)
	if firstAt < 0 || secondAt < 0 || secondAt >= firstAt {
		t.Fatalf("catalogue order: first at %d, second at %d, want newest first", firstAt, secondAt)
	}
	active, err := s.ListVerificationIcons(ctx, true, maxBotVerificationListLimit)
	if err != nil {
		t.Fatalf("list active icons: %v", err)
	}
	if botVerificationIconIndex(active, first.ID) < 0 {
		t.Fatal("active catalogue lost the active icon")
	}
	if botVerificationIconIndex(active, second.ID) >= 0 {
		t.Fatal("active catalogue kept the retired icon")
	}
	if page, err := s.ListVerificationIcons(ctx, false, 1); err != nil || len(page) != 1 {
		t.Fatalf("icon page = %+v err=%v", page, err)
	}
}

// TestBotVerifierSettingsLifecyclePostgres covers verifier status: optimistic
// locking on version, the idempotent kill switch and the cascade that takes the
// marks with the verifier row while the applications stay as history.
func TestBotVerifierSettingsLifecyclePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)
	bot := botVerificationTestUser(t, pool)
	otherBot := botVerificationTestUser(t, pool)
	applicant := botVerificationTestUser(t, pool)
	icon := botVerificationTestIcon(t, pool, s, "verifier icon", 0)

	if _, err := s.BotVerifierSettings(ctx, bot); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("unknown verifier err = %v, want ErrVerifierNotFound", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, IconDocumentID: icon.DocumentID, CompanyName: "Acme", Version: 3,
	}); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("versioned upsert of missing row err = %v, want ErrVerifierNotFound", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, CompanyName: "Acme",
	}); !errors.Is(err, domain.ErrVerifierSettingsInvalid) {
		t.Fatalf("iconless upsert err = %v, want ErrVerifierSettingsInvalid", err)
	}

	created := botVerificationTestVerifier(t, s, bot, icon.DocumentID)
	if created.Version != 1 || !created.Enabled || !created.CanModifyCustomDescription {
		t.Fatalf("created verifier = %+v", created)
	}
	if created.CreatedAt.IsZero() || !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Fatalf("verifier timestamps = %v / %v", created.CreatedAt, created.UpdatedAt)
	}

	// Version 0 means "there is no row yet", so it loses against the stored row.
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, IconDocumentID: icon.DocumentID, CompanyName: "Acme",
	}); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("re-create err = %v, want ErrCustomVerificationVersionConflict", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, IconDocumentID: icon.DocumentID, CompanyName: "Acme", Version: 99,
	}); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("stale upsert err = %v, want ErrCustomVerificationVersionConflict", err)
	}

	edited, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID:              bot,
		IconDocumentID:     icon.DocumentID + 1,
		CompanyName:        "Acme Media",
		DefaultDescription: "checked by Acme",
		Enabled:            true,
		GrantedBy:          "operator",
		Version:            created.Version,
	})
	if err != nil {
		t.Fatalf("edit verifier: %v", err)
	}
	if edited.Version != 2 || edited.CanModifyCustomDescription ||
		edited.DefaultDescription != "checked by Acme" {
		t.Fatalf("edited verifier = %+v", edited)
	}
	if !edited.CreatedAt.Equal(created.CreatedAt) || !edited.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("edited timestamps = %v / %v", edited.CreatedAt, edited.UpdatedAt)
	}
	// DescriptionFor is the single place the description rule lives; the stored
	// row is what feeds it.
	if _, err := edited.DescriptionFor("per-peer text"); !errors.Is(err, domain.ErrVerifierDescriptionForbidden) {
		t.Fatalf("custom description on a locked verifier err = %v, want ErrVerifierDescriptionForbidden", err)
	}
	if got, err := edited.DescriptionFor(""); err != nil || got != "checked by Acme" {
		t.Fatalf("default description = %q err=%v", got, err)
	}

	disabled, err := s.SetBotVerifierEnabled(ctx, bot, false)
	if err != nil {
		t.Fatalf("disable verifier: %v", err)
	}
	if disabled.Enabled || disabled.Version != edited.Version+1 {
		t.Fatalf("disabled verifier = %+v", disabled)
	}
	again, err := s.SetBotVerifierEnabled(ctx, bot, false)
	if err != nil || again.Version != disabled.Version {
		t.Fatalf("re-disable = v%d err=%v, want v%d", again.Version, err, disabled.Version)
	}
	if _, err := s.SetBotVerifierEnabled(ctx, otherBot, true); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("disable non-verifier err = %v, want ErrVerifierNotFound", err)
	}
	if stored, err := s.BotVerifierSettings(ctx, bot); err != nil || stored.Enabled {
		t.Fatalf("read disabled verifier = %+v err=%v", stored, err)
	}

	enabledOther := botVerificationTestVerifier(t, s, otherBot, icon.DocumentID)
	batch, err := s.BotVerifierSettingsBatch(ctx, []int64{bot, otherBot, applicant, 0, bot})
	if err != nil {
		t.Fatalf("batch verifiers: %v", err)
	}
	if len(batch) != 2 || batch[bot].Enabled || !batch[otherBot].Enabled {
		t.Fatalf("verifier batch = %+v", batch)
	}
	if _, present := batch[applicant]; present {
		t.Fatal("batch invented verifier status for a plain user")
	}
	if empty, err := s.BotVerifierSettingsBatch(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty verifier batch = %+v err=%v", empty, err)
	}

	listed, err := s.ListBotVerifiers(ctx, false, maxBotVerificationListLimit)
	if err != nil {
		t.Fatalf("list verifiers: %v", err)
	}
	seenDisabled, seenEnabled := false, false
	for _, settings := range listed {
		seenDisabled = seenDisabled || settings.BotID == bot
		seenEnabled = seenEnabled || settings.BotID == otherBot
	}
	if !seenDisabled || !seenEnabled {
		t.Fatalf("verifier list lost a row: %+v", listed)
	}
	enabledOnly, err := s.ListBotVerifiers(ctx, true, maxBotVerificationListLimit)
	if err != nil {
		t.Fatalf("list enabled verifiers: %v", err)
	}
	for _, settings := range enabledOnly {
		if settings.BotID == bot {
			t.Fatal("enabled-only list kept a disabled verifier")
		}
	}

	// Marks cascade with the verifier row; applications do not.
	peer := botVerificationChannelPeer(applicant)
	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: enabledOther.BotID, Peer: peer, Description: "cascade me",
	}); err != nil {
		t.Fatalf("grant before delete: %v", err)
	}
	req, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(enabledOther.BotID, applicant, peer, "CascadeChannel"))
	if err != nil {
		t.Fatalf("file application before delete: %v", err)
	}
	removed, err := s.DeleteBotVerifierSettings(ctx, enabledOther.BotID)
	if err != nil || !removed {
		t.Fatalf("delete verifier: removed=%v err=%v", removed, err)
	}
	if removed, err := s.DeleteBotVerifierSettings(ctx, enabledOther.BotID); err != nil || removed {
		t.Fatalf("repeated delete: removed=%v err=%v", removed, err)
	}
	if _, err := s.CustomVerification(ctx, enabledOther.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("mark after cascade err = %v, want ErrCustomVerificationNotFound", err)
	}
	if count, err := s.CountCustomVerifications(ctx, enabledOther.BotID); err != nil || count != 0 {
		t.Fatalf("mark count after cascade = %d err=%v", count, err)
	}
	if kept, err := s.CustomVerificationRequest(ctx, req.ID); err != nil || kept.ID != req.ID {
		t.Fatalf("application after cascade = %+v err=%v", kept, err)
	}
}

// TestCustomVerificationProjectionPostgres is the projection contract: exactly
// one mark exists per peer, a later verifier replaces the former one, a disabled
// verifier projects nothing while its row survives, and the batch form resolves
// every peer in a single query.
func TestCustomVerificationProjectionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)
	alphaBot := botVerificationTestUser(t, pool)
	betaBot := botVerificationTestUser(t, pool)
	target := botVerificationTestUser(t, pool)
	icon := botVerificationTestIcon(t, pool, s, "projection icon", 0)
	alpha := botVerificationTestVerifier(t, s, alphaBot, icon.DocumentID)
	beta := botVerificationTestVerifier(t, s, betaBot, icon.DocumentID+1)
	peer := botVerificationUserPeer(target)

	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: target, Peer: peer,
	}); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("grant by non-verifier err = %v, want ErrVerifierNotFound", err)
	}
	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: domain.Peer{Type: domain.PeerTypeCommunity, ID: 5},
	}); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
		t.Fatalf("grant on community err = %v, want ErrCustomVerificationTargetInvalid", err)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection of an unmarked peer err = %v, want ErrCustomVerificationNotFound", err)
	}

	// The icon is denormalised from the verifier when the caller leaves it unset.
	alphaMark, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: peer, Description: "checked by alpha",
		GrantedByUserID: target,
	})
	if err != nil || !created {
		t.Fatalf("grant alpha mark: created=%v err=%v", created, err)
	}
	if alphaMark.IconDocumentID != alpha.IconDocumentID || alphaMark.Version != 1 ||
		alphaMark.Peer != peer {
		t.Fatalf("alpha mark = %+v", alphaMark)
	}
	betaMark, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: beta.BotID, Peer: peer, Description: "checked by beta",
	})
	if err != nil || !created {
		t.Fatalf("grant beta mark: created=%v err=%v", created, err)
	}
	if betaMark.ID != alphaMark.ID || betaMark.Version != alphaMark.Version+1 {
		t.Fatalf("replacement mark = %+v, want id %d v%d", betaMark, alphaMark.ID, alphaMark.Version+1)
	}
	if _, err := s.CustomVerification(ctx, alpha.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("replaced alpha mark err = %v, want ErrCustomVerificationNotFound", err)
	}

	// One peer has one wire-visible mark, irrespective of how often it is read.
	for i := 0; i < 3; i++ {
		got, err := s.PeerVerification(ctx, peer)
		if err != nil {
			t.Fatalf("projection after replacement: %v", err)
		}
		if got.ID != betaMark.ID || got.Projection().Icon != beta.IconDocumentID {
			t.Fatalf("projection = %+v, want replacement mark %d", got, betaMark.ID)
		}
	}

	// Kill switch: disabling the current verifier hides the badge. The replaced
	// alpha mark must not silently reappear.
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, false); err != nil {
		t.Fatalf("disable beta: %v", err)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection after disabling beta err=%v, want ErrCustomVerificationNotFound", err)
	}
	if stored, err := s.CustomVerification(ctx, beta.BotID, peer); err != nil || stored.ID != betaMark.ID {
		t.Fatalf("disabled verifier lost its mark: %+v err=%v", stored, err)
	}
	if batch, err := s.PeerVerificationBatch(ctx, []domain.Peer{peer}); err != nil || len(batch) != 0 {
		t.Fatalf("batch with current verifier disabled = %+v err=%v", batch, err)
	}
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, true); err != nil {
		t.Fatalf("re-enable beta: %v", err)
	}
	if got, err := s.PeerVerification(ctx, peer); err != nil || got.ID != betaMark.ID {
		t.Fatalf("projection after re-enabling = %+v err=%v", got, err)
	}

	// Granting through alpha replaces beta's mark on the same peer.
	regranted, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: peer, IconDocumentID: icon.DocumentID + 99,
		Description: "checked by alpha, again",
	})
	if err != nil || !created {
		t.Fatalf("re-grant alpha mark: created=%v err=%v", created, err)
	}
	if regranted.ID != betaMark.ID || regranted.Version != betaMark.Version+1 ||
		regranted.IconDocumentID != icon.DocumentID+99 ||
		regranted.Description != "checked by alpha, again" {
		t.Fatalf("re-granted mark = %+v", regranted)
	}
	if regranted.CreatedAt.Before(betaMark.CreatedAt) || !regranted.UpdatedAt.After(betaMark.UpdatedAt) {
		t.Fatalf("re-granted timestamps = %v / %v", regranted.CreatedAt, regranted.UpdatedAt)
	}
	if count, err := s.CountCustomVerifications(ctx, alpha.BotID); err != nil || count != 1 {
		t.Fatalf("alpha mark count = %d err=%v", count, err)
	}
	if got, err := s.PeerVerification(ctx, peer); err != nil || got.VerifierBotID != alpha.BotID {
		t.Fatalf("projection after re-grant = %+v err=%v", got, err)
	}

	// The batch form resolves many peers with one round trip.
	second := botVerificationChannelPeer(target + 1)
	third := botVerificationUserPeer(target + 2)
	fourth := botVerificationChannelPeer(target + 3)
	unmarked := botVerificationChannelPeer(target + 4)
	secondMark, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: second, Description: "second",
	})
	if err != nil {
		t.Fatalf("grant second: %v", err)
	}
	thirdMark, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: beta.BotID, Peer: third, Description: "third",
	})
	if err != nil {
		t.Fatalf("grant third: %v", err)
	}
	fourthMark, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: beta.BotID, Peer: fourth, Description: "fourth",
	})
	if err != nil {
		t.Fatalf("grant fourth: %v", err)
	}

	tracedCtx, stats := dbtrace.WithStats(ctx)
	before := stats.Snapshot()
	batch, err := s.PeerVerificationBatch(tracedCtx, []domain.Peer{
		peer, second, third, fourth, unmarked, peer, {Type: domain.PeerTypeUser, ID: 0},
	})
	if err != nil {
		t.Fatalf("batch projection: %v", err)
	}
	// The point of the batch form: five distinct peers, one query. An N+1
	// implementation would put a round trip per peer on every serialisation.
	if spent := stats.Snapshot().Sub(before); spent.Queries != 1 {
		t.Fatalf("batch projection ran %d queries, want exactly 1", spent.Queries)
	}
	if len(batch) != 4 {
		t.Fatalf("batch projection = %+v, want 4 peers", batch)
	}
	if batch[peer].ID != regranted.ID || batch[second].ID != secondMark.ID ||
		batch[third].ID != thirdMark.ID || batch[fourth].ID != fourthMark.ID {
		t.Fatalf("batch projection picked %+v", batch)
	}
	if _, present := batch[unmarked]; present {
		t.Fatal("batch projected an unmarked peer")
	}
	if empty, err := s.PeerVerificationBatch(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch = %+v err=%v", empty, err)
	}

	// Disabling a verifier drops its peers from the batch too, in the same query.
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, false); err != nil {
		t.Fatalf("disable beta again: %v", err)
	}
	batch, err = s.PeerVerificationBatch(ctx, []domain.Peer{peer, second, third, fourth})
	if err != nil {
		t.Fatalf("batch projection after disable: %v", err)
	}
	if len(batch) != 2 || batch[peer].ID != regranted.ID || batch[second].ID != secondMark.ID {
		t.Fatalf("batch after disable = %+v", batch)
	}
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, true); err != nil {
		t.Fatalf("re-enable beta again: %v", err)
	}

	// Listing, filtering and keyset paging over the marks.
	mine, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: beta.BotID,
	})
	if err != nil || len(mine) != 2 || mine[0].ID != fourthMark.ID {
		t.Fatalf("verifier-filtered marks = %+v err=%v", mine, err)
	}
	page, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: beta.BotID, Limit: 2,
	})
	if err != nil || len(page) != 2 {
		t.Fatalf("mark page = %+v err=%v", page, err)
	}
	next, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: beta.BotID, Limit: 2, BeforeID: page[len(page)-1].ID,
	})
	if err != nil || len(next) != 0 {
		t.Fatalf("mark keyset tail = %+v err=%v", next, err)
	}
	channels, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: beta.BotID, PeerType: domain.PeerTypeChannel,
	})
	if err != nil || len(channels) != 1 || channels[0].Peer != fourth {
		t.Fatalf("channel-filtered marks = %+v err=%v", channels, err)
	}
	byPeer, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		PeerType: peer.Type, PeerID: peer.ID,
	})
	if err != nil || len(byPeer) != 1 {
		t.Fatalf("peer-filtered marks = %+v err=%v", byPeer, err)
	}
	byQuery, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: alpha.BotID, Query: fmt.Sprintf("%d", second.ID),
	})
	if err != nil || len(byQuery) != 1 || byQuery[0].ID != secondMark.ID {
		t.Fatalf("numeric mark query = %+v err=%v", byQuery, err)
	}
	byText, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: alpha.BotID, Query: "AGAIN",
	})
	if err != nil || len(byText) != 1 || byText[0].ID != regranted.ID {
		t.Fatalf("text mark query = %+v err=%v", byText, err)
	}
	if _, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		PeerType: domain.PeerTypeFolder,
	}); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
		t.Fatalf("bad peer-type filter err = %v, want ErrCustomVerificationTargetInvalid", err)
	}

	// A replaced verifier cannot revoke the current mark.
	revoked, err := s.RevokeCustomVerification(ctx, beta.BotID, peer)
	if err != nil || revoked {
		t.Fatalf("revoke replaced beta mark: revoked=%v err=%v", revoked, err)
	}
	if revoked, err := s.RevokeCustomVerification(ctx, beta.BotID, peer); err != nil || revoked {
		t.Fatalf("repeated revoke: revoked=%v err=%v", revoked, err)
	}
	if got, err := s.PeerVerification(ctx, peer); err != nil || got.ID != regranted.ID {
		t.Fatalf("projection after rejected revoke = %+v err=%v", got, err)
	}
	if revoked, err := s.RevokeCustomVerification(ctx, alpha.BotID, peer); err != nil || !revoked {
		t.Fatalf("revoke alpha mark: revoked=%v err=%v", revoked, err)
	}
	if _, err := s.RevokeCustomVerification(ctx, 0, peer); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
		t.Fatalf("revoke without verifier err = %v, want ErrCustomVerificationTargetInvalid", err)
	}
}

// TestCustomVerificationLimitPostgres pins the per-verifier bound: a new mark is
// refused at the limit while an existing one can still be re-described.
func TestCustomVerificationLimitPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)
	bot := botVerificationTestUser(t, pool)
	icon := botVerificationTestIcon(t, pool, s, "limit icon", 0)
	verifier := botVerificationTestVerifier(t, s, bot, icon.DocumentID)

	// The bound is 10k marks; filling it through the store would be 10k round
	// trips, so the fixture is seeded in one statement. peer_id has no foreign
	// key, which is what makes that safe.
	if _, err := pool.Exec(ctx, `
INSERT INTO custom_verifications (
  verifier_bot_id, peer_type, peer_id, icon_document_id, description,
  created_at, updated_at, version
)
SELECT $1, 'channel', g, $2, '', $3, $3, 1
FROM generate_series(1, $4::bigint) AS g`,
		verifier.BotID, verifier.IconDocumentID, time.Now().UTC(),
		domain.MaxCustomVerificationsPerVerifier); err != nil {
		t.Fatalf("seed marks: %v", err)
	}
	count, err := s.CountCustomVerifications(ctx, verifier.BotID)
	if err != nil || count != domain.MaxCustomVerificationsPerVerifier {
		t.Fatalf("mark count = %d err=%v", count, err)
	}

	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationUserPeer(bot),
	}); !errors.Is(err, domain.ErrCustomVerificationLimit) {
		t.Fatalf("grant past the limit err = %v, want ErrCustomVerificationLimit", err)
	}
	// The bound is on creating marks, not on editing them.
	updated, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationChannelPeer(7),
		Description: "still editable at the limit",
	})
	if err != nil || created {
		t.Fatalf("re-grant at the limit: created=%v err=%v", created, err)
	}
	if updated.Description != "still editable at the limit" || updated.Version != 2 {
		t.Fatalf("re-granted mark at the limit = %+v", updated)
	}
	// Freeing one slot lets the next grant through.
	if revoked, err := s.RevokeCustomVerification(ctx, verifier.BotID,
		botVerificationChannelPeer(1)); err != nil || !revoked {
		t.Fatalf("free a slot: revoked=%v err=%v", revoked, err)
	}
	if _, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationUserPeer(bot),
	}); err != nil || !created {
		t.Fatalf("grant into the freed slot: created=%v err=%v", created, err)
	}
}

// TestCustomVerificationRequestQueuePostgres covers the application queue
// against the real schema: one pending application per (verifier, peer), the
// decision status machine, and the transaction that keeps an approved
// application and its mark together -- including the rollback when the callback
// fails after it has already written through the decision's transaction.
func TestCustomVerificationRequestQueuePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)
	bot := botVerificationTestUser(t, pool)
	applicant := botVerificationTestUser(t, pool)
	target := botVerificationTestUser(t, pool)
	icon := botVerificationTestIcon(t, pool, s, "queue icon", 0)
	verifier := botVerificationTestVerifier(t, s, bot, icon.DocumentID)
	peer := botVerificationChannelPeer(target)
	// A distinct peer id, so the numeric queue search below addresses one peer
	// rather than both shapes of the same number.
	other := botVerificationUserPeer(target + 1)

	baseline, err := s.CustomVerificationRequestCounts(ctx)
	if err != nil {
		t.Fatalf("baseline queue counts: %v", err)
	}

	filed, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(verifier.BotID, applicant, peer, "AcmeNews"))
	if err != nil {
		t.Fatalf("file application: %v", err)
	}
	if filed.Status != domain.CustomVerificationPending || filed.Version != 1 {
		t.Fatalf("filed application = %s v%d", filed.Status, filed.Version)
	}
	if !filed.ApprovedAt.IsZero() || !filed.RejectedAt.IsZero() || filed.DecidedBy != "" {
		t.Fatalf("filed application carries a decision: %+v", filed)
	}
	if filed.Peer != peer || filed.PeerUsername != "AcmeNews" ||
		filed.CorrelationID != fmt.Sprintf("corr-%d", peer.ID) {
		t.Fatalf("filed payload did not round-trip: %+v", filed)
	}

	// custom_verification_requests_pending_idx: one live application per pair.
	if _, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(verifier.BotID, applicant, peer, "AcmeNews")); !errors.Is(err, domain.ErrCustomVerificationRequestExists) {
		t.Fatalf("duplicate pending err = %v, want ErrCustomVerificationRequestExists", err)
	}
	if _, err := s.CreateCustomVerificationRequest(ctx, domain.CustomVerificationRequest{
		VerifierBotID: verifier.BotID, ApplicantUserID: applicant, Peer: peer,
		Status: domain.CustomVerificationApproved,
	}); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("pre-decided application err = %v, want ErrCustomVerificationRequestInvalid", err)
	}
	// The SQL byte bound reserves the worst-case UTF-8 width for the domain's
	// rune limit, so valid multi-byte text must behave like ASCII.
	wide := botVerificationTestRequest(verifier.BotID, applicant, botVerificationUserPeer(target+9), "Wide")
	wide.Reason = strings.Repeat("é", domain.MaxCustomVerificationReasonLength-1)
	wideFiled, err := s.CreateCustomVerificationRequest(ctx, wide)
	if err != nil {
		t.Fatalf("valid multi-byte reason: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM custom_verification_requests WHERE id = $1`, wideFiled.ID); err != nil {
		t.Fatalf("clean wide application fixture: %v", err)
	}
	if pending, err := s.PendingCustomVerificationRequest(ctx, verifier.BotID, peer); err != nil ||
		pending.ID != filed.ID {
		t.Fatalf("pending application = %+v err=%v", pending, err)
	}

	// A failing callback rolls the whole decision back, including the mark the
	// callback wrote through the decision's own transaction before it failed.
	boom := errors.New("apply exploded")
	grant := botVerificationTxGrant()
	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version,
		domain.CustomVerificationApproved, "operator", "looks good", "",
		func(ctx context.Context, req domain.CustomVerificationRequest) error {
			if err := grant(ctx, req); err != nil {
				return err
			}
			return boom
		}); !errors.Is(err, boom) {
		t.Fatalf("failing apply err = %v, want the callback error", err)
	}
	rolledBack, err := s.CustomVerificationRequest(ctx, filed.ID)
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if rolledBack.Status != domain.CustomVerificationPending ||
		rolledBack.Version != filed.Version || !rolledBack.ApprovedAt.IsZero() ||
		rolledBack.DecidedBy != "" {
		t.Fatalf("application after rollback = %+v, want pending v%d", rolledBack, filed.Version)
	}
	if _, err := s.CustomVerification(ctx, verifier.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("mark after rollback err = %v, want ErrCustomVerificationNotFound", err)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection after rollback err = %v, want ErrCustomVerificationNotFound", err)
	}

	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version,
		domain.CustomVerificationApproved, "operator", "", "", nil); err == nil {
		t.Fatal("approve without a callback succeeded")
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version+7,
		domain.CustomVerificationApproved, "operator", "", "", grant); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("stale decision err = %v, want ErrCustomVerificationVersionConflict", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version,
		domain.CustomVerificationPending, "operator", "", "", nil); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("decision back to pending err = %v, want ErrCustomVerificationRequestInvalid", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID+1<<40, filed.Version,
		domain.CustomVerificationApproved, "operator", "", "", grant); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("decision on unknown application err = %v, want ErrCustomVerificationRequestNotFound", err)
	}

	approved, changed, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version,
		domain.CustomVerificationApproved, "operator", "brand confirmed", "ticket 12", grant)
	if err != nil || !changed {
		t.Fatalf("approve: changed=%v err=%v", changed, err)
	}
	if approved.Status != domain.CustomVerificationApproved ||
		approved.Version != filed.Version+1 || approved.ApprovedAt.IsZero() ||
		!approved.RejectedAt.IsZero() {
		t.Fatalf("approved application = %+v", approved)
	}
	if approved.DecidedBy != "operator" || approved.DecisionReason != "brand confirmed" ||
		approved.InternalNote != "ticket 12" {
		t.Fatalf("approved decision metadata = %+v", approved)
	}
	mark, err := s.PeerVerification(ctx, peer)
	if err != nil {
		t.Fatalf("projection after approve: %v", err)
	}
	if mark.VerifierBotID != verifier.BotID || mark.Description != filed.RequestedDescription ||
		mark.IconDocumentID != verifier.IconDocumentID || mark.GrantedByUserID != applicant {
		t.Fatalf("mark after approve = %+v", mark)
	}
	if _, err := s.PendingCustomVerificationRequest(ctx, verifier.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("pending after approve err = %v, want ErrCustomVerificationRequestNotFound", err)
	}

	// Re-issuing the decision that already holds moves nothing and does not run
	// the callback a second time.
	repeat, changed, err := s.DecideCustomVerificationRequest(ctx, approved.ID, approved.Version,
		domain.CustomVerificationApproved, "someone else", "again", "",
		func(context.Context, domain.CustomVerificationRequest) error {
			t.Error("apply ran for a decision that already held")
			return nil
		})
	if err != nil || changed {
		t.Fatalf("repeated approve: changed=%v err=%v", changed, err)
	}
	if repeat.Version != approved.Version || repeat.DecidedBy != "operator" {
		t.Fatalf("repeated approve mutated the row: %+v", repeat)
	}

	// approved -> rejected is not in the status machine; approved -> revoked is.
	if _, _, err := s.DecideCustomVerificationRequest(ctx, approved.ID, approved.Version,
		domain.CustomVerificationRejected, "operator", "changed my mind", "", nil); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("approved -> rejected err = %v, want ErrCustomVerificationRequestInvalid", err)
	}
	revoked, changed, err := s.DecideCustomVerificationRequest(ctx, approved.ID, approved.Version,
		domain.CustomVerificationRevoked, "operator", "brand asked us to", "",
		botVerificationTxRevoke())
	if err != nil || !changed {
		t.Fatalf("revoke: changed=%v err=%v", changed, err)
	}
	// The stamps are paired with the status, so leaving approved clears approved_at.
	if revoked.Status != domain.CustomVerificationRevoked ||
		revoked.Version != approved.Version+1 || !revoked.ApprovedAt.IsZero() ||
		!revoked.RejectedAt.IsZero() {
		t.Fatalf("revoked application = %+v", revoked)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection after revoke err = %v, want ErrCustomVerificationNotFound", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, revoked.ID, revoked.Version,
		domain.CustomVerificationApproved, "operator", "", "", grant); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("revoked -> approved err = %v, want ErrCustomVerificationRequestInvalid", err)
	}

	// A rejection needs a reason, and the domain is what says so.
	second, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(verifier.BotID, applicant, other, "AcmeCEO"))
	if err != nil {
		t.Fatalf("file second application: %v", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, second.ID, second.Version,
		domain.CustomVerificationRejected, "operator", "   ", "", nil); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("reject without a reason err = %v, want ErrVerificationReasonRequired", err)
	}
	if stillPending, err := s.CustomVerificationRequest(ctx, second.ID); err != nil ||
		stillPending.Status != domain.CustomVerificationPending ||
		stillPending.Version != second.Version {
		t.Fatalf("application after refused rejection = %+v err=%v", stillPending, err)
	}
	rejected, changed, err := s.DecideCustomVerificationRequest(ctx, second.ID, second.Version,
		domain.CustomVerificationRejected, "operator", "not a public figure", "", nil)
	if err != nil || !changed {
		t.Fatalf("reject: changed=%v err=%v", changed, err)
	}
	if rejected.Status != domain.CustomVerificationRejected || rejected.RejectedAt.IsZero() ||
		!rejected.ApprovedAt.IsZero() {
		t.Fatalf("rejected application = %+v", rejected)
	}
	if _, err := s.CustomVerification(ctx, verifier.BotID, other); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("rejection granted a mark: %v", err)
	}
	// A decided pair is free again: history keeps the rejection.
	reapplied, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(verifier.BotID, applicant, other, "AcmeCEO"))
	if err != nil {
		t.Fatalf("re-apply after rejection: %v", err)
	}

	counts, err := s.CustomVerificationRequestCounts(ctx)
	if err != nil {
		t.Fatalf("queue counts: %v", err)
	}
	for status, want := range map[domain.CustomVerificationRequestStatus]int64{
		domain.CustomVerificationPending:  1,
		domain.CustomVerificationRejected: 1,
		domain.CustomVerificationRevoked:  1,
		domain.CustomVerificationApproved: 0,
	} {
		if got := counts[status] - baseline[status]; got != want {
			t.Fatalf("queue count delta for %s = %d, want %d", status, got, want)
		}
	}

	listed, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID,
	})
	if err != nil || len(listed) != 3 || listed[0].ID != reapplied.ID {
		t.Fatalf("queue list = %+v err=%v", listed, err)
	}
	pendingOnly, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID,
		Statuses:      []domain.CustomVerificationRequestStatus{domain.CustomVerificationPending},
	})
	if err != nil || len(pendingOnly) != 1 || pendingOnly[0].ID != reapplied.ID {
		t.Fatalf("pending queue = %+v err=%v", pendingOnly, err)
	}
	page, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID, Limit: 2,
	})
	if err != nil || len(page) != 2 {
		t.Fatalf("queue page = %+v err=%v", page, err)
	}
	next, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID, Limit: 2, BeforeID: page[len(page)-1].ID,
	})
	if err != nil || len(next) != 1 || next[0].ID >= page[len(page)-1].ID {
		t.Fatalf("queue keyset page = %+v err=%v", next, err)
	}
	byUsername, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID, Query: "@acmec",
	})
	if err != nil || len(byUsername) != 2 {
		t.Fatalf("username query = %+v err=%v", byUsername, err)
	}
	byPeerID, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID, Query: fmt.Sprintf("%d", peer.ID),
	})
	if err != nil || len(byPeerID) != 1 || byPeerID[0].ID != revoked.ID {
		t.Fatalf("numeric query = %+v err=%v", byPeerID, err)
	}
	channelsOnly, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		VerifierBotID: verifier.BotID, PeerType: domain.PeerTypeChannel,
	})
	if err != nil || len(channelsOnly) != 1 || channelsOnly[0].ID != revoked.ID {
		t.Fatalf("channel queue = %+v err=%v", channelsOnly, err)
	}
	if _, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		Statuses: []domain.CustomVerificationRequestStatus{"nonsense"},
	}); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("bad status filter err = %v, want ErrCustomVerificationRequestInvalid", err)
	}

	history, err := s.CustomVerificationRequestsForApplicant(ctx, applicant, 0)
	if err != nil || len(history) != 3 || history[0].ID != reapplied.ID {
		t.Fatalf("applicant history = %+v err=%v", history, err)
	}
	if empty, err := s.CustomVerificationRequestsForApplicant(ctx, target, 0); err != nil ||
		len(empty) != 0 {
		t.Fatalf("other applicant history = %+v err=%v", empty, err)
	}
	if _, err := s.CustomVerificationRequestsForApplicant(ctx, 0, 0); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("history for applicant 0 err = %v, want ErrCustomVerificationRequestInvalid", err)
	}
	if _, err := s.CustomVerificationRequest(ctx, reapplied.ID+1<<40); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("unknown application err = %v, want ErrCustomVerificationRequestNotFound", err)
	}
}

// TestBotVerificationDescriptionsAcceptEmojiPostgres pins the app-configured
// 70-rune custom-description limit against UTF-8 byte constraints.
func TestBotVerificationDescriptionsAcceptEmojiPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewBotVerificationStore(pool)
	bot := botVerificationTestUser(t, pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: botVerificationTestUser(t, pool)}
	icon := botVerificationTestIcon(t, pool, s, "emoji icon", 0)

	emoji := strings.Repeat("🛡", domain.MaxCustomVerificationDescriptionLength)
	if utf8.RuneCountInString(emoji) > domain.MaxCustomVerificationDescriptionLength {
		t.Fatalf("test description is %d runes, over the domain limit", utf8.RuneCountInString(emoji))
	}
	if len(emoji) != 4*domain.MaxCustomVerificationDescriptionLength {
		t.Fatalf("test description is %d bytes, want UTF-8 worst case", len(emoji))
	}

	settings, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, IconDocumentID: icon.DocumentID, CompanyName: "Acme",
		DefaultDescription: emoji, Enabled: true, CanModifyCustomDescription: true,
	})
	if err != nil {
		t.Fatalf("verifier settings with an emoji description: %v", err)
	}
	if settings.DefaultDescription != emoji {
		t.Fatalf("stored default description was altered: %d runes back, %d in",
			utf8.RuneCountInString(settings.DefaultDescription), utf8.RuneCountInString(emoji))
	}

	// The granted mark carries the same text, so widening only the settings column
	// would move the failure one step later instead of fixing it.
	mark, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: bot, Peer: peer, IconDocumentID: icon.DocumentID, Description: emoji,
	})
	if err != nil {
		t.Fatalf("grant with an emoji description: %v", err)
	}
	if mark.Description != emoji {
		t.Fatalf("stored mark description was altered: %d runes back, %d in",
			utf8.RuneCountInString(mark.Description), utf8.RuneCountInString(emoji))
	}

	// And so does an application, which is where an applicant types one.
	if _, err := s.CreateCustomVerificationRequest(ctx, domain.CustomVerificationRequest{
		VerifierBotID: bot, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: botVerificationTestUser(t, pool)},
		ApplicantUserID: bot, RequestedDescription: emoji,
	}); err != nil {
		t.Fatalf("application with an emoji description: %v", err)
	}

	// Past the rune limit it is still a domain error, not a constraint error: the
	// bound moved, it did not disappear.
	tooLong := strings.Repeat("🛡", domain.MaxCustomVerificationDescriptionLength+1)
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: bot, IconDocumentID: icon.DocumentID, CompanyName: "Acme",
		DefaultDescription: tooLong, Version: settings.Version,
	}); !errors.Is(err, domain.ErrVerifierSettingsInvalid) {
		t.Fatalf("over-long description err = %v, want ErrVerifierSettingsInvalid", err)
	}
}
