package mtprotoedge

import (
	"testing"
	"time"
)

func TestRPCResultCacheRoundTripAndTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCache(func() time.Time { return now })

	var keyID [8]byte
	keyID[0] = 0xab
	encoded := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, typeID: 42, reqMsgID: 7}

	if _, ok := cache.Get(keyID, 5, 7); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	cache.Put(keyID, 5, 7, encoded)

	got, ok := cache.Get(keyID, 5, 7)
	if !ok {
		t.Fatal("expected hit")
	}
	// encodedOutboundMessage 不可变契约下 Get/Put 共享指针，不做防御性拷贝。
	if got != encoded {
		t.Fatal("expected shared pointer, got clone")
	}

	// 不同 session / msg_id 不串。
	if _, ok := cache.Get(keyID, 6, 7); ok {
		t.Fatal("hit with wrong session id")
	}
	if _, ok := cache.Get(keyID, 5, 8); ok {
		t.Fatal("hit with wrong msg id")
	}

	// TTL 过期。
	now = now.Add(rpcResultCacheTTL + time.Second)
	if _, ok := cache.Get(keyID, 5, 7); ok {
		t.Fatal("expected expiry after TTL")
	}
}

func TestRPCResultCacheShardTrim(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCache(func() time.Time { return now })

	var keyID [8]byte
	// 同一 (auth_key, session) 固定落在同一 shard；塞超过单 shard 条数上限，最旧的被逐出。
	perShard := rpcResultCacheMaxEntries / rpcResultCacheShards
	for i := 0; i < perShard+1; i++ {
		cache.Put(keyID, 1, int64(100+i), &encodedOutboundMessage{body: []byte{byte(i)}})
	}
	if _, ok := cache.Get(keyID, 1, 100); ok {
		t.Fatal("oldest entry should have been evicted by per-shard entry limit")
	}
	if _, ok := cache.Get(keyID, 1, int64(100+perShard)); !ok {
		t.Fatal("newest entry should survive")
	}

	// 单条超过单 shard 字节预算的结果不入缓存。
	huge := &encodedOutboundMessage{body: make([]byte, rpcResultCacheMaxBytes/rpcResultCacheShards+1)}
	cache.Put(keyID, 2, 999, huge)
	if _, ok := cache.Get(keyID, 2, 999); ok {
		t.Fatal("oversized entry should be rejected")
	}
}
