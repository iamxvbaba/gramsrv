package redisstore

import (
	"context"
	"os"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestUserCacheRoundTrip(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx := context.Background()
	c, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	const userID int64 = 77000001
	cache := NewUserCache(c, time.Minute)
	t.Cleanup(func() { _ = c.Del(ctx, userBaseKey(userID), userBaseKey(userID+1)).Err() })

	want := domain.User{
		ID:                userID,
		AccessHash:        12345,
		Phone:             "15550000001",
		FirstName:         "Alice",
		LastName:          "Base",
		About:             "about",
		Username:          "alice_base",
		CountryCode:       "US",
		Verified:          true,
		Support:           true,
		LastSeenAt:        99,
		Contact:           true,
		PhotoID:           42,
		Birthday:          domain.Birthday{Day: 14, Month: 2, Year: 1990},
		PersonalChannelID: 555,
	}
	if err := cache.PutMany(ctx, []domain.User{want, want}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := cache.GetByIDs(ctx, []int64{userID, userID + 1, userID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	u, ok := got[userID]
	if !ok {
		t.Fatalf("cached user %d not found", userID)
	}
	if u.ID != want.ID || u.AccessHash != want.AccessHash || u.FirstName != want.FirstName || u.Username != want.Username || u.LastSeenAt != want.LastSeenAt {
		t.Fatalf("cached base mismatch: got %+v want %+v", u, want)
	}
	if u.Contact || u.PhotoID != 0 {
		t.Fatalf("viewer overlay leaked into base cache: %+v", u)
	}
	// birthday / personal channel must round-trip through the cache (losing them on the cache-hit path makes a just-saved value revert to zero).
	if u.Birthday != want.Birthday || u.PersonalChannelID != want.PersonalChannelID {
		t.Fatalf("birthday/personal channel lost in base cache round-trip: got birthday=%+v personal=%d", u.Birthday, u.PersonalChannelID)
	}
	if _, ok := got[userID+1]; ok {
		t.Fatalf("unexpected missing user hit: %+v", got[userID+1])
	}

	if err := c.Set(ctx, userBaseKey(userID+1), "{bad json", time.Minute).Err(); err != nil {
		t.Fatalf("set corrupt: %v", err)
	}
	got, err = cache.GetByIDs(ctx, []int64{userID + 1})
	if err != nil {
		t.Fatalf("get corrupt: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("corrupt cache returned users: %+v", got)
	}
	if n, err := c.Exists(ctx, userBaseKey(userID+1)).Result(); err != nil || n != 0 {
		t.Fatalf("corrupt key exists=%d err=%v, want deleted", n, err)
	}

	if err := cache.PutMany(ctx, []domain.User{{ID: userID + 1, AccessHash: 6789, FirstName: "WrongKey"}}); err != nil {
		t.Fatalf("put mismatched payload source: %v", err)
	}
	raw, err := c.Get(ctx, userBaseKey(userID+1)).Result()
	if err != nil {
		t.Fatalf("get raw mismatched source: %v", err)
	}
	if err := c.Set(ctx, userBaseKey(userID), raw, time.Minute).Err(); err != nil {
		t.Fatalf("set mismatched payload: %v", err)
	}
	got, err = cache.GetByIDs(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("get mismatched payload: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("mismatched payload returned users: %+v", got)
	}
	if n, err := c.Exists(ctx, userBaseKey(userID)).Result(); err != nil || n != 0 {
		t.Fatalf("mismatched key exists=%d err=%v, want deleted", n, err)
	}

	if err := cache.PutMany(ctx, []domain.User{want}); err != nil {
		t.Fatalf("restore user: %v", err)
	}
	if err := cache.Delete(ctx, []int64{userID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err = cache.GetByIDs(ctx, []int64{userID})
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cache hit after delete: %+v", got)
	}
}
