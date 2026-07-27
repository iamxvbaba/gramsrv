package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func botVerificationUserPeer(id int64) domain.Peer {
	return domain.Peer{Type: domain.PeerTypeUser, ID: id}
}

func botVerificationChannelPeer(id int64) domain.Peer {
	return domain.Peer{Type: domain.PeerTypeChannel, ID: id}
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

// botVerificationTestRequest is an application that clears domain validation.
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

// TestBotVerificationIconCatalogueMemory covers the catalogue: an entry is keyed
// by document id, retiring one keeps it readable, and the listing pages newest
// first with the activeOnly filter honoured.
func TestBotVerificationIconCatalogueMemory(t *testing.T) {
	ctx := context.Background()
	s := NewBotVerificationStore()

	first, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{
		DocumentID: 5001, Name: "  Blue check  ", Active: true,
	})
	if err != nil {
		t.Fatalf("upsert icon: %v", err)
	}
	if first.ID == 0 || first.Name != "Blue check" || !first.Active || first.OwnerBotID != 0 {
		t.Fatalf("stored icon = %+v", first)
	}
	if first.CreatedAt.IsZero() || first.UpdatedAt.Before(first.CreatedAt) {
		t.Fatalf("icon timestamps = %v / %v", first.CreatedAt, first.UpdatedAt)
	}

	second, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{
		DocumentID: 5002, OwnerBotID: 777, Name: "Reserved", Active: true,
	})
	if err != nil {
		t.Fatalf("upsert second icon: %v", err)
	}

	// document_id is the identity of an entry: the second upsert edits in place.
	edited, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{
		DocumentID: 5001, OwnerBotID: 42, Name: "Blue check v2", Active: true,
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

	if _, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{DocumentID: 0, Name: "bad"}); !errors.Is(err, domain.ErrVerificationIconInvalid) {
		t.Fatalf("upsert without document err = %v, want ErrVerificationIconInvalid", err)
	}
	if _, err := s.UpsertVerificationIcon(ctx, domain.VerificationIcon{DocumentID: 9, Name: "   "}); !errors.Is(err, domain.ErrVerificationIconInvalid) {
		t.Fatalf("upsert without name err = %v, want ErrVerificationIconInvalid", err)
	}

	retired, err := s.SetVerificationIconActive(ctx, second.ID, false)
	if err != nil {
		t.Fatalf("retire icon: %v", err)
	}
	if retired.Active || retired.ID != second.ID {
		t.Fatalf("retired icon = %+v", retired)
	}
	if _, err := s.SetVerificationIconActive(ctx, second.ID+1000, false); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("retire unknown err = %v, want ErrVerificationIconNotFound", err)
	}

	byDocument, err := s.VerificationIconByDocument(ctx, 5002)
	if err != nil {
		t.Fatalf("read icon by document: %v", err)
	}
	if byDocument.ID != second.ID || byDocument.Active {
		t.Fatalf("icon by document = %+v", byDocument)
	}
	if _, err := s.VerificationIconByDocument(ctx, 999999); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("unknown document err = %v, want ErrVerificationIconNotFound", err)
	}
	byID, err := s.VerificationIcon(ctx, first.ID)
	if err != nil || byID.DocumentID != 5001 {
		t.Fatalf("read icon by id = %+v err=%v", byID, err)
	}
	if _, err := s.VerificationIcon(ctx, 0); !errors.Is(err, domain.ErrVerificationIconNotFound) {
		t.Fatalf("icon id 0 err = %v, want ErrVerificationIconNotFound", err)
	}

	all, err := s.ListVerificationIcons(ctx, false, 0)
	if err != nil {
		t.Fatalf("list icons: %v", err)
	}
	if len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("catalogue order = %+v, want newest first", all)
	}
	active, err := s.ListVerificationIcons(ctx, true, 0)
	if err != nil {
		t.Fatalf("list active icons: %v", err)
	}
	if len(active) != 1 || active[0].ID != first.ID {
		t.Fatalf("active catalogue = %+v", active)
	}
	if page, err := s.ListVerificationIcons(ctx, false, 1); err != nil || len(page) != 1 {
		t.Fatalf("icon page = %+v err=%v", page, err)
	}
}

