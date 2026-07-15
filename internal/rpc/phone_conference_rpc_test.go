package rpc

import (
	"context"
	"encoding/binary"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appgroupcalls "telesrv/internal/app/groupcalls"
	appmessages "telesrv/internal/app/messages"
	appphone "telesrv/internal/app/phone"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type conferenceFixture struct {
	ctx      context.Context
	router   *Router
	group    *appgroupcalls.Service
	messages *appmessages.Service
	sessions *groupCallSessions
	alice    domain.User
	bob      domain.User
	carol    domain.User
	clock    *phoneTestClock
}

func newConferenceFixture(t *testing.T) *conferenceFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	groupStore := memory.NewGroupCallStore()
	sessions := &groupCallSessions{}
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	groupSvc := appgroupcalls.NewService(groupStore)
	messageSvc := appmessages.NewService(messageStore, dialogs)
	router := New(Config{GroupCallMaxParticipants: 8}, Deps{
		Users:      appusers.NewService(users),
		Messages:   messageSvc,
		GroupCalls: groupSvc,
		Phone:      appphone.NewService(appphone.Config{}, appphone.WithClock(clk)),
		Sessions:   sessions,
	}, zaptest.NewLogger(t), clk)
	mk := func(hash int64, phone, name string) domain.User {
		u, err := users.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f := &conferenceFixture{ctx: ctx, router: router, group: groupSvc, messages: messageSvc, sessions: sessions, clock: clk}
	f.alice = mk(3001, "13700000001", "Alice")
	f.bob = mk(3002, "13700000002", "Bob")
	f.carol = mk(3003, "13700000003", "Carol")
	f.sessions.online = []int64{f.alice.ID, f.bob.ID, f.carol.ID}
	return f
}

func (f *conferenceFixture) userCtx(u domain.User, session int64) context.Context {
	return WithSessionID(WithUserID(f.ctx, u.ID), session)
}

func joinConferenceForTest(t *testing.T, f *conferenceFixture, ctx context.Context, call tg.InputGroupCallClass, blockSuffix string, ssrc int32) {
	t.Helper()
	block := conferenceTestBlock(conferenceChainBlockConstructor, blockSuffix)
	req := &tg.PhoneJoinGroupCallRequest{
		Call:   call,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, ssrc),
	}
	req.SetBlock(block)
	if _, err := f.router.onPhoneJoinGroupCall(ctx, req); err != nil {
		t.Fatalf("join conference %s: %v", blockSuffix, err)
	}
}

