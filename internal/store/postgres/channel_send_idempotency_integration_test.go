package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/deploy"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestChannelSendFingerprintReplayPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 181, Phone: "+1781" + suffix + "01", FirstName: "ChannelReplayOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, channelID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Channel replay " + suffix,
		Megagroup:     true,
		Date:          1700100000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	base := domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  781001,
		Message:   "immutable original",
		Entities: []domain.MessageEntity{{
			Type:   domain.MessageEntityBold,
			Offset: 0,
			Length: 9,
		}},
		Date: 1700100001,
	}
	wantFingerprint, err := store.ChannelSendFingerprint(base)
	if err != nil {
		t.Fatalf("fingerprint base: %v", err)
	}
	first, err := channels.SendChannelMessage(ctx, base)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	var storedFingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id = $1 AND id = $2`, channelID, first.Message.ID).Scan(&storedFingerprint); err != nil {
		t.Fatalf("load fingerprint: %v", err)
	}
	if !bytes.Equal(storedFingerprint, wantFingerprint) {
		t.Fatalf("stored fingerprint = %x, want %x", storedFingerprint, wantFingerprint)
	}

	type durableState struct {
		pts    int
		events int
		rows   int
	}
	loadState := func(randomID int64) durableState {
		t.Helper()
		var state durableState
		if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, channelID).Scan(&state.pts); err != nil {
			t.Fatalf("load channel pts: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, channelID).Scan(&state.events); err != nil {
			t.Fatalf("count channel events: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_messages WHERE channel_id = $1 AND sender_user_id = $2 AND random_id = $3`, channelID, owner.ID, randomID).Scan(&state.rows); err != nil {
			t.Fatalf("count random receipt: %v", err)
		}
		return state
	}

	before := loadState(base.RandomID)
	exact := base
	exact.Date += 100 // execution time is not part of immutable intent
	replay, err := channels.SendChannelMessage(ctx, exact)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replay.Duplicate || replay.Message.ID != first.Message.ID || replay.Event.Pts != first.Event.Pts {
		t.Fatalf("exact replay = %+v, want first id=%d pts=%d", replay, first.Message.ID, first.Event.Pts)
	}
	if after := loadState(base.RandomID); after != before {
		t.Fatalf("exact replay mutated state = %+v, want %+v", after, before)
	}

	conflicts := []struct {
		name   string
		mutate func(*domain.SendChannelMessageRequest)
	}{
		{name: "body", mutate: func(req *domain.SendChannelMessageRequest) { req.Message = "changed body" }},
		{name: "media", mutate: func(req *domain.SendChannelMessageRequest) {
			req.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 781, AccessHash: 782, DCID: 2}}
		}},
		{name: "reply", mutate: func(req *domain.SendChannelMessageRequest) {
			req.ReplyTo = &domain.MessageReply{MessageID: first.Message.ID}
		}},
		{name: "group", mutate: func(req *domain.SendChannelMessageRequest) { req.GroupedID = 781003 }},
	}
	for _, tc := range conflicts {
		t.Run("conflict_"+tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			if _, err := channels.SendChannelMessage(ctx, changed); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
				t.Fatalf("changed %s replay err = %v, want ErrMessageRandomIDDuplicate", tc.name, err)
			}
			if after := loadState(base.RandomID); after != before {
				t.Fatalf("changed %s replay mutated state = %+v, want %+v", tc.name, after, before)
			}
		})
	}

	if _, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, ID: first.Message.ID, Message: "edited current", EditDate: 1700100002,
	}); err != nil {
		t.Fatalf("edit channel message: %v", err)
	}
	editedReplay, err := channels.SendChannelMessage(ctx, exact)
	if err != nil {
		t.Fatalf("replay edited message: %v", err)
	}
	if !editedReplay.Duplicate || editedReplay.Message.Body != "edited current" || editedReplay.Event.Pts != first.Event.Pts {
		t.Fatalf("edited replay = %+v, want current projection with first pts", editedReplay)
	}
	deleted, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, IDs: []int{first.Message.ID}, Date: 1700100003,
	})
	if err != nil {
		t.Fatalf("delete channel message: %v", err)
	}
	deletedReplay, err := channels.SendChannelMessage(ctx, exact)
	if err != nil {
		t.Fatalf("replay deleted message: %v", err)
	}
	if !deletedReplay.Duplicate || deletedReplay.Message.Body != base.Message || deletedReplay.Message.ID != first.Message.ID || deletedReplay.ReplayDeleteEvent == nil || deletedReplay.ReplayDeleteEvent.Pts != deleted.Event.Pts {
		t.Fatalf("deleted replay = %+v, want immutable first snapshot + delete receipt %+v", deletedReplay, deleted.Event)
	}

	// A raw request-boundary fingerprint must be stored byte-for-byte rather
	// than replaced with the domain fallback.
	raw := sha256.Sum256([]byte("raw channel TL intent"))
	rawReq := domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 781002, Message: "raw fingerprint", Date: 1700100010,
		IdempotencyFingerprint: raw[:],
	}
	rawSent, err := channels.SendChannelMessage(ctx, rawReq)
	if err != nil {
		t.Fatalf("raw fingerprint send: %v", err)
	}
	storedFingerprint = nil
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id = $1 AND id = $2`, channelID, rawSent.Message.ID).Scan(&storedFingerprint); err != nil {
		t.Fatalf("load raw fingerprint: %v", err)
	}
	if !bytes.Equal(storedFingerprint, raw[:]) {
		t.Fatalf("stored raw fingerprint = %x, want %x", storedFingerprint, raw)
	}

	// Simulate a rolling old writer that omits the new column. The empty
	// default keeps the write compatible, but it is never accepted as replay.
	legacyID := rawSent.Message.ID + 100
	legacyRandomID := int64(781099)
	if _, err := pool.Exec(ctx, `