// TestBotVerifierSettingsLifecycleMemory covers verifier status: optimistic
// locking on version, the idempotent kill switch and the cascade that takes the
// marks with the verifier row.
func TestBotVerifierSettingsLifecycleMemory(t *testing.T) {
	ctx := context.Background()
	s := NewBotVerificationStore()

	if _, err := s.BotVerifierSettings(ctx, 4242); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("unknown verifier err = %v, want ErrVerifierNotFound", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: 4242, IconDocumentID: 7, CompanyName: "Acme", Version: 3,
	}); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("versioned upsert of missing row err = %v, want ErrVerifierNotFound", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: 4242, IconDocumentID: 0, CompanyName: "Acme",
	}); !errors.Is(err, domain.ErrVerifierSettingsInvalid) {
		t.Fatalf("iconless upsert err = %v, want ErrVerifierSettingsInvalid", err)
	}

	created := botVerificationTestVerifier(t, s, 4242, 5001)
	if created.Version != 1 || !created.Enabled || created.CompanyName != "Verifier 4242" {
		t.Fatalf("created verifier = %+v", created)
	}

	// Version 0 means "there is no row yet", so it loses against the stored row.
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: 4242, IconDocumentID: 5001, CompanyName: "Acme",
	}); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("re-create err = %v, want ErrCustomVerificationVersionConflict", err)
	}
	if _, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID: 4242, IconDocumentID: 5001, CompanyName: "Acme", Version: 99,
	}); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("stale upsert err = %v, want ErrCustomVerificationVersionConflict", err)
	}

	edited, err := s.UpsertBotVerifierSettings(ctx, domain.BotVerifierSettings{
		BotID:              4242,
		IconDocumentID:     5002,
		CompanyName:        "Acme Media",
		DefaultDescription: "checked by Acme",
		Enabled:            true,
		Version:            created.Version,
	})
	if err != nil {
		t.Fatalf("edit verifier: %v", err)
	}
	if edited.Version != 2 || edited.IconDocumentID != 5002 || edited.CanModifyCustomDescription {
		t.Fatalf("edited verifier = %+v", edited)
	}
	if !edited.CreatedAt.Equal(created.CreatedAt) || !edited.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("edited timestamps = %v / %v", edited.CreatedAt, edited.UpdatedAt)
	}

	disabled, err := s.SetBotVerifierEnabled(ctx, 4242, false)
	if err != nil {
		t.Fatalf("disable verifier: %v", err)
	}
	if disabled.Enabled || disabled.Version != edited.Version+1 {
		t.Fatalf("disabled verifier = %+v", disabled)
	}
	again, err := s.SetBotVerifierEnabled(ctx, 4242, false)
	if err != nil {
		t.Fatalf("re-disable verifier: %v", err)
	}
	if again.Version != disabled.Version {
		t.Fatalf("re-disable bumped version to %d", again.Version)
	}
	if _, err := s.SetBotVerifierEnabled(ctx, 777777, false); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("disable unknown err = %v, want ErrVerifierNotFound", err)
	}

	// A disabled verifier is still readable: the admin panel renders the switch.
	stored, err := s.BotVerifierSettings(ctx, 4242)
	if err != nil || stored.Enabled {
		t.Fatalf("read disabled verifier = %+v err=%v", stored, err)
	}

	other := botVerificationTestVerifier(t, s, 1042, 5001)
	batch, err := s.BotVerifierSettingsBatch(ctx, []int64{4242, 1042, 999999, 0})
	if err != nil {
		t.Fatalf("batch verifiers: %v", err)
	}
	if len(batch) != 2 || batch[4242].Enabled || !batch[1042].Enabled {
		t.Fatalf("verifier batch = %+v", batch)
	}
	if _, absent := batch[999999]; absent {
		t.Fatal("batch invented a verifier")
	}

	listed, err := s.ListBotVerifiers(ctx, false, 0)
	if err != nil {
		t.Fatalf("list verifiers: %v", err)
	}
	if len(listed) != 2 || listed[0].BotID != other.BotID || listed[1].BotID != 4242 {
		t.Fatalf("verifier list = %+v, want bot id order", listed)
	}
	enabledOnly, err := s.ListBotVerifiers(ctx, true, 0)
	if err != nil {
		t.Fatalf("list enabled verifiers: %v", err)
	}
	if len(enabledOnly) != 1 || enabledOnly[0].BotID != other.BotID {
		t.Fatalf("enabled verifier list = %+v", enabledOnly)
	}

	// Marks cascade with the verifier row; applications do not.
	peer := botVerificationChannelPeer(9001)
	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: other.BotID, Peer: peer, Description: "cascade me",
	}); err != nil {
		t.Fatalf("grant before delete: %v", err)
	}
	req, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(other.BotID, 31337, peer, "CascadeChannel"))
	if err != nil {
		t.Fatalf("create request before delete: %v", err)
	}
	removed, err := s.DeleteBotVerifierSettings(ctx, other.BotID)
	if err != nil || !removed {
		t.Fatalf("delete verifier: removed=%v err=%v", removed, err)
	}
	if removed, err := s.DeleteBotVerifierSettings(ctx, other.BotID); err != nil || removed {
		t.Fatalf("repeated delete: removed=%v err=%v", removed, err)
	}
	if _, err := s.CustomVerification(ctx, other.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("mark after cascade err = %v, want ErrCustomVerificationNotFound", err)
	}
	if count, err := s.CountCustomVerifications(ctx, other.BotID); err != nil || count != 0 {
		t.Fatalf("mark count after cascade = %d err=%v", count, err)
	}
	if kept, err := s.CustomVerificationRequest(ctx, req.ID); err != nil || kept.ID != req.ID {
		t.Fatalf("application after cascade = %+v err=%v", kept, err)
	}
}

