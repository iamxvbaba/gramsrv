package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ListSharedDeviceGroups is hand-written SQL grouping authorizations by a
// device fingerprint tuple, so the grouping logic (which accounts land in
// which group, and that a lone different device never leaks in) can only be
// proven against the real schema. Gated on TELESRV_TEST_POSTGRES_DSN like the
// rest of this package's integration tests.
func TestReadStoreSharedDeviceGroups(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)

	sharedDeviceModel := "Integration-Test-Device-" + suffix
	sharedSystemVersion := "Test OS 1.0"
	sharedPlatform := "test-platform"
	sharedIP := "203.0.113." + suffix[len(suffix)-2:]

	userA := 3_700_000_000 + time.Now().UnixNano()%1_000_000
	userB := userA + 1
	userC := userA + 2 // different device -- must never appear in the shared group.

	t.Cleanup(func() {
		for _, id := range []int64{userA, userB, userC} {
			_, _ = pool.Exec(ctx, `DELETE FROM authorizations WHERE user_id=$1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM auth_keys WHERE auth_key_id=$1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
		}
	})

	for i, id := range []int64{userA, userB, userC} {
		if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, is_bot, created_at, updated_at)
VALUES ($1, $2, $3, $4, '', $5, false, now(), now())`,
			id, id, fmt.Sprintf("+1890%s%d", suffix, i), fmt.Sprintf("Shared%d", i), fmt.Sprintf("shared%d%s", i, suffix)); err != nil {
			t.Fatalf("seed user %d: %v", id, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt)
VALUES ($1, decode(repeat('00', 256), 'hex'), 0)`, id); err != nil {
			t.Fatalf("seed auth key %d: %v", id, err)
		}
	}

	// userA and userB authorize from the same device fingerprint; userC from a
	// distinct one, so it must not be pulled into the shared group.
	if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (user_id, auth_key_id, device_model, system_version, platform, ip, created_at, active_at)
VALUES ($1, $1, $2, $3, $4, $5, now(), now())`, userA, sharedDeviceModel, sharedSystemVersion, sharedPlatform, sharedIP); err != nil {
		t.Fatalf("seed authorization A: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (user_id, auth_key_id, device_model, system_version, platform, ip, created_at, active_at)
VALUES ($1, $1, $2, $3, $4, $5, now(), now())`, userB, sharedDeviceModel, sharedSystemVersion, sharedPlatform, sharedIP); err != nil {
		t.Fatalf("seed authorization B: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (user_id, auth_key_id, device_model, system_version, platform, ip, created_at, active_at)
VALUES ($1, $1, $2, $3, $4, $5, now(), now())`, userC, "Other-Device-"+suffix, sharedSystemVersion, sharedPlatform, sharedIP+".other"); err != nil {
		t.Fatalf("seed authorization C: %v", err)
	}

	groups, _, err := store.ListSharedDeviceGroups(ctx, 0, 200)
	if err != nil {
		t.Fatalf("ListSharedDeviceGroups: %v", err)
	}

	var found *SharedDeviceGroup
	for i := range groups {
		if groups[i].DeviceModel == sharedDeviceModel {
			found = &groups[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("seeded device fingerprint %q not found among %d groups", sharedDeviceModel, len(groups))
	}
	if found.AccountCount != 2 {
		t.Fatalf("AccountCount = %d, want 2", found.AccountCount)
	}
	if found.SystemVersion != sharedSystemVersion || found.Platform != sharedPlatform || found.IP != sharedIP {
		t.Fatalf("group fingerprint = %+v, want system_version=%q platform=%q ip=%q", found, sharedSystemVersion, sharedPlatform, sharedIP)
	}
	if len(found.Accounts) != 2 {
		t.Fatalf("Accounts = %#v, want exactly 2 members", found.Accounts)
	}
	seen := map[int64]bool{}
	for _, acc := range found.Accounts {
		seen[acc.UserID] = true
		if acc.UserID == userC {
			t.Fatalf("userC (different device) leaked into the shared group: %#v", found.Accounts)
		}
	}
	if !seen[userA] || !seen[userB] {
		t.Fatalf("Accounts = %#v, want both userA=%d and userB=%d present", found.Accounts, userA, userB)
	}

	// A lone device (userC's) must never itself form a "shared" group.
	for i := range groups {
		if groups[i].DeviceModel == "Other-Device-"+suffix {
			t.Fatalf("a device with only one distinct account formed a group: %+v", groups[i])
		}
	}
}