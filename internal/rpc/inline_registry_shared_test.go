package rpc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestInlineRegistrySharedResolveUnblocksRemoteWaitAndSend(t *testing.T) {
	ctx := context.Background()
	shared := newTestInlineRegistryStore()
	now := time.Now()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 3001}
	cacheKey := inlineCacheKey{botUserID: 1001, userID: 2001, peer: peer, query: "shape"}

	waitingRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	answeringRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	queryID, pending := waitingRegistry.registerWithCacheKeyContext(ctx, now, 1001, 2001, peer, cacheKey)
	router := &Router{clock: clock.System, inlines: waitingRegistry}

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	gotCh := make(chan struct {
		results domain.BotInlineResults
		err     error
	}, 1)
	go func() {
		results, err := router.awaitInlineBotResults(waitCtx, pending, 2001, queryID)
		gotCh <- struct {
			results domain.BotInlineResults
			err     error
		}{results: results, err: err}
	}()

	ok := answeringRegistry.resolveContext(ctx, now, 1001, queryID, domain.BotInlineResults{
		CacheTime: 60,
		Results:   []domain.BotInlineResult{{ID: "article-1", Type: "article", Message: "hello"}},
	})
	if !ok {
		t.Fatal("remote resolve = false, want true")
	}

	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("remote wait: %v", got.err)
		}
		if got.results.QueryID != queryID || got.results.UserID != 2001 || got.results.BotUserID != 1001 || got.results.Peer != peer {
			t.Fatalf("remote results metadata = %+v", got.results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote wait did not resolve")
	}

	results, item, ok := answeringRegistry.resultForSendContext(ctx, now, 2001, queryID, "article-1")
	if !ok {
		t.Fatal("remote resultForSend = false, want true")
	}
	if results.Peer != peer || item.Message != "hello" {
		t.Fatalf("remote send recovered results=%+v item=%+v", results, item)
	}
}

func TestInlineRegistrySharedCacheHitCreatesFreshSendableQuery(t *testing.T) {
	ctx := context.Background()
	shared := newTestInlineRegistryStore()
	now := time.Now()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 3002}
	cacheKey := inlineCacheKey{botUserID: 1002, userID: 2002, peer: peer, query: "cached", offset: "p1"}

	ownerRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	queryID, _ := ownerRegistry.registerWithCacheKeyContext(ctx, now, 1002, 2002, peer, cacheKey)
	if !ownerRegistry.resolveContext(ctx, now, 1002, queryID, domain.BotInlineResults{
		CacheTime: 60,
		Results:   []domain.BotInlineResult{{ID: "cached-1", Type: "article", Message: "cached hello"}},
	}) {
		t.Fatal("resolve cached result = false, want true")
	}

	cacheRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	cached, ok := cacheRegistry.cachedContext(ctx, now.Add(time.Second), cacheKey)
	if !ok {
		t.Fatal("shared cache miss, want hit")
	}
	if cached.QueryID != 0 || cached.CacheTime <= 0 || cached.CacheTime > 60 {
		t.Fatalf("shared cached metadata = %+v", cached)
	}
	fresh := cacheRegistry.registerCachedContext(ctx, now.Add(time.Second), 1002, 2002, peer, cached)
	if fresh.QueryID == 0 || fresh.QueryID == queryID {
		t.Fatalf("fresh query_id = %d, old %d", fresh.QueryID, queryID)
	}
	_, item, ok := ownerRegistry.resultForSendContext(ctx, now.Add(time.Second), 2002, fresh.QueryID, "cached-1")
	if !ok || item.Message != "cached hello" {
		t.Fatalf("fresh shared resultForSend ok=%v item=%+v", ok, item)
	}
}

func TestInlineRegistrySharedWebDocumentAndBytes(t *testing.T) {
	ctx := context.Background()
	shared := newTestInlineRegistryStore()
	now := time.Now()
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 3003}
	document := domain.BotInlineWebDocument{
		URL:        "https://example.test/inline.png",
		AccessHash: 9001,
		Size:       6,
		MimeType:   "image/png",
	}

	writerRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	queryID, _ := writerRegistry.registerWithCacheKeyContext(ctx, now, 1003, 2003, peer, inlineCacheKey{botUserID: 1003, userID: 2003, peer: peer, query: "web"})
	if !writerRegistry.resolveContext(ctx, now, 1003, queryID, domain.BotInlineResults{
		CacheTime: 60,
		Results: []domain.BotInlineResult{{
			ID:        "web-1",
			Type:      "photo",
			Content:   &document,
			MediaAuto: true,
		}},
	}) {
		t.Fatal("resolve web document result = false, want true")
	}

	readerRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	gotDoc, data, mime, ok := readerRegistry.webDocumentForDownloadContext(ctx, now.Add(time.Second), document.URL, document.AccessHash)
	if !ok {
		t.Fatal("shared web document miss, want hit")
	}
	if gotDoc.URL != document.URL || gotDoc.AccessHash != document.AccessHash || len(data) != 0 || mime != "" {
		t.Fatalf("shared web document = %+v bytes=%q mime=%q", gotDoc, data, mime)
	}
	if ok := readerRegistry.cacheWebDocumentBytesContext(ctx, now.Add(time.Second), document.URL, document.AccessHash, []byte("abcdef"), "image/png"); !ok {
		t.Fatal("shared web document byte cache = false, want true")
	}

	thirdRegistry := newInlineRegistry(botInlineQueryTTL, shared)
	_, data, mime, ok = thirdRegistry.webDocumentForDownloadContext(ctx, now.Add(2*time.Second), document.URL, document.AccessHash)
	if !ok || string(data) != "abcdef" || mime != "image/png" {
		t.Fatalf("shared web document cached bytes ok=%v bytes=%q mime=%q", ok, data, mime)
	}
	if _, _, _, ok := thirdRegistry.webDocumentForDownloadContext(ctx, now.Add(2*time.Second), document.URL, document.AccessHash+1); ok {
		t.Fatal("shared web document hash mismatch hit, want miss")
	}
}