// TestCustomVerificationGrantAndProjectionMemory is the projection contract:
// exactly one mark exists per peer, a later verifier replaces the former one,
// a disabled verifier projects nothing while its row survives, and a repeated
// grant updates the mark in place.
func TestCustomVerificationGrantAndProjectionMemory(t *testing.T) {
	ctx := context.Background()
	s := NewBotVerificationStore()
	alpha := botVerificationTestVerifier(t, s, 101, 5001)
	beta := botVerificationTestVerifier(t, s, 102, 5002)
	peer := botVerificationUserPeer(7001)

	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: 999, Peer: peer,
	}); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("grant by non-verifier err = %v, want ErrVerifierNotFound", err)
	}
	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: domain.Peer{Type: domain.PeerTypeCommunity, ID: 5},
	}); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
		t.Fatalf("grant on community err = %v, want ErrCustomVerificationTargetInvalid", err)
	}

	// The icon is denormalised from the verifier when the caller leaves it unset.
	alphaMark, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: peer, Description: "checked by alpha",
		GrantedByUserID: 555,
	})
	if err != nil || !created {
		t.Fatalf("grant alpha mark: created=%v err=%v", created, err)
	}
	if alphaMark.IconDocumentID != alpha.IconDocumentID || alphaMark.Version != 1 {
		t.Fatalf("alpha mark = %+v", alphaMark)
	}
	if alphaMark.CreatedAt.IsZero() || !alphaMark.UpdatedAt.Equal(alphaMark.CreatedAt) {
		t.Fatalf("alpha mark timestamps = %v / %v", alphaMark.CreatedAt, alphaMark.UpdatedAt)
	}

	if got, err := s.PeerVerification(ctx, peer); err != nil || got.ID != alphaMark.ID {
		t.Fatalf("projection with one mark = %+v err=%v", got, err)
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
	if count, err := s.CountCustomVerifications(ctx, alpha.BotID); err != nil || count != 0 {
		t.Fatalf("alpha mark count after replacement = %d err=%v", count, err)
	}

	// One peer has one wire-visible mark, irrespective of how often it is read.
	for i := 0; i < 3; i++ {
		got, err := s.PeerVerification(ctx, peer)
		if err != nil {
			t.Fatalf("projection after replacement: %v", err)
		}
		if got.ID != betaMark.ID || got.VerifierBotID != beta.BotID {
			t.Fatalf("projection = %+v, want replacement mark %d", got, betaMark.ID)
		}
		if got.Projection().Icon != beta.IconDocumentID {
			t.Fatalf("projected icon = %d, want %d", got.Projection().Icon, beta.IconDocumentID)
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
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, true); err != nil {
		t.Fatalf("re-enable beta: %v", err)
	}
	if got, err := s.PeerVerification(ctx, peer); err != nil || got.ID != betaMark.ID {
		t.Fatalf("projection after re-enabling beta = %+v err=%v", got, err)
	}
	// Granting through alpha replaces beta's mark on the same peer.
	regranted, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: alpha.BotID, Peer: peer, IconDocumentID: 5099,
		Description: "checked by alpha, again",
	})
	if err != nil || !created {
		t.Fatalf("re-grant alpha mark: created=%v err=%v", created, err)
	}
	if regranted.ID != betaMark.ID || regranted.Version != betaMark.Version+1 {
		t.Fatalf("re-granted mark = %+v, want id %d v%d", regranted, betaMark.ID, betaMark.Version+1)
	}
	if regranted.IconDocumentID != 5099 || regranted.Description != "checked by alpha, again" {
		t.Fatalf("re-granted payload = %+v", regranted)
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

	// The batch form resolves several peers at once, with the same rules.
	second := botVerificationChannelPeer(7002)
	third := botVerificationUserPeer(7003)
	unmarked := botVerificationChannelPeer(7004)
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
	batch, err := s.PeerVerificationBatch(ctx,
		[]domain.Peer{peer, second, third, unmarked, peer, {Type: domain.PeerTypeUser, ID: 0}})
	if err != nil {
		t.Fatalf("batch projection: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("batch projection = %+v, want 3 peers", batch)
	}
	if batch[peer].ID != regranted.ID || batch[second].ID != secondMark.ID ||
		batch[third].ID != thirdMark.ID {
		t.Fatalf("batch projection picked %+v", batch)
	}
	if _, present := batch[unmarked]; present {
		t.Fatal("batch projected an unmarked peer")
	}
	if empty, err := s.PeerVerificationBatch(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch = %+v err=%v", empty, err)
	}

	// Disabling a verifier drops its peers from the batch too.
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, false); err != nil {
		t.Fatalf("disable beta again: %v", err)
	}
	batch, err = s.PeerVerificationBatch(ctx, []domain.Peer{peer, second, third})
	if err != nil {
		t.Fatalf("batch projection after disable: %v", err)
	}
	if len(batch) != 2 || batch[peer].ID != regranted.ID || batch[second].ID != secondMark.ID {
		t.Fatalf("batch after disable = %+v", batch)
	}
	if _, present := batch[third]; present {
		t.Fatal("disabled verifier still projects in the batch")
	}
	if _, err := s.SetBotVerifierEnabled(ctx, beta.BotID, true); err != nil {
		t.Fatalf("re-enable beta again: %v", err)
	}

	// Listing and paging over the marks.
	all, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list marks = %+v err=%v", all, err)
	}
	if all[0].ID != thirdMark.ID {
		t.Fatalf("mark list order = %+v, want newest first", all)
	}
	page, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("mark page = %+v err=%v", page, err)
	}
	next, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		Limit: 2, BeforeID: page[len(page)-1].ID,
	})
	if err != nil || len(next) != 1 || next[0].ID >= page[len(page)-1].ID {
		t.Fatalf("mark keyset page = %+v err=%v", next, err)
	}
	mine, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		VerifierBotID: alpha.BotID,
	})
	if err != nil || len(mine) != 2 {
		t.Fatalf("verifier-filtered marks = %+v err=%v", mine, err)
	}
	channels, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		PeerType: domain.PeerTypeChannel,
	})
	if err != nil || len(channels) != 1 || channels[0].Peer != second {
		t.Fatalf("channel-filtered marks = %+v err=%v", channels, err)
	}
	byPeer, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		PeerType: peer.Type, PeerID: peer.ID,
	})
	if err != nil || len(byPeer) != 1 {
		t.Fatalf("peer-filtered marks = %+v err=%v", byPeer, err)
	}
	byQuery, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{
		Query: fmt.Sprintf("%d", second.ID),
	})
	if err != nil || len(byQuery) != 1 || byQuery[0].ID != secondMark.ID {
		t.Fatalf("numeric mark query = %+v err=%v", byQuery, err)
	}
	byText, err := s.ListCustomVerifications(ctx, domain.CustomVerificationFilter{Query: "AGAIN"})
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