func optionalUpdate[T tg.UpdateClass](updates tg.UpdatesClass) (T, bool) {
	box, ok := updates.(*tg.Updates)
	if !ok {
		var zero T
		return zero, false
	}
	for _, u := range box.Updates {
		if v, ok := u.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

func pushedDiscardedGroupCallUsers(records []phonePushRecord, callID int64) map[int64]bool {
	seen := map[int64]bool{}
	for _, rec := range records {
		box, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, raw := range box.Updates {
			update, ok := raw.(*tg.UpdateGroupCall)
			if !ok {
				continue
			}
			discarded, ok := update.Call.(*tg.GroupCallDiscarded)
			if ok && discarded.ID == callID {
				seen[rec.userID] = true
			}
		}
	}
	return seen
}

func TestConferenceCreateLinkAndGetBySlug(t *testing.T) {
	f := newConferenceFixture(t)
	ctx := f.userCtx(f.alice, 11)
	res, err := f.router.onPhoneCreateConferenceCall(ctx, &tg.PhoneCreateConferenceCallRequest{RandomID: 42})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	update := findUpdate[*tg.UpdateGroupCall](t, res)
	call, ok := update.Call.(*tg.GroupCall)
	if !ok || !call.Conference || call.InviteLink == "" || !strings.Contains(call.InviteLink, "slug=") || !strings.HasPrefix(call.InviteLink, "https://telesrv.net/call/") {
		t.Fatalf("created call = %#v", update.Call)
	}
	slug := conferenceSlugFromLink(t, call.InviteLink)
	got, err := f.router.onPhoneGetGroupCall(ctx, &tg.PhoneGetGroupCallRequest{
		Call:  &tg.InputGroupCallSlug{Slug: slug},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get by slug: %v", err)
	}
	if got.Call.(*tg.GroupCall).ID != call.ID {
		t.Fatalf("get by slug id = %d, want %d", got.Call.(*tg.GroupCall).ID, call.ID)
	}
	again, err := f.router.onPhoneCreateConferenceCall(ctx, &tg.PhoneCreateConferenceCallRequest{RandomID: 42})
	if err != nil {
		t.Fatalf("repeat create conference: %v", err)
	}
	if findUpdate[*tg.UpdateGroupCall](t, again).Call.(*tg.GroupCall).ID != call.ID {
		t.Fatalf("random_id must be idempotent")
	}
}

func TestConferenceExportGroupCallInviteReturnsConferenceLink(t *testing.T) {
	f := newConferenceFixture(t)
	ctx := f.userCtx(f.alice, 11)
	create, err := f.router.onPhoneCreateConferenceCall(ctx, &tg.PhoneCreateConferenceCallRequest{RandomID: 43})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	exported, err := f.router.onPhoneExportGroupCallInvite(ctx, &tg.PhoneExportGroupCallInviteRequest{
		Call: &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	if exported.Link != call.InviteLink || !strings.Contains(exported.Link, "slug=") {
		t.Fatalf("exported link = %q, want conference invite link %q", exported.Link, call.InviteLink)
	}
}

func TestConferenceExportGroupCallInviteReturnsPathSlug(t *testing.T) {
	f := newConferenceFixture(t)
	ctx := WithClientInfo(f.userCtx(f.alice, 11), ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.8.1"})
	create, err := f.router.onPhoneCreateConferenceCall(ctx, &tg.PhoneCreateConferenceCallRequest{RandomID: 44})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	slug := conferenceSlugFromLink(t, call.InviteLink)
	exported, err := f.router.onPhoneExportGroupCallInvite(ctx, &tg.PhoneExportGroupCallInviteRequest{
		Call: &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	if exported.Link != call.InviteLink {
		t.Fatalf("exported link = %q, want stored canonical invite link %q", exported.Link, call.InviteLink)
	}
	if got := lastPathSegmentFromLink(t, exported.Link); got != slug {
		t.Fatalf("export path slug = %q, want %q (link %q)", got, slug, exported.Link)
	}
	if got := conferenceSlugFromLink(t, exported.Link); got != slug {
		t.Fatalf("export query slug = %q, want %q (link %q)", got, slug, exported.Link)
	}
}

func TestConferenceLinksNormalizeLegacyTMeLink(t *testing.T) {
	const slug = "legacy_slug-1"
	legacy := domain.GroupCall{
		ID:         1,
		AccessHash: 2,
		Kind:       domain.GroupCallKindConference,
		State:      domain.GroupCallStateActive,
		Version:    1,
		InviteSlug: slug,
		InviteLink: "https://t.me/call?slug=" + slug,
	}
	want := "https://telesrv.net/call/" + slug + "?slug=" + slug
	if got := conferenceExportInviteLink(legacy); got != want {
		t.Fatalf("export link = %q, want %q", got, want)
	}
	call := tgGroupCall(legacy, 0, false).(*tg.GroupCall)
	if call.InviteLink != want {
		t.Fatalf("tg group call invite link = %q, want %q", call.InviteLink, want)
	}
}

func TestConferenceJoinBroadcastAndGetChainBlocks(t *testing.T) {
	f := newConferenceFixture(t)
	ctx := f.userCtx(f.alice, 11)
	create, err := f.router.onPhoneCreateConferenceCall(ctx, &tg.PhoneCreateConferenceCallRequest{RandomID: 77})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	join, err := f.router.onPhoneJoinGroupCall(ctx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9001),
	})
	if err != nil {
		t.Fatalf("join conference: %v", err)
	}
	findUpdate[*tg.UpdateGroupCallConnection](t, join)
	participants := findUpdate[*tg.UpdateGroupCallParticipants](t, join)
	if len(participants.Participants) != 1 || !participants.Participants[0].Self {
		t.Fatalf("participants update = %+v", participants.Participants)
	}
	block := conferenceTestBlock(conferenceBroadcastCommitConstructor, "opaque-chain-block")
	broadcast, err := f.router.onPhoneSendConferenceCallBroadcast(ctx, &tg.PhoneSendConferenceCallBroadcastRequest{
		Call:  input,
		Block: block,
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	chain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, broadcast)
	if chain.SubChainID != 1 || chain.NextOffset != 1 || len(chain.Blocks) != 1 || conferenceTestConstructor(chain.Blocks[0]) != conferenceBroadcastCommitServerConstructor {
		t.Fatalf("chain update = %+v", chain)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(ctx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, SubChainID: 1, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	got := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if got.SubChainID != 1 || len(got.Blocks) != 1 || conferenceTestConstructor(got.Blocks[0]) != conferenceBroadcastCommitServerConstructor {
		t.Fatalf("get chain blocks = %+v", got)
	}
	nextBlock := conferenceTestBlock(conferenceBroadcastRevealConstructor, "opaque-chain-block-2")
	nextBroadcast, err := f.router.onPhoneSendConferenceCallBroadcast(ctx, &tg.PhoneSendConferenceCallBroadcastRequest{
		Call:  input,
		Block: nextBlock,
	})
	if err != nil {
		t.Fatalf("second broadcast: %v", err)
	}
	nextChain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, nextBroadcast)
	if nextChain.SubChainID != 1 || nextChain.NextOffset != 2 || len(nextChain.Blocks) != 1 || conferenceTestConstructor(nextChain.Blocks[0]) != conferenceBroadcastRevealServerConstructor {
		t.Fatalf("second chain update = %+v", nextChain)
	}
	latest, err := f.router.onPhoneGetGroupCallChainBlocks(ctx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, SubChainID: 1, Offset: domain.GroupCallChainBlockLatestOffset, Limit: 1,
	})
	if err != nil {
		t.Fatalf("get latest chain block: %v", err)
	}
	gotLatest := findUpdate[*tg.UpdateGroupCallChainBlocks](t, latest)
	if gotLatest.SubChainID != 1 || gotLatest.NextOffset != 2 || len(gotLatest.Blocks) != 1 || conferenceTestConstructor(gotLatest.Blocks[0]) != conferenceBroadcastRevealServerConstructor {
		t.Fatalf("latest chain block = %+v", gotLatest)
	}
}

func TestConferenceBroadcastRequiresActiveParticipant(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 770})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	f.sessions.reset()
	block := conferenceTestBlock(conferenceBroadcastCommitConstructor, "creator-not-joined-broadcast")
	res, err := f.router.onPhoneSendConferenceCallBroadcast(aliceCtx, &tg.PhoneSendConferenceCallBroadcastRequest{
		Call:  input,
		Block: block,
	})
	if res != nil || !tgerr.Is(err, "GROUPCALL_JOIN_MISSING") {
		t.Fatalf("broadcast before join = %+v err=%v, want GROUPCALL_JOIN_MISSING", res, err)
	}
	if got := f.sessions.records(); len(got) != 0 {
		t.Fatalf("broadcast before join must not push updates, got %+v", got)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(aliceCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, SubChainID: 1, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	chain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if chain.NextOffset != 0 || len(chain.Blocks) != 0 {
		t.Fatalf("chain after forbidden broadcast = %+v, want empty", chain)
	}
}

func TestConferenceJoinBlockSeedsChainAndDuplicateJoinIsRejected(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 78})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinBlock := conferenceTestBlock(conferenceChainBlockConstructor, "join-chain-block")
	joinReq := &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9002),
	}
	joinReq.SetBlock(joinBlock)
	join, err := f.router.onPhoneJoinGroupCall(aliceCtx, joinReq)
	if err != nil {
		t.Fatalf("join conference: %v", err)
	}
	findUpdate[*tg.UpdateGroupCallConnection](t, join)
	joinChain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, join)
	if joinChain.SubChainID != 0 || joinChain.NextOffset != 1 || len(joinChain.Blocks) != 1 || conferenceTestConstructor(joinChain.Blocks[0]) != conferenceChainBlockServerConstructor {
		t.Fatalf("join chain update = %+v", joinChain)
	}

	dupJoinReq := &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9003),
	}
	dupJoinReq.SetBlock(append([]byte(nil), joinBlock...))
	dupJoin, err := f.router.onPhoneJoinGroupCall(bobCtx, dupJoinReq)
	if dupJoin != nil || !tgerr.Is(err, "CONF_WRITE_CHAIN_INVALID") {
		t.Fatalf("duplicate join = %+v err=%v, want CONF_WRITE_CHAIN_INVALID", dupJoin, err)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(aliceCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	got := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if got.NextOffset != 1 || len(got.Blocks) != 1 || conferenceTestConstructor(got.Blocks[0]) != conferenceChainBlockServerConstructor {
		t.Fatalf("get chain blocks after duplicate = %+v", got)
	}
}

func TestConferenceJoinReturnsSubmittedBlockOnly(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 790})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	aliceBlock := conferenceTestBlock(conferenceChainBlockConstructor, "alice-join-chain-block")
	aliceJoin := &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9031),
	}
	aliceJoin.SetBlock(aliceBlock)
	if _, err := f.router.onPhoneJoinGroupCall(aliceCtx, aliceJoin); err != nil {
		t.Fatalf("alice join: %v", err)
	}

	bobBlock := conferenceTestBlock(conferenceChainBlockConstructor, "bob-join-chain-block")
	bobJoin := &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9032),
	}
	bobJoin.SetBlock(bobBlock)
	join, err := f.router.onPhoneJoinGroupCall(bobCtx, bobJoin)
	if err != nil {
		t.Fatalf("bob join: %v", err)
	}
	joinChain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, join)
	if joinChain.SubChainID != 0 || joinChain.NextOffset != 2 || len(joinChain.Blocks) != 1 {
		t.Fatalf("bob join chain update = %+v, want only submitted block at next_offset=2", joinChain)
	}
	if conferenceTestConstructor(joinChain.Blocks[0]) != conferenceChainBlockServerConstructor || string(joinChain.Blocks[0][4:]) != string(bobBlock[4:]) {
		t.Fatalf("bob join block = %x, want server-form submitted block %x", joinChain.Blocks[0], bobBlock)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(aliceCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	got := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if got.NextOffset != 2 || len(got.Blocks) != 2 {
		t.Fatalf("persisted chain blocks = %+v, want full history through getGroupCallChainBlocks", got)
	}
}

func TestConferenceDuplicateJoinBlockDoesNotLeaveParticipantActive(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 79})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinBlock := conferenceTestBlock(conferenceChainBlockConstructor, "alice-join-chain-block")
	aliceJoin := &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9011),
	}
	aliceJoin.SetBlock(joinBlock)
	if _, err := f.router.onPhoneJoinGroupCall(aliceCtx, aliceJoin); err != nil {
		t.Fatalf("alice join: %v", err)
	}

	bobJoin := &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9022),
	}
	bobJoin.SetBlock(append([]byte(nil), joinBlock...))
	if res, err := f.router.onPhoneJoinGroupCall(bobCtx, bobJoin); res != nil || !tgerr.Is(err, "CONF_WRITE_CHAIN_INVALID") {
		t.Fatalf("bob duplicate join = %+v err=%v, want CONF_WRITE_CHAIN_INVALID", res, err)
	}
	if p, found, err := f.group.Participant(f.ctx, call.ID, f.bob.ID); err != nil || (found && !p.Left) {
		t.Fatalf("bob participant after duplicate join = %+v found=%v err=%v, want absent or left", p, found, err)
	}
}