func TestInlineRegistrySharedPreparedInlineMessage(t *testing.T) {
	ctx := context.Background()
	shared := newTestInlineRegistryStore()
	now := time.Now()
	writer := newInlineRegistry(botInlineQueryTTL, shared)
	reader := newInlineRegistry(botInlineQueryTTL, shared)
	result := domain.BotInlineResult{ID: "prepared-shared", Type: "article", Message: "shared prepared"}

	id, expireDate := writer.savePreparedInlineContext(ctx, now, 1001, 2001, result, []string{store.InlineQueryPeerTypePM})
	if id == "" || expireDate <= int(now.Unix()) {
		t.Fatalf("prepared save = id %q expire %d", id, expireDate)
	}
	got, ok := reader.preparedInlineContext(ctx, now.Add(time.Second), 2001, 1001, id)
	if !ok {
		t.Fatal("shared prepared inline not found")
	}
	if got.QueryID == 0 || got.UserID != 2001 || got.BotUserID != 1001 || len(got.PeerTypes) != 1 || got.PeerTypes[0] != store.InlineQueryPeerTypePM {
		t.Fatalf("shared prepared results = %+v", got)
	}
	_, item, ok := reader.resultForSendContext(ctx, now.Add(time.Second), 2001, got.QueryID, "prepared-shared")
	if !ok || item.Message != "shared prepared" {
		t.Fatalf("shared prepared send lookup ok=%v item=%+v", ok, item)
	}
}

type testInlineRegistryStore struct {
	mu                sync.Mutex
	pending           map[int64]testInlineValue[store.InlinePending]
	results           map[int64]testInlineValue[domain.BotInlineResults]
	cache             map[store.InlineCacheKey]testInlineValue[domain.BotInlineResults]
	prepared          map[string]testInlineValue[store.PreparedInlineMessage]
	web               map[store.InlineWebDocumentKey]testInlineValue[store.InlineWebDocumentEntry]
	webview           map[int64]testInlineValue[store.WebViewSession]
	webviewByBotQuery map[string]int64
}

type testInlineValue[T any] struct {
	value     T
	expiresAt time.Time
}

func newTestInlineRegistryStore() *testInlineRegistryStore {
	return &testInlineRegistryStore{
		pending:           make(map[int64]testInlineValue[store.InlinePending]),
		results:           make(map[int64]testInlineValue[domain.BotInlineResults]),
		cache:             make(map[store.InlineCacheKey]testInlineValue[domain.BotInlineResults]),
		prepared:          make(map[string]testInlineValue[store.PreparedInlineMessage]),
		web:               make(map[store.InlineWebDocumentKey]testInlineValue[store.InlineWebDocumentEntry]),
		webview:           make(map[int64]testInlineValue[store.WebViewSession]),
		webviewByBotQuery: make(map[string]int64),
	}
}

func testInlineExpires(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}

func testInlineExpired(expiresAt time.Time) bool {
	return !expiresAt.IsZero() && !expiresAt.After(time.Now())
}

func (s *testInlineRegistryStore) PutInlinePending(_ context.Context, pending store.InlinePending, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[pending.QueryID] = testInlineValue[store.InlinePending]{value: pending, expiresAt: testInlineExpires(ttl)}
	return nil
}

func (s *testInlineRegistryStore) GetInlinePending(_ context.Context, queryID int64) (store.InlinePending, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.pending[queryID]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.pending, queryID)
		return store.InlinePending{}, false, nil
	}
	return v.value, true, nil
}

func (s *testInlineRegistryStore) DeleteInlinePending(_ context.Context, queryID int64) error {
	s.mu.Lock()
	delete(s.pending, queryID)
	s.mu.Unlock()
	return nil
}

func (s *testInlineRegistryStore) PutInlineResult(_ context.Context, results domain.BotInlineResults, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[results.QueryID] = testInlineValue[domain.BotInlineResults]{value: cloneInlineResults(results), expiresAt: testInlineExpires(ttl)}
	return nil
}

