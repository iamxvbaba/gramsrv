package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestBusinessStoresRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := randomSuffix(t)
	var authID [8]byte
	var authBody [256]byte
	if _, err := rand.Read(authID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authBody[:]); err != nil {
		t.Fatal(err)
	}
	authExpiry := int(time.Now().Add(time.Hour).Unix())
	keys := NewAuthKeyStore(pool)
	if err := keys.Save(ctx, store.AuthKeyData{
		ID:         authID,
		Value:      authBody,
		ServerSalt: 42,
		ExpiresAt:  authExpiry,
	}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_ = keys.Delete(ctx, authID)
	})
	var permAuthID [8]byte
	var permAuthBody [256]byte
	if _, err := rand.Read(permAuthID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(permAuthBody[:]); err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: permAuthID, Value: permAuthBody}); err != nil {
		t.Fatalf("save permanent auth key: %v", err)
	}
	t.Cleanup(func() {
		_ = keys.Delete(ctx, permAuthID)
	})

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 1,
		Phone:      "+1555" + suffix + "01",
		FirstName:  "Owner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if owner.ID < domain.UserIDSequenceBase {
		t.Fatalf("owner id = %d, want >= base %d", owner.ID, domain.UserIDSequenceBase)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 2,
		Phone:      "+1555" + suffix + "02",
		FirstName:  "Friend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})
	username := "owner_" + suffix
	owner, err = users.UpdateUsername(ctx, owner.ID, username)
	if err != nil {
		t.Fatalf("update owner username: %v", err)
	}
	if owner.Username != username {
		t.Fatalf("owner username = %q, want %q", owner.Username, username)
	}
	byUsername, found, err := users.ByUsername(ctx, strings.ToUpper(username))
	if err != nil || !found || byUsername.ID != owner.ID {
		t.Fatalf("by username = user %+v found %v err %v, want owner", byUsername, found, err)
	}
	if _, err := users.UpdateUsername(ctx, friend.ID, strings.ToUpper(username)); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("duplicate username err = %v, want username occupied", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO contacts (user_id, contact_user_id, mutual)
		VALUES ($1, $2, true)
	`, owner.ID, friend.ID); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	contacts, err := NewContactStore(pool).ListByUser(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(contacts.Contacts) != 1 || contacts.Contacts[0].User.ID != friend.ID || !contacts.Contacts[0].Mutual {
		t.Fatalf("contacts = %+v, want friend mutual contact", contacts)
	}
	if contacts.Hash == 0 {
		t.Fatal("contacts hash is zero for non-empty contact list")
	}
	search, err := users.Search(ctx, owner.ID, "Friend", "", 10)
	if err != nil {
		t.Fatalf("search users: %v", err)
	}
	if len(search.MyResults) != 1 || search.MyResults[0].ID != friend.ID || !search.MyResults[0].Contact {
		t.Fatalf("search contacts = %+v, want friend in my results", search)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO dialogs (user_id, peer_type, peer_id, top_message_id, unread_count, pinned)
		VALUES ($1, 'user', $2, 10, 2, true)
	`, owner.ID, friend.ID); err != nil {
		t.Fatalf("insert dialog: %v", err)
	}
	dialogs, err := NewDialogStore(pool).ListByUser(ctx, owner.ID, domain.DialogFilter{PinnedOnly: true, Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].Peer.ID != friend.ID || !dialogs.Dialogs[0].Pinned {
		t.Fatalf("dialogs = %+v, want pinned friend dialog", dialogs)
	}
	emptyDialogsPage, err := NewDialogStore(pool).ListByUser(ctx, owner.ID, domain.DialogFilter{
		PinnedOnly:    true,
		OffsetID:      dialogs.Dialogs[0].TopMessage,
		HasOffsetPeer: true,
		OffsetPeer:    dialogs.Dialogs[0].Peer,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("list empty dialogs page: %v", err)
	}
	if len(emptyDialogsPage.Dialogs) != 0 || emptyDialogsPage.Count != dialogs.Count || emptyDialogsPage.Hash != dialogs.Hash {
		t.Fatalf("empty dialogs page = %+v, want empty rows with full count/hash from %+v", emptyDialogsPage, dialogs)
	}
	peerDialogs, err := NewDialogStore(pool).ListByPeers(ctx, owner.ID, []domain.Peer{
		{Type: domain.PeerTypeUser, ID: friend.ID},
		{Type: domain.PeerTypeUser, ID: owner.ID},
	})
	if err != nil {
		t.Fatalf("list peer dialogs: %v", err)
	}
	if len(peerDialogs.Dialogs) != 2 || peerDialogs.Dialogs[0].Peer.ID != friend.ID || peerDialogs.Dialogs[0].TopMessage != 10 {
		t.Fatalf("peer dialogs = %+v, want friend dialog plus owner placeholder", peerDialogs)
	}
	if peerDialogs.Dialogs[1].Peer.ID != owner.ID || peerDialogs.Dialogs[1].TopMessage != 0 {
		t.Fatalf("placeholder dialog = %+v, want owner empty dialog", peerDialogs.Dialogs[1])
	}
	if len(peerDialogs.Users) != 2 {
		t.Fatalf("peer dialog users = %+v, want both requested users", peerDialogs.Users)
	}

	wantState := domain.UpdateState{Pts: 11, Qts: 12, Date: 13, Seq: 14}
	states := NewUpdateStateStore(pool)
	if err := states.Save(ctx, authID, owner.ID, wantState); err != nil {
		t.Fatalf("save update state: %v", err)
	}
	gotState, found, err := states.Get(ctx, authID, owner.ID)
	if err != nil {
		t.Fatalf("get update state: %v", err)
	}
	if !found || gotState != wantState {
		t.Fatalf("update state = %+v found=%v, want %+v found=true", gotState, found, wantState)
	}
	if err := states.Delete(ctx, authID, owner.ID); err != nil {
		t.Fatalf("delete update state: %v", err)
	}
	if _, found, err := states.Get(ctx, authID, owner.ID); err != nil || found {
		t.Fatalf("state after delete found=%v err=%v, want not found", found, err)
	}

	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID:    authID,
		PermAuthKeyID:    authKeyIDToInt64(permAuthID),
		Nonce:            67890,
		TempSessionID:    24680,
		ExpiresAt:        authExpiry,
		EncryptedMessage: []byte("binding"),
	}); err != nil {
		t.Fatalf("save temp auth key binding: %v", err)
	}
	var bindingCount int
	var tempSessionID int64
	if err := pool.QueryRow(ctx, "SELECT count(*), coalesce(max(temp_session_id), 0) FROM temp_auth_key_bindings WHERE temp_auth_key_id = $1", authKeyIDToInt64(authID)).Scan(&bindingCount, &tempSessionID); err != nil {
		t.Fatalf("count temp auth key binding: %v", err)
	}
	if bindingCount != 1 || tempSessionID != 24680 {
		t.Fatalf("temp auth key binding count/session = %d/%d, want 1/24680", bindingCount, tempSessionID)
	}

	passwords := NewPasswordStore(pool)
	wantPassword := domain.PasswordSettings{
		Hint:         "dev",
		SecureRandom: []byte("secure-random"),
	}
	if err := passwords.Save(ctx, owner.ID, wantPassword); err != nil {
		t.Fatalf("save password settings: %v", err)
	}
	gotPassword, found, err := passwords.GetByUser(ctx, owner.ID)
	if err != nil {
		t.Fatalf("get password settings: %v", err)
	}
	if !found || gotPassword.Hint != wantPassword.Hint || string(gotPassword.SecureRandom) != string(wantPassword.SecureRandom) {
		t.Fatalf("password settings = %+v found=%v, want %+v found=true", gotPassword, found, wantPassword)
	}

	help := NewHelpStore(pool)
	client := "tdesktop-test-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM app_configs WHERE client = $1", client)
	})
	if err := help.UpsertAppConfig(ctx, domain.AppConfig{Client: client, Hash: 9, JSON: []byte(`{"test":true}`)}); err != nil {
		t.Fatalf("upsert app config: %v", err)
	}
	cfg, found, err := help.GetAppConfig(ctx, client)
	if err != nil {
		t.Fatalf("get app config: %v", err)
	}
	if !found || cfg.Hash != 9 || string(cfg.JSON) != `{"test": true}` {
		t.Fatalf("app config = %+v found=%v, want hash=9 json", cfg, found)
	}
	countryISO := "T" + suffix[:1]
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM countries WHERE iso2 = $1", countryISO)
	})
	if err := help.UpsertCountries(ctx, []domain.Country{{
		ISO2:        countryISO,
		DefaultName: "Testland",
		CountryCodes: []domain.CountryCode{
			{CountryCode: "999", Prefixes: []string{"999"}, Patterns: []string{"XXX"}},
		},
	}}); err != nil {
		t.Fatalf("upsert countries: %v", err)
	}
	countryList, err := help.ListCountries(ctx, "en")
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	var foundCountry bool
	for _, country := range countryList.Countries {
		if country.ISO2 == countryISO && len(country.CountryCodes) == 1 && country.CountryCodes[0].CountryCode == "999" {
			foundCountry = true
		}
	}
	if !foundCountry {
		t.Fatalf("country %s not found in %+v", countryISO, countryList)
	}

	langCode := "test-" + suffix
	lang := NewLangPackStore(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM lang_packs WHERE lang_pack = $1 AND lang_code = $2", "tdesktop", langCode)
	})
	if err := lang.UpsertPack(ctx, domain.LangPack{
		LangPack: "tdesktop",
		LangCode: langCode,
		Version:  7,
		Strings: []domain.LangPackString{
			{Key: "lng_test", Value: "Test"},
			{Key: "lng_items", Pluralized: true, OneValue: "{count} item", OtherValue: "{count} items"},
		},
	}); err != nil {
		t.Fatalf("upsert lang pack: %v", err)
	}
	pack, err := lang.GetPack(ctx, "tdesktop", langCode, 0)
	if err != nil {
		t.Fatalf("get lang pack: %v", err)
	}
	if pack.Version != 7 || len(pack.Strings) != 2 {
		t.Fatalf("lang pack = %+v, want version 7 with 2 strings", pack)
	}
	notModified, err := lang.GetPack(ctx, "tdesktop", langCode, 7)
	if err != nil {
		t.Fatalf("get lang pack not modified: %v", err)
	}
	if notModified.Version != 7 || len(notModified.Strings) != 0 {
		t.Fatalf("not modified pack = %+v, want version 7 with no strings", notModified)
	}
	selected, err := lang.GetStrings(ctx, "tdesktop", langCode, []string{"lng_test"})
	if err != nil {
		t.Fatalf("get lang pack strings: %v", err)
	}
	if len(selected.Strings) != 1 || selected.Strings[0].Value != "Test" {
		t.Fatalf("selected strings = %+v, want lng_test=Test", selected.Strings)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s", hex.EncodeToString(b[:]))
}