INSERT INTO channel_messages (channel_id, id, random_id, sender_user_id, from_peer_id, message_date, pts, body)
VALUES ($1,$2,$3,$4,$4,$5,$6,$7)`, channelID, legacyID, legacyRandomID, owner.ID, 1700100020, rawSent.Event.Pts+100, "legacy unknown intent"); err != nil {
		t.Fatalf("old-writer insert without fingerprint: %v", err)
	}
	var legacyFingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id=$1 AND id=$2`, channelID, legacyID).Scan(&legacyFingerprint); err != nil {
		t.Fatalf("load legacy fingerprint: %v", err)
	}
	if len(legacyFingerprint) != 0 {
		t.Fatalf("legacy fingerprint length = %d, want empty", len(legacyFingerprint))
	}
	legacyReq := domain.SendChannelMessageRequest{UserID: owner.ID, ChannelID: channelID, RandomID: legacyRandomID, Message: "legacy unknown intent", Date: 1700100021}
	legacyExpected, err := store.ChannelSendFingerprint(legacyReq)
	if err != nil {
		t.Fatalf("fingerprint legacy retry: %v", err)
	}
	if _, _, err := channels.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
		ChannelID: channelID, SenderUserID: owner.ID, RandomID: legacyRandomID, IdempotencyFingerprint: legacyExpected,
	}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("legacy empty lookup err = %v, want ErrMessageRandomIDDuplicate", err)
	}
	if _, err := channels.SendChannelMessage(ctx, legacyReq); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("legacy empty send err = %v, want ErrMessageRandomIDDuplicate", err)
	}
}

func TestChannelSendFingerprintConcurrentRacePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 191, Phone: "+1781" + suffix + "11", FirstName: "ChannelRaceOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id = ANY($1::bigint[])`, channelIDs)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, owner.ID)
	})
	newChannel := func(title string) int64 {
		t.Helper()
		created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: title + suffix, Megagroup: true, Date: 1700110000})
		if err != nil {
			t.Fatalf("create %s channel: %v", title, err)
		}
		channelIDs = append(channelIDs, created.Channel.ID)
		return created.Channel.ID
	}

	run := func(reqs [2]domain.SendChannelMessageRequest) ([2]domain.SendChannelMessageResult, [2]error) {
		t.Helper()
		var results [2]domain.SendChannelMessageResult
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range reqs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], errs[i] = NewChannelStore(pool).SendChannelMessage(ctx, reqs[i])
			}(i)
		}
		close(start)
		wg.Wait()
		return results, errs
	}

	exactChannelID := newChannel("exact race ")
	exactReq := domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: exactChannelID, RandomID: 791001, Message: "same intent", Date: 1700110001,
		IdempotencyPreflighted: true,
	}
	exactResults, exactErrs := run([2]domain.SendChannelMessageRequest{exactReq, exactReq})
	for i, err := range exactErrs {
		if err != nil {
			t.Fatalf("exact race result[%d] err = %v", i, err)
		}
	}
	if exactResults[0].Message.ID != exactResults[1].Message.ID || exactResults[0].Duplicate == exactResults[1].Duplicate {
		t.Fatalf("exact race results = %+v / %+v, want same id and one duplicate", exactResults[0], exactResults[1])
	}
	assertChannelRandomReceiptCount(t, ctx, pool, exactChannelID, owner.ID, exactReq.RandomID, 1)

	conflictChannelID := newChannel("conflict race ")
	conflictA := domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: conflictChannelID, RandomID: 791002, Message: "intent A", Date: 1700110010,
		IdempotencyPreflighted: true,
	}
	conflictB := conflictA
	conflictB.Message = "intent B"
	conflictResults, conflictErrs := run([2]domain.SendChannelMessageRequest{conflictA, conflictB})
	nilCount, duplicateErrCount := 0, 0
	for _, err := range conflictErrs {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, domain.ErrMessageRandomIDDuplicate):
			duplicateErrCount++
		default:
			t.Fatalf("conflicting race unexpected err = %v; results=%+v", err, conflictResults)
		}
	}
	if nilCount != 1 || duplicateErrCount != 1 {
		t.Fatalf("conflicting race errors = %v, want one success and one duplicate", conflictErrs)
	}
	assertChannelRandomReceiptCount(t, ctx, pool, conflictChannelID, owner.ID, conflictA.RandomID, 1)
}