func TestConferenceDeleteParticipantsReturnsSubmittedBlock(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 791})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	aliceBlock := conferenceTestBlock(conferenceChainBlockConstructor, "alice-delete-seed-block")
	aliceJoin := &tg.PhoneJoinGroupCallRequest{Call: input, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 9041)}
	aliceJoin.SetBlock(aliceBlock)
	if _, err := f.router.onPhoneJoinGroupCall(aliceCtx, aliceJoin); err != nil {
		t.Fatalf("alice join: %v", err)
	}
	bobBlock := conferenceTestBlock(conferenceChainBlockConstructor, "bob-delete-seed-block")
	bobJoin := &tg.PhoneJoinGroupCallRequest{Call: &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 9042)}
	bobJoin.SetBlock(bobBlock)
	if _, err := f.router.onPhoneJoinGroupCall(bobCtx, bobJoin); err != nil {
		t.Fatalf("bob join: %v", err)
	}

	removeBlock := conferenceTestBlock(conferenceChainBlockConstructor, "alice-remove-bob-block")
	res, err := f.router.onPhoneDeleteConferenceCallParticipants(aliceCtx, &tg.PhoneDeleteConferenceCallParticipantsRequest{
		Call:  input,
		IDs:   []int64{f.bob.ID},
		Kick:  true,
		Block: removeBlock,
	})
	if err != nil {
		t.Fatalf("delete conference participants: %v", err)
	}
	chain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, res)
	if chain.SubChainID != 0 || chain.NextOffset != 3 || len(chain.Blocks) != 1 || conferenceTestConstructor(chain.Blocks[0]) != conferenceChainBlockServerConstructor {
		t.Fatalf("delete participants chain update = %+v", chain)
	}
}