// TestCustomVerificationLimitMemory pins the per-verifier bound: a new mark is
// refused at the limit while an existing one can still be re-described.
func TestCustomVerificationLimitMemory(t *testing.T) {
	ctx := context.Background()
	s := NewBotVerificationStore()
	verifier := botVerificationTestVerifier(t, s, 303, 5001)

	for i := 1; i <= domain.MaxCustomVerificationsPerVerifier; i++ {
		if _, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
			VerifierBotID: verifier.BotID, Peer: botVerificationChannelPeer(int64(i)),
		}); err != nil || !created {
			t.Fatalf("grant %d: created=%v err=%v", i, created, err)
		}
	}
	if count, err := s.CountCustomVerifications(ctx, verifier.BotID); err != nil ||
		count != domain.MaxCustomVerificationsPerVerifier {
		t.Fatalf("mark count = %d err=%v", count, err)
	}
	if _, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationUserPeer(424242),
	}); !errors.Is(err, domain.ErrCustomVerificationLimit) {
		t.Fatalf("grant past the limit err = %v, want ErrCustomVerificationLimit", err)
	}
	// The bound is on creating marks, not on editing them.
	if _, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationChannelPeer(7),
		Description: "still editable at the limit",
	}); err != nil || created {
		t.Fatalf("re-grant at the limit: created=%v err=%v", created, err)
	}
	// Freeing one slot lets the next grant through.
	if revoked, err := s.RevokeCustomVerification(ctx, verifier.BotID,
		botVerificationChannelPeer(1)); err != nil || !revoked {
		t.Fatalf("free a slot: revoked=%v err=%v", revoked, err)
	}
	if _, created, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID: verifier.BotID, Peer: botVerificationUserPeer(424242),
	}); err != nil || !created {
		t.Fatalf("grant into the freed slot: created=%v err=%v", created, err)
	}
}