func (s *testInlineRegistryStore) GetInlineResult(_ context.Context, queryID int64) (domain.BotInlineResults, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.results[queryID]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.results, queryID)
		return domain.BotInlineResults{}, false, nil
	}
	return cloneInlineResults(v.value), true, nil
}

func (s *testInlineRegistryStore) DeleteInlineResult(_ context.Context, queryID int64) error {
	s.mu.Lock()
	delete(s.results, queryID)
	s.mu.Unlock()
	return nil
}

func (s *testInlineRegistryStore) PutInlineCache(_ context.Context, key store.InlineCacheKey, results domain.BotInlineResults, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	results.QueryID = 0
	s.cache[key] = testInlineValue[domain.BotInlineResults]{value: cloneInlineResults(results), expiresAt: testInlineExpires(ttl)}
	return nil
}

func (s *testInlineRegistryStore) GetInlineCache(_ context.Context, key store.InlineCacheKey) (domain.BotInlineResults, bool, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.cache[key]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.cache, key)
		return domain.BotInlineResults{}, false, 0, nil
	}
	remaining := time.Until(v.expiresAt)
	if v.expiresAt.IsZero() || remaining <= 0 {
		remaining = time.Second
	}
	return cloneInlineResults(v.value), true, remaining, nil
}

func (s *testInlineRegistryStore) PutInlineWebDocument(_ context.Context, document domain.BotInlineWebDocument, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := store.InlineWebDocumentKey{URL: document.URL, AccessHash: document.AccessHash}
	entry := store.InlineWebDocumentEntry{Document: cloneInlineWebDocument(document)}
	if existing, ok := s.web[key]; ok && !testInlineExpired(existing.expiresAt) {
		entry.Bytes = append([]byte(nil), existing.value.Bytes...)
		entry.MimeType = existing.value.MimeType
	}
	s.web[key] = testInlineValue[store.InlineWebDocumentEntry]{value: entry, expiresAt: testInlineExpires(ttl)}
	return nil
}

func (s *testInlineRegistryStore) GetInlineWebDocument(_ context.Context, key store.InlineWebDocumentKey) (store.InlineWebDocumentEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.web[key]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.web, key)
		return store.InlineWebDocumentEntry{}, false, nil
	}
	entry := v.value
	entry.Document = cloneInlineWebDocument(entry.Document)
	entry.Bytes = append([]byte(nil), entry.Bytes...)
	return entry, true, nil
}

func (s *testInlineRegistryStore) PutInlineWebDocumentBytes(_ context.Context, key store.InlineWebDocumentKey, data []byte, mimeType string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.web[key]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.web, key)
		return errors.New("inline web document missing")
	}
	v.value.Bytes = append([]byte(nil), data...)
	v.value.MimeType = mimeType
	if v.expiresAt.IsZero() {
		v.expiresAt = testInlineExpires(ttl)
	}
	s.web[key] = v
	return nil
}

func (s *testInlineRegistryStore) PutPreparedInlineMessage(_ context.Context, msg store.PreparedInlineMessage, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg.Results = cloneInlineResults(msg.Results)
	s.prepared[msg.ID] = testInlineValue[store.PreparedInlineMessage]{value: msg, expiresAt: testInlineExpires(ttl)}
	return nil
}

func (s *testInlineRegistryStore) GetPreparedInlineMessage(_ context.Context, id string) (store.PreparedInlineMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.prepared[id]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.prepared, id)
		return store.PreparedInlineMessage{}, false, nil
	}
	msg := v.value
	msg.Results = cloneInlineResults(msg.Results)
	return msg, true, nil
}

func (s *testInlineRegistryStore) PutWebViewSession(_ context.Context, session store.WebViewSession, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webview[session.QueryID] = testInlineValue[store.WebViewSession]{value: cloneWebViewSession(session), expiresAt: testInlineExpires(ttl)}
	s.webviewByBotQuery[session.BotQueryID] = session.QueryID
	return nil
}

func (s *testInlineRegistryStore) GetWebViewSession(_ context.Context, queryID int64) (store.WebViewSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.webview[queryID]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.webview, queryID)
		return store.WebViewSession{}, false, nil
	}
	return cloneWebViewSession(v.value), true, nil
}

func (s *testInlineRegistryStore) GetWebViewSessionByBotQuery(_ context.Context, botQueryID string) (store.WebViewSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	queryID, ok := s.webviewByBotQuery[botQueryID]
	if !ok {
		return store.WebViewSession{}, false, nil
	}
	v, ok := s.webview[queryID]
	if !ok || testInlineExpired(v.expiresAt) {
		delete(s.webview, queryID)
		delete(s.webviewByBotQuery, botQueryID)
		return store.WebViewSession{}, false, nil
	}
	return cloneWebViewSession(v.value), true, nil
}

func (s *testInlineRegistryStore) DeleteWebViewSession(_ context.Context, queryID int64, botQueryID string) error {
	s.mu.Lock()
	delete(s.webview, queryID)
	delete(s.webviewByBotQuery, botQueryID)
	s.mu.Unlock()
	return nil
}