func TestConferenceOnlyLeftRemoveIsIdempotentAndAllowedForParticipant(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	carolCtx := f.userCtx(f.carol, 33)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 792})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-only-left-join", 9051)
	joinConferenceForTest(t, f, bobCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "bob-only-left-join", 9052)
	joinConferenceForTest(t, f, carolCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "carol-only-left-join", 9053)
	if _, err := f.router.onPhoneLeaveGroupCall(carolCtx, &tg.PhoneLeaveGroupCallRequest{Call: input}); err != nil {
		t.Fatalf("carol leave: %v", err)
	}

	aliceRemove := conferenceTestBlock(conferenceChainBlockConstructor, "alice-remove-left-carol")
	first, err := f.router.onPhoneDeleteConferenceCallParticipants(aliceCtx, &tg.PhoneDeleteConferenceCallParticipantsRequest{
		Call:     input,
		IDs:      []int64{f.carol.ID},
		OnlyLeft: true,
		Block:    aliceRemove,
	})
	if err != nil {
		t.Fatalf("alice only_left remove: %v", err)
	}
	firstChain := findUpdate[*tg.UpdateGroupCallChainBlocks](t, first)
	if firstChain.SubChainID != 0 || firstChain.NextOffset != 4 || len(firstChain.Blocks) != 1 {
		t.Fatalf("first only_left chain update = %+v", firstChain)
	}

	bobRemove := conferenceTestBlock(conferenceChainBlockConstructor, "bob-stale-remove-left-carol")
	second, err := f.router.onPhoneDeleteConferenceCallParticipants(bobCtx, &tg.PhoneDeleteConferenceCallParticipantsRequest{
		Call:     input,
		IDs:      []int64{f.carol.ID},
		OnlyLeft: true,
		Block:    bobRemove,
	})
	if err != nil {
		t.Fatalf("bob duplicate only_left remove: %v", err)
	}
	if chain, ok := optionalUpdate[*tg.UpdateGroupCallChainBlocks](second); ok {
		t.Fatalf("duplicate only_left must not append stale chain block, got %+v", chain)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(aliceCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	got := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if got.NextOffset != 4 || len(got.Blocks) != 4 {
		t.Fatalf("chain after duplicate only_left = %+v, want exactly 3 joins + 1 remove", got)
	}
}

func TestConferenceForbiddenKickDoesNotAppendChainBlock(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	carolCtx := f.userCtx(f.carol, 33)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 793})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-forbidden-kick-join", 9061)
	joinConferenceForTest(t, f, bobCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "bob-forbidden-kick-join", 9062)
	joinConferenceForTest(t, f, carolCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "carol-forbidden-kick-join", 9063)

	staleBlock := conferenceTestBlock(conferenceChainBlockConstructor, "bob-forbidden-kick-carol")
	res, err := f.router.onPhoneDeleteConferenceCallParticipants(bobCtx, &tg.PhoneDeleteConferenceCallParticipantsRequest{
		Call:  input,
		IDs:   []int64{f.carol.ID},
		Kick:  true,
		Block: staleBlock,
	})
	if res != nil || !tgerr.Is(err, "GROUPCALL_FORBIDDEN") {
		t.Fatalf("bob forbidden kick = %+v err=%v, want GROUPCALL_FORBIDDEN", res, err)
	}
	list, err := f.router.onPhoneGetGroupCallChainBlocks(aliceCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call: input, Offset: 0, Limit: 10,
	})
	if err != nil {
		t.Fatalf("get chain blocks: %v", err)
	}
	got := findUpdate[*tg.UpdateGroupCallChainBlocks](t, list)
	if got.NextOffset != 3 || len(got.Blocks) != 3 {
		t.Fatalf("chain after forbidden kick = %+v, want only join blocks", got)
	}
}