func TestChannelSendFingerprintSingleConnectionConflictLookupPostgres(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	setupPool := testPool(t)
	setupCtx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(setupPool)
	owner, err := users.Create(setupCtx, domain.User{AccessHash: 192, Phone: "+1781" + suffix + "21", FirstName: "OneConnectionOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if len(channelIDs) != 0 {
			_, _ = setupPool.Exec(cleanupCtx, `DELETE FROM channels WHERE id = ANY($1::bigint[])`, channelIDs)
		}
		_, _ = setupPool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, owner.ID)
	})

	setupChannels := NewChannelStore(setupPool)
	created, err := setupChannels.CreateChannel(setupCtx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "one connection " + suffix, Megagroup: true, Date: 1700120000,
	})
	if err != nil {
		t.Fatalf("create ordinary channel: %v", err)
	}
	channelIDs = append(channelIDs, created.Channel.ID)
	ordinaryReq := domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: created.Channel.ID, RandomID: 792001, Message: "single pool exact", Date: 1700120001,
		IdempotencyPreflighted: true,
	}
	broadcast, err := setupChannels.CreateChannel(setupCtx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "one connection mono " + suffix, Broadcast: true, Date: 1700120010,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channelIDs = append(channelIDs, broadcast.Channel.ID)
	enabled, err := setupChannels.SetPaidMessagesPrice(setupCtx, owner.ID, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable monoforum: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	channelIDs = append(channelIDs, monoID)

	// Fixture creation itself has legacy allocator paths that require more than
	// one connection. Constrain only the send/replay path under test.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse postgres config: %v", err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open one-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	msgIDs := &singleConnectionMessageIDAllocator{current: make(map[int64]int)}
	for _, id := range []int64{created.Channel.ID, monoID} {
		var current int
		if err := setupPool.QueryRow(setupCtx, `SELECT COALESCE(MAX(id), 0) FROM channel_messages WHERE channel_id=$1`, id).Scan(&current); err != nil {
			t.Fatalf("seed message allocator for channel %d: %v", id, err)
		}
		msgIDs.current[id] = current
	}
	oneConnectionStore := func() *ChannelStore {
		return NewChannelStore(pool, WithChannelAllocators(nil, msgIDs))
	}

	var ordinaryResults [2]domain.SendChannelMessageResult
	var ordinaryErrs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range ordinaryResults {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ordinaryResults[i], ordinaryErrs[i] = oneConnectionStore().SendChannelMessage(ctx, ordinaryReq)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range ordinaryErrs {
		if err != nil {
			t.Fatalf("one-connection ordinary result[%d] err = %v", i, err)
		}
	}
	if ordinaryResults[0].Message.ID != ordinaryResults[1].Message.ID || ordinaryResults[0].Duplicate == ordinaryResults[1].Duplicate {
		t.Fatalf("one-connection ordinary results = %+v / %+v, want same id and one duplicate", ordinaryResults[0], ordinaryResults[1])
	}

	monoReq := domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: owner.ID,
		SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		RandomID:  792002, Message: "single pool mono exact", Date: 1700120011,
		IdempotencyPreflighted: true,
	}
	var monoResults [2]domain.SendChannelMessageResult
	var monoErrs [2]error
	start = make(chan struct{})
	for i := range monoResults {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			monoResults[i], monoErrs[i] = oneConnectionStore().SendMonoforumMessage(ctx, monoReq)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range monoErrs {
		if err != nil {
			t.Fatalf("one-connection monoforum result[%d] err = %v", i, err)
		}
	}
	if monoResults[0].Message.ID != monoResults[1].Message.ID || monoResults[0].Duplicate == monoResults[1].Duplicate {
		t.Fatalf("one-connection monoforum results = %+v / %+v, want same id and one duplicate", monoResults[0], monoResults[1])
	}
}

type singleConnectionMessageIDAllocator struct {
	mu      sync.Mutex
	current map[int64]int
}

func (a *singleConnectionMessageIDAllocator) NextChannelMessageID(_ context.Context, channelID int64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.current[channelID]++
	return a.current[channelID], nil
}

func (a *singleConnectionMessageIDAllocator) CurrentChannelMessageID(_ context.Context, channelID int64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current[channelID], nil
}

func assertChannelRandomReceiptCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channelID, senderUserID, randomID int64, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_messages WHERE channel_id=$1 AND sender_user_id=$2 AND random_id=$3`, channelID, senderUserID, randomID).Scan(&got); err != nil {
		t.Fatalf("count channel random receipt: %v", err)
	}
	if got != want {
		t.Fatalf("channel random receipt count = %d, want %d", got, want)
	}
}

func TestChannelSendFingerprintMigrationRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	downSQL, err := deploy.Migrations.ReadFile("migrations/0078_channel_send_fingerprint.down.sql")
	if err != nil {
		t.Fatalf("read 0078 down: %v", err)
	}
	upSQL, err := deploy.Migrations.ReadFile("migrations/0078_channel_send_fingerprint.up.sql")
	if err != nil {
		t.Fatalf("read 0078 up: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin 0078 round trip: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("0078 down: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("0078 up: %v", err)
	}
	var defaultExpr string
	var constraintExists bool
	if err := tx.QueryRow(ctx, `
SELECT column_default,
       EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'channel_messages_request_fingerprint_size')
FROM information_schema.columns
WHERE table_schema='public' AND table_name='channel_messages' AND column_name='request_fingerprint'`).Scan(&defaultExpr, &constraintExists); err != nil {
		t.Fatalf("inspect 0078: %v", err)
	}
	if !strings.Contains(defaultExpr, `\x`) || !constraintExists {
		t.Fatalf("0078 default=%q constraint=%v, want empty bytea rolling default + size constraint", defaultExpr, constraintExists)
	}
}
