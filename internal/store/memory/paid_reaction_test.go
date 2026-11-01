package memory

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

func paidReactionTestRandomID(date int, low uint32) int64 {
	return int64(uint64(uint32(date))<<32 | uint64(low))
}

func seedBroadcastPost(t *testing.T, st *ChannelStore, creator int64, broadcast bool) (channelID int64, msgID int) {
	t.Helper()
	ctx := context.Background()
	created, err := st.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator,
		Title:         "Paid",
		Broadcast:     broadcast,
		Megagroup:     !broadcast,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if broadcast {
		if _, err := st.SetAvailableReactions(ctx, creator, created.Channel.ID, domain.ChannelReactionPolicy{
			Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
		}); err != nil {
			t.Fatalf("enable paid reactions: %v", err)
		}
	}
	sent, err := st.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    creator,
		ChannelID: created.Channel.ID,
		Message:   "post",
		Date:      1700000000,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	return created.Channel.ID, sent.Message.ID
}

// Paid reaction accumulation + aggregation: multiple top-ups by the same reactor add up, and TopReactors includes the caller with My set.
func TestAddChannelMessagePaidReactionAccumulates(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000001)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)

	res, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 100, RandomID: paidReactionTestRandomID(1700000001, 1001), Date: 1700000001,
	})
	if err != nil {
		t.Fatalf("first paid reaction: %v", err)
	}
	if res.Paid.TotalStars != 100 || res.Paid.MyStars != 100 {
		t.Fatalf("after 100 = total %d my %d, want 100/100", res.Paid.TotalStars, res.Paid.MyStars)
	}

	res, err = st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 50, RandomID: paidReactionTestRandomID(1700000002, 1002), Date: 1700000002,
	})
	if err != nil {
		t.Fatalf("second paid reaction: %v", err)
	}
	if res.Paid.TotalStars != 150 || res.Paid.MyStars != 150 {
		t.Fatalf("after +50 = total %d my %d, want 150/150 (accumulated)", res.Paid.TotalStars, res.Paid.MyStars)
	}
	if len(res.Paid.TopReactors) != 1 || res.Paid.TopReactors[0].Stars != 150 || !res.Paid.TopReactors[0].My {
		t.Fatalf("top reactors = %+v, want one My 150", res.Paid.TopReactors)
	}
}

func TestAddChannelMessagePaidReactionAtomicIdempotency(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000010)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	req := domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID,
		Stars: 75, RandomID: paidReactionTestRandomID(1700000100, 99001), Date: 1700000100,
	}
	ptsBefore := st.ptsSeq[channelID]

	first, err := st.AddChannelMessagePaidReaction(ctx, req)
	if err != nil {
		t.Fatalf("first paid reaction: %v", err)
	}
	if first.Duplicate || first.PayerBalance.Balance != domain.DefaultStarsStartingGrant-75 || first.ChannelBalance != 75 {
		t.Fatalf("first receipt = %+v", first)
	}
	if got := st.ptsSeq[channelID]; got != ptsBefore {
		t.Fatalf("paid reaction consumed channel PTS: before=%d after=%d", ptsBefore, got)
	}
	req.Date++ // server-derived date is not part of the client command
	replay, err := st.AddChannelMessagePaidReaction(ctx, req)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replay.Duplicate || replay.Paid.TotalStars != 75 || replay.PayerBalance.Balance != first.PayerBalance.Balance || replay.ChannelBalance != 75 {
		t.Fatalf("replay = %+v, want unchanged first receipt", replay)
	}
	if got := st.starsBalances[creator]; got != domain.DefaultStarsStartingGrant-75 {
		t.Fatalf("payer balance after replay = %d", got)
	}
	if got := st.channelStarsBalances[channelID]; got != 75 {
		t.Fatalf("channel balance after replay = %d", got)
	}

	conflict := req
	conflict.Stars++
	if _, err := st.AddChannelMessagePaidReaction(ctx, conflict); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed random_id payload err = %v", err)
	}
}