func TestConferenceLastLeaveDiscardsCall(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 794})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-last-leave-join", 9071)
	joinConferenceForTest(t, f, bobCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "bob-last-leave-join", 9072)

	bobLeave, err := f.router.onPhoneLeaveGroupCall(bobCtx, &tg.PhoneLeaveGroupCallRequest{Call: input})
	if err != nil {
		t.Fatalf("bob leave: %v", err)
	}
	if update, ok := optionalUpdate[*tg.UpdateGroupCall](bobLeave); ok {
		if _, discarded := update.Call.(*tg.GroupCallDiscarded); discarded {
			t.Fatalf("first leave must keep conference active, got %+v", update.Call)
		}
	}
	aliceLeave, err := f.router.onPhoneLeaveGroupCall(aliceCtx, &tg.PhoneLeaveGroupCallRequest{Call: input})
	if err != nil {
		t.Fatalf("alice last leave: %v", err)
	}
	discardUpdate := findUpdate[*tg.UpdateGroupCall](t, aliceLeave)
	if discarded, ok := discardUpdate.Call.(*tg.GroupCallDiscarded); !ok || discarded.ID != call.ID {
		t.Fatalf("last leave update = %+v, want groupCallDiscarded %d", discardUpdate.Call, call.ID)
	}
	_, err = f.router.onPhoneJoinGroupCall(bobCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 9073),
	})
	if !tgerr.Is(err, "GROUPCALL_ALREADY_DISCARDED") {
		t.Fatalf("join after last leave err = %v, want GROUPCALL_ALREADY_DISCARDED", err)
	}
}