// TestCustomVerificationRequestQueueMemory covers the application queue: one
// pending application per (verifier, peer), the decision status machine, and the
// transaction that keeps an approved application and its mark together.
func TestCustomVerificationRequestQueueMemory(t *testing.T) {
	ctx := context.Background()
	s := NewBotVerificationStore()
	verifier := botVerificationTestVerifier(t, s, 501, 5001)
	peer := botVerificationChannelPeer(8001)
	applicant := int64(6001)

	grant := func(ctx context.Context, req domain.CustomVerificationRequest) error {
		_, _, err := s.GrantCustomVerification(ctx, domain.CustomVerification{
			VerifierBotID:   req.VerifierBotID,
			Peer:            req.Peer,
			Description:     req.RequestedDescription,
			GrantedByUserID: req.ApplicantUserID,
		})
		return err
	}
	revoke := func(ctx context.Context, req domain.CustomVerificationRequest) error {
		_, err := s.RevokeCustomVerification(ctx, req.VerifierBotID, req.Peer)
		return err
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
	// rune limit, so valid multi-byte text must behave the same in both stores.
	wideStore := NewBotVerificationStore()
	wideVerifier := botVerificationTestVerifier(t, wideStore, 909, 5001)
	wide := botVerificationTestRequest(wideVerifier.BotID, applicant, botVerificationUserPeer(8009), "Wide")
	wide.Reason = strings.Repeat("é", domain.MaxCustomVerificationReasonLength-1)
	if _, err := wideStore.CreateCustomVerificationRequest(ctx, wide); err != nil {
		t.Fatalf("valid multi-byte reason: %v", err)
	}
	pending, err := s.PendingCustomVerificationRequest(ctx, verifier.BotID, peer)
	if err != nil || pending.ID != filed.ID {
		t.Fatalf("pending application = %+v err=%v", pending, err)
	}

	// A failing callback rolls the whole decision back, including what the
	// callback itself wrote before it failed.
	boom := errors.New("apply exploded")
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
	if rolledBack.Status != domain.CustomVerificationPending || rolledBack.Version != filed.Version {
		t.Fatalf("application after rollback = %s v%d, want pending v%d",
			rolledBack.Status, rolledBack.Version, filed.Version)
	}
	if !rolledBack.ApprovedAt.IsZero() || rolledBack.DecidedBy != "" {
		t.Fatalf("application after rollback carries a decision: %+v", rolledBack)
	}
	if _, err := s.CustomVerification(ctx, verifier.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("mark after rollback err = %v, want ErrCustomVerificationNotFound", err)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection after rollback err = %v, want ErrCustomVerificationNotFound", err)
	}

	// Approving requires a callback: there is no "approved, mark later".
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
	if _, _, err := s.DecideCustomVerificationRequest(ctx, filed.ID+9000, filed.Version,
		domain.CustomVerificationApproved, "operator", "", "", grant); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("decision on unknown application err = %v, want ErrCustomVerificationRequestNotFound", err)
	}

	approved, changed, err := s.DecideCustomVerificationRequest(ctx, filed.ID, filed.Version,
		domain.CustomVerificationApproved, "operator", "brand confirmed", "ticket 12", grant)
	if err != nil || !changed {
		t.Fatalf("approve: changed=%v err=%v", changed, err)
	}
	if approved.Status != domain.CustomVerificationApproved || approved.Version != filed.Version+1 {
		t.Fatalf("approved application = %s v%d", approved.Status, approved.Version)
	}
	if approved.ApprovedAt.IsZero() || !approved.RejectedAt.IsZero() {
		t.Fatalf("approved stamps = %v / %v", approved.ApprovedAt, approved.RejectedAt)
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
		mark.IconDocumentID != verifier.IconDocumentID {
		t.Fatalf("mark after approve = %+v", mark)
	}
	if _, err := s.PendingCustomVerificationRequest(ctx, verifier.BotID, peer); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("pending after approve err = %v, want ErrCustomVerificationRequestNotFound", err)
	}

	// Re-issuing the decision that already holds moves nothing and does not apply
	// the callback a second time.
	repeat, changed, err := s.DecideCustomVerificationRequest(ctx, approved.ID, approved.Version,
		domain.CustomVerificationApproved, "someone else", "again", "", func(context.Context, domain.CustomVerificationRequest) error {
			t.Fatal("apply ran for a decision that already held")
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
		domain.CustomVerificationRevoked, "operator", "brand asked us to", "", revoke)
	if err != nil || !changed {
		t.Fatalf("revoke: changed=%v err=%v", changed, err)
	}
	if revoked.Status != domain.CustomVerificationRevoked || revoked.Version != approved.Version+1 {
		t.Fatalf("revoked application = %s v%d", revoked.Status, revoked.Version)
	}
	// The stamps are paired with the status, so leaving approved clears approved_at.
	if !revoked.ApprovedAt.IsZero() || !revoked.RejectedAt.IsZero() {
		t.Fatalf("revoked stamps = %v / %v", revoked.ApprovedAt, revoked.RejectedAt)
	}
	if _, err := s.PeerVerification(ctx, peer); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("projection after revoke err = %v, want ErrCustomVerificationNotFound", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, revoked.ID, revoked.Version,
		domain.CustomVerificationApproved, "operator", "", "", grant); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("revoked -> approved err = %v, want ErrCustomVerificationRequestInvalid", err)
	}

	// A rejection needs a reason, and the domain is what says so.
	other := botVerificationUserPeer(8002)
	second, err := s.CreateCustomVerificationRequest(ctx,
		botVerificationTestRequest(verifier.BotID, applicant, other, "AcmeCEO"))
	if err != nil {
		t.Fatalf("file second application: %v", err)
	}
	if _, _, err := s.DecideCustomVerificationRequest(ctx, second.ID, second.Version,
		domain.CustomVerificationRejected, "operator", "   ", "", nil); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("reject without a reason err = %v, want ErrVerificationReasonRequired", err)
	}
	stillPending, err := s.CustomVerificationRequest(ctx, second.ID)
	if err != nil || stillPending.Status != domain.CustomVerificationPending ||
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
	if counts[domain.CustomVerificationPending] != 1 ||
		counts[domain.CustomVerificationRejected] != 1 ||
		counts[domain.CustomVerificationRevoked] != 1 {
		t.Fatalf("queue counts = %+v", counts)
	}
	if _, present := counts[domain.CustomVerificationApproved]; present {
		t.Fatalf("queue counts invented an approved application: %+v", counts)
	}

	listed, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{})
	if err != nil || len(listed) != 3 {
		t.Fatalf("queue list = %+v err=%v", listed, err)
	}
	if listed[0].ID != reapplied.ID {
		t.Fatalf("queue order = %+v, want newest first", listed)
	}
	pendingOnly, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		Statuses: []domain.CustomVerificationRequestStatus{domain.CustomVerificationPending},
	})
	if err != nil || len(pendingOnly) != 1 || pendingOnly[0].ID != reapplied.ID {
		t.Fatalf("pending queue = %+v err=%v", pendingOnly, err)
	}
	page, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("queue page = %+v err=%v", page, err)
	}
	next, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		Limit: 2, BeforeID: page[len(page)-1].ID,
	})
	if err != nil || len(next) != 1 || next[0].ID >= page[len(page)-1].ID {
		t.Fatalf("queue keyset page = %+v err=%v", next, err)
	}
	byUsername, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		Query: "@acmec",
	})
	if err != nil || len(byUsername) != 2 {
		t.Fatalf("username query = %+v err=%v", byUsername, err)
	}
	byPeerID, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		Query: fmt.Sprintf("%d", peer.ID),
	})
	if err != nil || len(byPeerID) != 1 || byPeerID[0].ID != revoked.ID {
		t.Fatalf("numeric query = %+v err=%v", byPeerID, err)
	}
	channelsOnly, err := s.ListCustomVerificationRequests(ctx, domain.CustomVerificationRequestFilter{
		PeerType: domain.PeerTypeChannel, VerifierBotID: verifier.BotID,
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
	if empty, err := s.CustomVerificationRequestsForApplicant(ctx, applicant+1, 0); err != nil ||
		len(empty) != 0 {
		t.Fatalf("other applicant history = %+v err=%v", empty, err)
	}
	if _, err := s.CustomVerificationRequestsForApplicant(ctx, 0, 0); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("history for applicant 0 err = %v, want ErrCustomVerificationRequestInvalid", err)
	}
	if _, err := s.CustomVerificationRequest(ctx, reapplied.ID+9000); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("unknown application err = %v, want ErrCustomVerificationRequestNotFound", err)
	}
}