func TestAddChannelMessagePaidReactionReplayUsesFirstSnapshotAfterStateChanges(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000012)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	firstReq := domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID,
		Stars: 30, RandomID: paidReactionTestRandomID(1700000300, 99101), Date: 1700000300,
	}
	first, err := st.AddChannelMessagePaidReaction(ctx, firstReq)
	if err != nil {
		t.Fatalf("first paid reaction: %v", err)
	}
	if _, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID,
		Stars: 20, RandomID: paidReactionTestRandomID(1700000301, 99102), Date: 1700000301,
	}); err != nil {
		t.Fatalf("later paid reaction: %v", err)
	}
	st.mu.Lock()
	channel := st.channels[channelID]
	channel.Deleted = true
	st.channels[channelID] = channel
	st.members[channelID][creator] = domain.ChannelMember{ChannelID: channelID, UserID: creator, Status: domain.ChannelMemberKicked}
	for i := range st.messages[channelID] {
		if st.messages[channelID][i].ID == msgID {
			st.messages[channelID][i].Deleted = true
		}
	}
	st.mu.Unlock()

	replay, err := st.AddChannelMessagePaidReaction(ctx, firstReq)
	if err != nil {
		t.Fatalf("replay after delete/kick/later reaction: %v", err)
	}
	if !replay.Duplicate || replay.Paid.TotalStars != first.Paid.TotalStars || replay.Paid.MyStars != first.Paid.MyStars ||
		replay.ChannelBalance != first.ChannelBalance || replay.PayerBalance.Balance != domain.DefaultStarsStartingGrant-50 {
		t.Fatalf("replay=%+v, want frozen first receipt=%+v", replay, first)
	}
}

func TestAddChannelMessagePaidReactionConcurrentReplay(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000013)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	req := domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID,
		Stars: 11, RandomID: paidReactionTestRandomID(1700000400, 99103), Date: 1700000400,
	}
	const attempts = 8
	results := make(chan domain.ChannelMessagePaidReactionResult, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := st.AddChannelMessagePaidReaction(ctx, req)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
	}
	duplicates := 0
	for result := range results {
		if result.Duplicate {
			duplicates++
		}
	}
	if duplicates != attempts-1 || st.starsBalances[creator] != domain.DefaultStarsStartingGrant-11 || st.channelStarsBalances[channelID] != 11 {
		t.Fatalf("duplicates=%d payer=%d channel=%d", duplicates, st.starsBalances[creator], st.channelStarsBalances[channelID])
	}
}

func TestAddChannelMessagePaidReactionExpiresUncommittedRandomID(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000014)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	const now = 1700100000
	_, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 1,
		RandomID: paidReactionTestRandomID(now-domain.PaidReactionRandomIDMaxAgeSeconds-1, 99104), Date: now,
	})
	if !errors.Is(err, domain.ErrPaidReactionRandomIDExpired) {
		t.Fatalf("expired random id err=%v", err)
	}
	if len(st.paidReactionCommands) != 0 || st.channelStarsBalances[channelID] != 0 {
		t.Fatal("expired command mutated receipt or channel balance")
	}
}