func TestConferenceDiscardRequiresCreator(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 794})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-discard-permission-join", 9071)
	joinConferenceForTest(t, f, bobCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "bob-discard-permission-join", 9072)

	f.sessions.reset()
	res, err := f.router.onPhoneDiscardGroupCall(bobCtx, input)
	if res != nil || !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("bob discard = %+v err=%v, want CHAT_ADMIN_REQUIRED", res, err)
	}
	if got := f.sessions.records(); len(got) != 0 {
		t.Fatalf("forbidden discard must not push updates, got %+v", got)
	}
	got, err := f.router.onPhoneGetGroupCall(aliceCtx, &tg.PhoneGetGroupCallRequest{Call: input, Limit: 10})
	if err != nil {
		t.Fatalf("get group call after forbidden discard: %v", err)
	}
	if _, ok := got.Call.(*tg.GroupCall); !ok {
		t.Fatalf("call after forbidden discard = %T, want active GroupCall", got.Call)
	}
}

func TestConferenceDiscardFanoutIncludesSlugJoinedParticipants(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	carolCtx := f.userCtx(f.carol, 33)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 795})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-discard-fanout-join", 9081)
	joinConferenceForTest(t, f, bobCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "bob-discard-fanout-join", 9082)
	joinConferenceForTest(t, f, carolCtx, &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}, "carol-discard-fanout-join", 9083)

	f.sessions.reset()
	discard, err := f.router.onPhoneDiscardGroupCall(aliceCtx, input)
	if err != nil {
		t.Fatalf("discard conference: %v", err)
	}
	if _, ok := findUpdate[*tg.UpdateGroupCall](t, discard).Call.(*tg.GroupCallDiscarded); !ok {
		t.Fatalf("discard response missing groupCallDiscarded")
	}
	seen := pushedDiscardedGroupCallUsers(f.sessions.records(), call.ID)
	for _, user := range []domain.User{f.alice, f.bob, f.carol} {
		if !seen[user.ID] {
			t.Fatalf("discard fanout missing user %d, seen=%v records=%+v", user.ID, seen, f.sessions.records())
		}
	}
}

func TestConferenceDiscardAllowsHistoricalParticipantCleanupRPCs(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	carolCtx := f.userCtx(f.carol, 33)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 796})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	slugCall := &tg.InputGroupCallSlug{Slug: conferenceSlugFromLink(t, call.InviteLink)}
	joinConferenceForTest(t, f, aliceCtx, input, "alice-discard-cleanup-join", 9091)
	joinConferenceForTest(t, f, bobCtx, slugCall, "bob-discard-cleanup-join", 9092)
	joinConferenceForTest(t, f, carolCtx, slugCall, "carol-discard-cleanup-join", 9093)

	if _, err := f.router.onPhoneDiscardGroupCall(aliceCtx, input); err != nil {
		t.Fatalf("discard conference: %v", err)
	}
	got, err := f.router.onPhoneGetGroupCall(bobCtx, &tg.PhoneGetGroupCallRequest{Call: input, Limit: 10})
	if err != nil {
		t.Fatalf("bob get discarded group call: %v", err)
	}
	if _, ok := got.Call.(*tg.GroupCallDiscarded); !ok {
		t.Fatalf("bob call after discard = %T, want GroupCallDiscarded", got.Call)
	}
	ssrcs, err := f.router.onPhoneCheckGroupCall(bobCtx, &tg.PhoneCheckGroupCallRequest{
		Call:    input,
		Sources: []int{9092},
	})
	if err != nil || len(ssrcs) != 0 {
		t.Fatalf("bob check discarded group call = %v err=%v, want empty no error", ssrcs, err)
	}
	chain, err := f.router.onPhoneGetGroupCallChainBlocks(bobCtx, &tg.PhoneGetGroupCallChainBlocksRequest{
		Call:       input,
		SubChainID: 0,
		Offset:     0,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("bob get chain blocks after discard: %v", err)
	}
	if blocks := findUpdate[*tg.UpdateGroupCallChainBlocks](t, chain).Blocks; len(blocks) != 3 {
		t.Fatalf("bob cleanup chain blocks len=%d, want 3", len(blocks))
	}
}

func TestConferenceInviteMessageResolvesInputGroupCallInviteMessage(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 88})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	invite, err := f.router.onPhoneInviteConferenceCallParticipant(aliceCtx, &tg.PhoneInviteConferenceCallParticipantRequest{
		Call:   input,
		UserID: &tg.InputUser{UserID: f.bob.ID, AccessHash: f.bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("invite conference: %v", err)
	}
	msg := findUpdate[*tg.UpdateNewMessage](t, invite).Message.(*tg.MessageService)
	if _, ok := msg.Action.(*tg.MessageActionConferenceCall); !ok {
		t.Fatalf("invite action = %T", msg.Action)
	}
	history, err := f.messages.GetHistory(f.ctx, f.bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.alice.ID},
		Limit:   10,
	})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("bob history len=%d err=%v", len(history.Messages), err)
	}
	bobMsgID := history.Messages[0].ID
	joinConferenceForTest(t, f, aliceCtx, input, "alice-invite-message-broadcast-join", 9091)
	got, err := f.router.onPhoneGetGroupCall(f.userCtx(f.bob, 22), &tg.PhoneGetGroupCallRequest{
		Call:  &tg.InputGroupCallInviteMessage{MsgID: bobMsgID},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get by invite message: %v", err)
	}
	if got.Call.(*tg.GroupCall).ID != call.ID {
		t.Fatalf("invite message call id = %d, want %d", got.Call.(*tg.GroupCall).ID, call.ID)
	}
	block := conferenceTestBlock(conferenceBroadcastCommitConstructor, "invite-message-chain-block")
	if _, err := f.router.onPhoneSendConferenceCallBroadcast(aliceCtx, &tg.PhoneSendConferenceCallBroadcastRequest{
		Call:  input,
		Block: block,
	}); err != nil {
		t.Fatalf("broadcast invite message chain block: %v", err)
	}
	chain, err := f.router.onPhoneGetGroupCallChainBlocks(f.userCtx(f.bob, 22), &tg.PhoneGetGroupCallChainBlocksRequest{
		Call:       &tg.InputGroupCallInviteMessage{MsgID: bobMsgID},
		SubChainID: 1,
		Offset:     domain.GroupCallChainBlockLatestOffset,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("get latest by invite message: %v", err)
	}
	latest := findUpdate[*tg.UpdateGroupCallChainBlocks](t, chain)
	if latest.SubChainID != 1 || latest.NextOffset != 1 || len(latest.Blocks) != 1 || conferenceTestConstructor(latest.Blocks[0]) != conferenceBroadcastCommitServerConstructor {
		t.Fatalf("invite message latest block = %+v", latest)
	}
}

func TestConferenceInviteExactReplayConfirmsImmutableMessageWithoutFanout(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 89})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	request := &tg.PhoneInviteConferenceCallParticipantRequest{
		Call:   &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		UserID: &tg.InputUser{UserID: f.bob.ID, AccessHash: f.bob.AccessHash},
	}
	first, err := f.router.onPhoneInviteConferenceCallParticipant(aliceCtx, request)
	if err != nil {
		t.Fatalf("first conference invite: %v", err)
	}
	firstUpdate := findUpdate[*tg.UpdateNewMessage](t, first)
	firstMessage, ok := firstUpdate.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("first conference message = %T, want MessageService", firstUpdate.Message)
	}
	bobHistory, err := f.messages.GetHistory(f.ctx, f.bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.alice.ID},
		Limit:   10,
	})
	if err != nil || len(bobHistory.Messages) != 1 {
		t.Fatalf("bob history after first invite len=%d err=%v, want one", len(bobHistory.Messages), err)
	}
	bobMessageID := bobHistory.Messages[0].ID
	_, firstInvite, found, err := f.group.GetByInviteMessage(f.ctx, f.bob.ID, bobMessageID)
	if err != nil || !found {
		t.Fatalf("first durable invite = %+v found=%v err=%v", firstInvite, found, err)
	}

	f.sessions.reset()
	replay, err := f.router.onPhoneInviteConferenceCallParticipant(aliceCtx, request)
	if err != nil {
		t.Fatalf("replay conference invite: %v", err)
	}
	replayed := findUpdate[*tg.UpdateNewMessage](t, replay)
	replayedMessage, ok := replayed.Message.(*tg.MessageService)
	if !ok || replayedMessage.ID != firstMessage.ID || replayed.Pts != firstUpdate.Pts || replayed.PtsCount != firstUpdate.PtsCount {
		t.Fatalf("replay message = %+v (%T), want original confirmation %d pts %d/%d", replayed.Message, replayed.Message, firstMessage.ID, firstUpdate.Pts, firstUpdate.PtsCount)
	}
	mapping := findUpdate[*tg.UpdateMessageID](t, replay)
	wantRandomID := conferenceInviteRandomID(call.ID, f.bob.ID, int(f.clock.Now().Unix()))
	if mapping.ID != firstMessage.ID || mapping.RandomID != wantRandomID {
		t.Fatalf("replay mapping = %+v, want id/random_id %d/%d", mapping, firstMessage.ID, wantRandomID)
	}
	if records := f.sessions.records(); len(records) != 0 {
		t.Fatalf("replay must not fan out another invite, got %+v", records)
	}
	bobHistory, err = f.messages.GetHistory(f.ctx, f.bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.alice.ID},
		Limit:   10,
	})
	if err != nil || len(bobHistory.Messages) != 1 {
		t.Fatalf("bob history after replay len=%d err=%v, want original one", len(bobHistory.Messages), err)
	}
	_, replayInvite, found, err := f.group.GetByInviteMessage(f.ctx, f.bob.ID, bobMessageID)
	if err != nil || !found || replayInvite != firstInvite {
		t.Fatalf("durable invite after replay = %+v found=%v err=%v, want unchanged %+v", replayInvite, found, err, firstInvite)
	}

	conflict := *request
	conflict.Video = true
	if result, err := f.router.onPhoneInviteConferenceCallParticipant(aliceCtx, &conflict); result != nil || !tgerr.Is(err, "RANDOM_ID_DUPLICATE") {
		t.Fatalf("conflicting replay = %+v err=%v, want RANDOM_ID_DUPLICATE", result, err)
	}
	if records := f.sessions.records(); len(records) != 0 {
		t.Fatalf("conflicting replay must not fan out, got %+v", records)
	}
}