func TestAddChannelMessagePaidReactionValidatesAndPersistsChannelSendAs(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const payer = int64(1000000015)
	targetID, msgID := seedBroadcastPost(t, st, payer, true)
	displayID, _ := seedBroadcastPost(t, st, payer, true)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: displayID}
	result, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: payer, ChannelID: targetID, MessageID: msgID, Stars: 9,
		RandomID: paidReactionTestRandomID(1700000500, 99105), Date: 1700000500,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer}, DisplayPeer: peer,
	})
	if err != nil {
		t.Fatalf("owned channel send-as: %v", err)
	}
	if len(result.Paid.TopReactors) != 1 || result.Paid.TopReactors[0].DisplayPeer() != peer ||
		len(result.DisplayChannels) != 1 || result.DisplayChannels[0].ID != displayID {
		t.Fatalf("send-as result=%+v", result)
	}

	const outsider = int64(1000000016)
	invalidPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: targetID}
	_, err = st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: outsider, ChannelID: targetID, MessageID: msgID, Stars: 1,
		RandomID: paidReactionTestRandomID(1700000501, 99106), Date: 1700000501,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &invalidPeer}, DisplayPeer: invalidPeer,
	})
	if !errors.Is(err, domain.ErrChannelPrivate) {
		// Target membership rejects before send-as; validate the send-as boundary
		// with a payer who is allowed on target but not on another identity.
		t.Fatalf("outsider target err=%v", err)
	}
	other, err := st.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: outsider, Title: "Admin-only display", Broadcast: true,
		MemberUserIDs: []int64{payer}, Date: 1700000502,
	})
	if err != nil {
		t.Fatalf("create admin-only display channel: %v", err)
	}
	if _, err := st.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID: outsider, ChannelID: other.Channel.ID, MemberID: payer,
		AdminRights: domain.ChannelAdminRights{PostMessages: true}, Date: 1700000502,
	}); err != nil {
		t.Fatalf("promote display channel admin: %v", err)
	}
	invalidPeer = domain.Peer{Type: domain.PeerTypeChannel, ID: other.Channel.ID}
	_, err = st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: payer, ChannelID: targetID, MessageID: msgID, Stars: 1,
		RandomID: paidReactionTestRandomID(1700000502, 99107), Date: 1700000502,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &invalidPeer}, DisplayPeer: invalidPeer,
	})
	if !errors.Is(err, domain.ErrPaidReactionSendAsPeerInvalid) {
		t.Fatalf("foreign channel send-as err=%v", err)
	}
}

func TestAddChannelMessagePaidReactionReceiptSnapshotsAllTopDisplayChannels(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const (
		payer1 = int64(1000000017)
		payer2 = int64(1000000018)
	)
	created, err := st.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: payer1, Title: "Paid target", Broadcast: true,
		MemberUserIDs: []int64{payer2}, Date: 1700000600,
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := st.SetAvailableReactions(ctx, payer1, created.Channel.ID, domain.ChannelReactionPolicy{
		Type: domain.ChannelReactionPolicyAll, PaidEnabled: true,
	}); err != nil {
		t.Fatalf("enable target paid reactions: %v", err)
	}
	sent, err := st.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: payer1, ChannelID: created.Channel.ID, Message: "post", Date: 1700000601,
	})
	if err != nil {
		t.Fatalf("send target post: %v", err)
	}
	display1, _ := seedBroadcastPost(t, st, payer1, true)
	display2, _ := seedBroadcastPost(t, st, payer2, true)
	peer1 := domain.Peer{Type: domain.PeerTypeChannel, ID: display1}
	peer2 := domain.Peer{Type: domain.PeerTypeChannel, ID: display2}
	if _, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: payer1, ChannelID: created.Channel.ID, MessageID: sent.Message.ID, Stars: 100,
		RandomID: paidReactionTestRandomID(1700000602, 99108), Date: 1700000602,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer1}, DisplayPeer: peer1,
	}); err != nil {
		t.Fatalf("first channel identity: %v", err)
	}
	secondReq := domain.SendChannelPaidReactionRequest{
		UserID: payer2, ChannelID: created.Channel.ID, MessageID: sent.Message.ID, Stars: 90,
		RandomID: paidReactionTestRandomID(1700000603, 99109), Date: 1700000603,
		Privacy: domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer2}, DisplayPeer: peer2,
	}
	second, err := st.AddChannelMessagePaidReaction(ctx, secondReq)
	if err != nil {
		t.Fatalf("second channel identity: %v", err)
	}
	if len(second.DisplayChannels) != 2 || second.DisplayChannels[0].ID != display1 || second.DisplayChannels[1].ID != display2 {
		t.Fatalf("display channel snapshots=%+v", second.DisplayChannels)
	}
	st.mu.Lock()
	for _, id := range []int64{display1, display2} {
		channel := st.channels[id]
		channel.Deleted = true
		st.channels[id] = channel
		delete(st.members[id], payer1)
		delete(st.members[id], payer2)
	}
	st.mu.Unlock()
	replay, err := st.AddChannelMessagePaidReaction(ctx, secondReq)
	if err != nil {
		t.Fatalf("replay after display channels deleted: %v", err)
	}
	if !replay.Duplicate || len(replay.DisplayChannels) != 2 || replay.DisplayChannels[0].Deleted || replay.DisplayChannels[1].Deleted {
		t.Fatalf("replay lost immutable display channel snapshots: %+v", replay.DisplayChannels)
	}
}

func TestAddChannelMessagePaidReactionChecksPaidPolicyAndBalance(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000011)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	if _, err := st.SetAvailableReactions(ctx, creator, channelID, domain.ChannelReactionPolicy{Type: domain.ChannelReactionPolicyAll}); err != nil {
		t.Fatalf("disable paid reactions: %v", err)
	}
	req := domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 10, RandomID: paidReactionTestRandomID(1700000200, 99002), Date: 1700000200,
	}
	if _, err := st.AddChannelMessagePaidReaction(ctx, req); !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("disabled paid reaction err = %v", err)
	}
	if _, err := st.SetAvailableReactions(ctx, creator, channelID, domain.ChannelReactionPolicy{Type: domain.ChannelReactionPolicyAll, PaidEnabled: true}); err != nil {
		t.Fatalf("enable paid reactions: %v", err)
	}
	st.starsBalances[creator] = 5
	if _, err := st.AddChannelMessagePaidReaction(ctx, req); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient paid reaction err = %v", err)
	}
	if st.channelStarsBalances[channelID] != 0 || len(st.paidReactionCommands) != 0 {
		t.Fatalf("failed command mutated ledger/receipt")
	}
}

// Multiple reactors: TopReactors is ordered by star count descending and the caller is always present.
func TestAddChannelMessagePaidReactionTopReactors(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000001)
	channelID, msgID := seedBroadcastPost(t, st, creator, true)
	// Make two other users members and add top-ups (write the store accumulation directly, bypassing member validation to test aggregation only).
	for _, c := range []struct {
		user  int64
		stars int64
	}{{creator, 30}, {2000000002, 200}, {2000000003, 80}} {
		// Only the creator goes through the official path; other users accumulate directly to build the ranking.
		if c.user == creator {
			if _, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
				UserID: c.user, ChannelID: channelID, MessageID: msgID, Stars: c.stars, RandomID: paidReactionTestRandomID(1700000010, 2001), Date: 1700000010,
			}); err != nil {
				t.Fatalf("creator paid reaction: %v", err)
			}
			continue
		}
		st.mu.Lock()
		st.paidReactions[channelID][msgID][c.user] = memoryPaidReaction{stars: c.stars, date: 1700000010}
		st.mu.Unlock()
	}
	res, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 0 + 1, RandomID: paidReactionTestRandomID(1700000011, 2002), Date: 1700000011,
	})
	// creator is now 31+? Recompute: creator 30 + this 1 = 31.
	if err != nil {
		t.Fatalf("paid reaction: %v", err)
	}
	if res.Paid.TotalStars != 31+200+80 {
		t.Fatalf("total = %d, want %d", res.Paid.TotalStars, 31+200+80)
	}
	// Descending: 200, 80, 31.
	if len(res.Paid.TopReactors) != 3 || res.Paid.TopReactors[0].Stars != 200 || res.Paid.TopReactors[1].Stars != 80 || res.Paid.TopReactors[2].Stars != 31 {
		t.Fatalf("top reactors = %+v, want 200/80/31 desc", res.Paid.TopReactors)
	}
	if !res.Paid.TopReactors[2].My {
		t.Fatalf("creator (31) must carry My flag, got %+v", res.Paid.TopReactors[2])
	}
}

// A non-broadcast channel rejects paid reactions.
func TestAddChannelMessagePaidReactionRejectsMegagroup(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000001)
	channelID, msgID := seedBroadcastPost(t, st, creator, false)
	_, err := st.AddChannelMessagePaidReaction(ctx, domain.SendChannelPaidReactionRequest{
		UserID: creator, ChannelID: channelID, MessageID: msgID, Stars: 10, RandomID: paidReactionTestRandomID(1700000001, 3001), Date: 1700000001,
	})
	if !errors.Is(err, domain.ErrReactionInvalid) {
		t.Fatalf("megagroup paid reaction err = %v, want ErrReactionInvalid", err)
	}
}