func TestPhoneDiscardMigrateConferenceCarriesSlug(t *testing.T) {
	f := newConferenceFixture(t)
	aliceCtx := f.userCtx(f.alice, 11)
	bobCtx := f.userCtx(f.bob, 22)
	ga, gaHash, gb := phoneTestKeys()
	requested, err := f.router.onPhoneRequestCall(aliceCtx, &tg.PhoneRequestCallRequest{
		UserID:   &tg.InputUser{UserID: f.bob.ID, AccessHash: f.bob.AccessHash},
		RandomID: 9,
		GAHash:   gaHash,
		Protocol: phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("request call: %v", err)
	}
	peer := tg.InputPhoneCall{ID: requested.PhoneCall.(*tg.PhoneCallWaiting).ID, AccessHash: requested.PhoneCall.(*tg.PhoneCallWaiting).AccessHash}
	if _, err := f.router.onPhoneAcceptCall(bobCtx, &tg.PhoneAcceptCallRequest{Peer: peer, GB: gb, Protocol: phoneTestProtocol()}); err != nil {
		t.Fatalf("accept call: %v", err)
	}
	if _, err := f.router.onPhoneConfirmCall(aliceCtx, &tg.PhoneConfirmCallRequest{Peer: peer, GA: ga, KeyFingerprint: 123, Protocol: phoneTestProtocol()}); err != nil {
		t.Fatalf("confirm call: %v", err)
	}
	create, err := f.router.onPhoneCreateConferenceCall(aliceCtx, &tg.PhoneCreateConferenceCallRequest{RandomID: 99})
	if err != nil {
		t.Fatalf("create conference: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, create).Call.(*tg.GroupCall)
	slug := conferenceSlugFromLink(t, call.InviteLink)
	updates, err := f.router.onPhoneDiscardCall(aliceCtx, &tg.PhoneDiscardCallRequest{
		Peer:     peer,
		Duration: 1,
		Reason:   &tg.PhoneCallDiscardReasonMigrateConferenceCall{Slug: slug},
	})
	if err != nil {
		t.Fatalf("discard migrate: %v", err)
	}
	discarded := findUpdate[*tg.UpdatePhoneCall](t, updates).PhoneCall.(*tg.PhoneCallDiscarded)
	reason, ok := discarded.Reason.(*tg.PhoneCallDiscardReasonMigrateConferenceCall)
	if !ok || reason.Slug != slug {
		t.Fatalf("discard reason = %#v, want slug %q", discarded.Reason, slug)
	}
}

var _ clock.Clock = (*phoneTestClock)(nil)

func conferenceSlugFromLink(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err == nil {
		if slug := u.Query().Get("slug"); slug != "" {
			return slug
		}
	}
	idx := strings.LastIndex(link, "slug=")
	if idx < 0 {
		t.Fatalf("link %q does not contain slug", link)
	}
	return link[idx+len("slug="):]
}

func conferenceTestBlock(constructor uint32, suffix string) []byte {
	block := make([]byte, 4+len(suffix))
	binary.LittleEndian.PutUint32(block[:4], constructor)
	copy(block[4:], suffix)
	return block
}

func conferenceTestConstructor(block []byte) uint32 {
	if len(block) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(block[:4])
}

func lastPathSegmentFromLink(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	segments := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(segments) == 0 || segments[len(segments)-1] == "" {
		t.Fatalf("link %q has no path segment", link)
	}
	segment, err := url.PathUnescape(segments[len(segments)-1])
	if err != nil {
		t.Fatalf("decode path segment in %q: %v", link, err)
	}
	return segment
}
