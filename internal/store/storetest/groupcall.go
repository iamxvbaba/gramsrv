// Package storetest provides store behavior contract cases shared across implementations
// (memory/postgres). Group call semantics (monotonic version, rejoin upsert, ssrc uniqueness,
// join_muted policy) are a drift-prone area between the two stores: any semantics change must
// run this contract against both implementations plus the PG integration (TELESRV_TEST_POSTGRES_DSN).
package storetest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store"
)

// baseNow is the test time base. It must use the current instant: integration tests may share
// the same database with a running dev server, and an ancient last_check_date would be swept by
// the live sweeper (45s TTL) as a ghost participant (version++/count--), polluting assertions.
func baseNow() int {
	return int(time.Now().Unix())
}

func conferenceInviteLink(slug string) string {
	return links.Build(links.DefaultPublicBaseURL, "call/"+slug, url.Values{"slug": []string{slug}})
}

func contractInviteMessageID(namespace int64, suffix int) int {
	// group_call_invites has a global (invitee_user_id, message_id) uniqueness
	// invariant because private box IDs are per-user. Keep contract fixtures in a
	// bounded, namespace-derived range so filtered/reordered test runs cannot collide.
	return int(namespace%1_000_000)*1000 + suffix
}

// GroupCallStoreFactory provides a clean store and a non-colliding channel id for each case.
type GroupCallStoreFactory func(t *testing.T) (st store.GroupCallStore, channelID int64)

// RunGroupCallStoreContract runs all group call store contract cases.
func RunGroupCallStoreContract(t *testing.T, factory GroupCallStoreFactory) {
	t.Helper()
	t.Run("CreateAndActiveUniq", func(t *testing.T) { contractCreateActiveUniq(t, factory) })
	t.Run("JoinVersionMonotonic", func(t *testing.T) { contractJoinVersion(t, factory) })
	t.Run("SSRCDuplicateAndRejoin", func(t *testing.T) { contractSSRC(t, factory) })
	t.Run("JoinMutedPolicy", func(t *testing.T) { contractJoinMuted(t, factory) })
	t.Run("TouchAndSweep", func(t *testing.T) { contractTouchSweep(t, factory) })
	t.Run("DiscardClearsParticipants", func(t *testing.T) { contractDiscard(t, factory) })
	t.Run("ListPagination", func(t *testing.T) { contractListPagination(t, factory) })
	t.Run("UpdateParticipant", func(t *testing.T) { contractUpdateParticipant(t, factory) })
	t.Run("ResetAllParticipants", func(t *testing.T) { contractReset(t, factory) })
	t.Run("JoinVideoStateLifecycle", func(t *testing.T) { contractJoinVideoState(t, factory) })
	t.Run("ConferenceChainBlocks", func(t *testing.T) { contractConferenceChainBlocks(t, factory) })
	t.Run("ConferenceInviteIdempotency", func(t *testing.T) { contractConferenceInviteIdempotency(t, factory) })
	t.Run("ConferenceRecipientsTerminalAccess", func(t *testing.T) { contractConferenceRecipientsTerminalAccess(t, factory) })
	t.Run("ConferenceEmptyDiscards", func(t *testing.T) { contractConferenceEmptyDiscards(t, factory) })
}

func contractConferenceInviteIdempotency(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	now := baseNow()
	slug := fmt.Sprintf("contract-invite-idempotency-%d", channelID)
	call, err := st.CreateConferenceCall(ctx, domain.GroupCall{
		ID: channelID*100 + 62, AccessHash: channelID*100 + 69, CreatorUserID: 1,
		InviteSlug: slug, InviteLink: conferenceInviteLink(slug),
		RandomID: channelID*100 + 62, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create conference call: %v", err)
	}
	want := domain.GroupCallInvite{
		CallID: call.ID, InviterUserID: 1, InviteeUserID: 4, MessageID: contractInviteMessageID(channelID, 402),
		Status: domain.GroupCallInvitePending, Video: true, CreatedAt: now + 1,
	}
	first, err := st.CreateConferenceInvite(ctx, want)
	if err != nil {
		t.Fatalf("create conference invite: %v", err)
	}
	replay, err := st.CreateConferenceInvite(ctx, want)
	if err != nil {
		t.Fatalf("replay conference invite: %v", err)
	}
	if replay != first {
		t.Fatalf("replayed conference invite = %+v, want unchanged %+v", replay, first)
	}
	gotCall, gotInvite, found, err := st.GetGroupCallByInviteMessage(ctx, want.InviteeUserID, want.MessageID)
	if err != nil || !found || gotCall.ID != call.ID || gotInvite != first {
		t.Fatalf("invite lookup = call %+v invite %+v found=%v err=%v, want call %d invite %+v", gotCall, gotInvite, found, err, call.ID, first)
	}
}

func newContractCall(t *testing.T, st store.GroupCallStore, channelID, id int64) domain.GroupCall {
	t.Helper()
	call, err := st.CreateGroupCall(context.Background(), domain.GroupCall{
		ID: id, AccessHash: id + 7, ChannelID: channelID, CreatorUserID: 1, CreatedAt: baseNow(),
	})
	if err != nil {
		t.Fatalf("create group call: %v", err)
	}
	return call
}

func join(t *testing.T, st store.GroupCallStore, callID, userID, ssrc int64, now int) domain.GroupCallMutation {
	t.Helper()
	mut, err := st.JoinGroupCall(context.Background(), domain.JoinGroupCallRequest{
		CallID: callID, UserID: userID, SSRC: ssrc, Now: now,
	})
	if err != nil {
		t.Fatalf("join call %d user %d: %v", callID, userID, err)
	}
	return mut
}

func contractCreateActiveUniq(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	if call.Version != 1 || !call.Active() {
		t.Fatalf("created call = %+v", call)
	}
	// Second call creation in the same channel: the unique-active constraint.
	if _, err := st.CreateGroupCall(ctx, domain.GroupCall{ID: channelID*100 + 2, AccessHash: 9, ChannelID: channelID, CreatorUserID: 1, CreatedAt: baseNow()}); !errors.Is(err, domain.ErrGroupCallAlreadyStarted) {
		t.Fatalf("second active call err = %v, want ErrGroupCallAlreadyStarted", err)
	}
	// A new call can be created after discard.
	if _, _, err := st.DiscardGroupCall(ctx, call.ID, baseNow()+500); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := st.CreateGroupCall(ctx, domain.GroupCall{ID: channelID*100 + 3, AccessHash: 9, ChannelID: channelID, CreatorUserID: 1, CreatedAt: baseNow() + 501}); err != nil {
		t.Fatalf("create after discard: %v", err)
	}
}

func contractJoinVersion(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	call := newContractCall(t, st, channelID, channelID*100+1)
	// Each participant change increments version by exactly 1.
	v := call.Version
	for i := int64(1); i <= 3; i++ {
		mut := join(t, st, call.ID, 100+i, 9000+i, baseNow()+int(i))
		if mut.Call.Version != v+int(i) {
			t.Fatalf("join %d version = %d, want %d", i, mut.Call.Version, v+int(i))
		}
		if mut.Call.ParticipantsCount != int(i) {
			t.Fatalf("join %d count = %d, want %d", i, mut.Call.ParticipantsCount, i)
		}
	}
	mut, err := st.LeaveGroupCall(context.Background(), call.ID, 101, baseNow()+100)
	if err != nil || mut.Call.Version != v+4 || mut.Call.ParticipantsCount != 2 || !mut.Participant.Left {
		t.Fatalf("leave = %+v err=%v", mut, err)
	}
	// Duplicate leave.
	if _, err := st.LeaveGroupCall(context.Background(), call.ID, 101, baseNow()+101); !errors.Is(err, domain.ErrGroupCallNotJoined) {
		t.Fatalf("double leave err = %v, want ErrGroupCallNotJoined", err)
	}
}

func contractSSRC(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	join(t, st, call.ID, 101, 7001, now)
	// Another user collides on ssrc.
	if _, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 102, SSRC: 7001, Now: now + 1}); !errors.Is(err, domain.ErrGroupCallSSRCDuplicate) {
		t.Fatalf("ssrc duplicate err = %v, want ErrGroupCallSSRCDuplicate", err)
	}
	// Self rejoin with a new ssrc: join_date is preserved and count stays the same.
	mut := join(t, st, call.ID, 101, 7002, now+500)
	if mut.Participant.SSRC != 7002 || mut.Participant.JoinDate != now || mut.Call.ParticipantsCount != 1 {
		t.Fatalf("rejoin = %+v", mut)
	}
	// Rejoin after leaving: join_date is reset and count is restored.
	if _, err := st.LeaveGroupCall(ctx, call.ID, 101, now+600); err != nil {
		t.Fatalf("leave: %v", err)
	}
	mut = join(t, st, call.ID, 101, 7003, now+700)
	if mut.Participant.JoinDate != now+700 || mut.Call.ParticipantsCount != 1 || mut.Participant.Left {
		t.Fatalf("rejoin after leave = %+v", mut)
	}
	// The old ssrc is available to others after release.
	if _, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 102, SSRC: 7001, Now: now + 800}); err != nil {
		t.Fatalf("reuse released ssrc: %v", err)
	}
}

// contractJoinVideoState: join replaces VideoJSON wholesale, and rejoin clears presentation
// (after the main connection rejoins the client resends joinGroupCallPresentation, so the old
// screen registration must be invalidated).
func contractJoinVideoState(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	video1 := []byte(`{"endpoint":"audio-9001","active":false}`)
	mut, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{
		CallID: call.ID, UserID: 101, SSRC: 9001, VideoJSON: video1, Now: now,
	})
	if err != nil || string(mut.Participant.VideoJSON) != string(video1) {
		t.Fatalf("join video_json = %s err=%v", mut.Participant.VideoJSON, err)
	}
	// During sharing: presentation_json is persisted.
	pres := []byte(`{"endpoint":"presentation-9100","active":true,"audio_source":9100}`)
	if _, changed, err := st.UpdateParticipant(ctx, call.ID, 101, domain.GroupCallParticipantUpdate{
		PresentationJSON: &pres, Now: now + 1,
	}); err != nil || !changed {
		t.Fatalf("set presentation changed=%v err=%v", changed, err)
	}
	// rejoin: video_json is replaced wholesale and presentation_json must be cleared.
	video2 := []byte(`{"endpoint":"audio-9002","active":true}`)
	mut, err = st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{
		CallID: call.ID, UserID: 101, SSRC: 9002, VideoJSON: video2, Now: now + 2,
	})
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if string(mut.Participant.VideoJSON) != string(video2) {
		t.Fatalf("rejoin video_json = %s, want replaced", mut.Participant.VideoJSON)
	}
	if len(mut.Participant.PresentationJSON) != 0 {
		t.Fatalf("rejoin presentation_json = %s, want cleared", mut.Participant.PresentationJSON)
	}
	// Field-level update that clears presentation (the leaveGroupCallPresentation path).
	if _, changed, err := st.UpdateParticipant(ctx, call.ID, 101, domain.GroupCallParticipantUpdate{
		PresentationJSON: &pres, Now: now + 3,
	}); err != nil || !changed {
		t.Fatalf("re-set presentation changed=%v err=%v", changed, err)
	}
	empty := []byte(nil)
	mut2, changed, err := st.UpdateParticipant(ctx, call.ID, 101, domain.GroupCallParticipantUpdate{
		PresentationJSON: &empty, Now: now + 4,
	})
	if err != nil || !changed || len(mut2.Participant.PresentationJSON) != 0 {
		t.Fatalf("clear presentation = %s changed=%v err=%v", mut2.Participant.PresentationJSON, changed, err)
	}
}

func contractJoinMuted(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	if _, _, err := st.SetGroupCallJoinMuted(ctx, call.ID, true); err != nil {
		t.Fatalf("set join muted: %v", err)
	}
	// WARNING P1-4: under join_muted, an ordinary member has muted=true and muted_by_admin=true (cannot unmute themselves).
	mut, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 201, SSRC: 8001, Now: baseNow()})
	if err != nil || !mut.Participant.Muted || !mut.Participant.MutedByAdmin {
		t.Fatalf("plain member under join_muted = %+v err=%v", mut.Participant, err)
	}
	// Admins are unaffected by join_muted.
	mut, err = st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 202, SSRC: 8002, IsAdmin: true, Now: baseNow()})
	if err != nil || mut.Participant.Muted || mut.Participant.MutedByAdmin {
		t.Fatalf("admin under join_muted = %+v err=%v", mut.Participant, err)
	}
}

func contractTouchSweep(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	join(t, st, call.ID, 101, 7001, now)
	join(t, st, call.ID, 102, 7002, now)

	ssrcs, joined, err := st.TouchParticipant(ctx, call.ID, 101, now+40)
	if err != nil || !joined || len(ssrcs) != 1 || ssrcs[0] != 7001 {
		t.Fatalf("touch = %v joined=%v err=%v", ssrcs, joined, err)
	}
	if _, joined, err := st.TouchParticipant(ctx, call.ID, 999, now+40); err != nil || joined {
		t.Fatalf("touch non-member joined=%v err=%v, want false (client rejoins based on the empty set)", joined, err)
	}
	// 101 refreshed its watermark at now+40 while 102 stayed at now: cutoff=now+20 clears only 102.
	muts, err := st.SweepStaleParticipants(ctx, now+20, now+50, 10)
	if err != nil || len(muts) != 1 || muts[0].Participant.UserID != 102 || !muts[0].Participant.Left {
		t.Fatalf("sweep = %+v err=%v", muts, err)
	}
	if muts[0].Call.ParticipantsCount != 1 {
		t.Fatalf("sweep count = %d, want 1", muts[0].Call.ParticipantsCount)
	}
	// Idempotent.
	if muts, err := st.SweepStaleParticipants(ctx, now+20, now+51, 10); err != nil || len(muts) != 0 {
		t.Fatalf("second sweep = %+v err=%v", muts, err)
	}
}

func contractDiscard(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	join(t, st, call.ID, 101, 7001, now)
	join(t, st, call.ID, 102, 7002, now+1)
	discarded, active, err := st.DiscardGroupCall(ctx, call.ID, call.CreatedAt+500)
	if err != nil || discarded.Active() || len(active) != 2 {
		t.Fatalf("discard = %+v active=%d err=%v", discarded, len(active), err)
	}
	if discarded.Duration != 500 || discarded.ParticipantsCount != 0 {
		t.Fatalf("discarded snapshot = %+v", discarded)
	}
	if _, _, err := st.DiscardGroupCall(ctx, call.ID, call.CreatedAt+501); !errors.Is(err, domain.ErrGroupCallDiscarded) {
		t.Fatalf("double discard err = %v, want ErrGroupCallDiscarded", err)
	}
	// A terminal call rejects join.
	if _, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 103, SSRC: 7003, Now: now + 502}); !errors.Is(err, domain.ErrGroupCallDiscarded) {
		t.Fatalf("join discarded err = %v, want ErrGroupCallDiscarded", err)
	}
}

func contractListPagination(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	for i := int64(1); i <= 5; i++ {
		join(t, st, call.ID, 300+i, 9100+i, now+int(i))
	}
	page, err := st.ListParticipants(ctx, call.ID, "", 2)
	if err != nil || page.Count != 5 || len(page.Participants) != 2 || page.NextOffset == "" {
		t.Fatalf("page1 = %+v err=%v", page, err)
	}
	if page.Version == 0 {
		t.Fatalf("page must carry current version (client full reload on skipped number depends on it)")
	}
	seen := map[int64]bool{}
	for _, p := range page.Participants {
		seen[p.UserID] = true
	}
	page2, err := st.ListParticipants(ctx, call.ID, page.NextOffset, 10)
	if err != nil || len(page2.Participants) != 3 {
		t.Fatalf("page2 = %+v err=%v", page2, err)
	}
	for _, p := range page2.Participants {
		if seen[p.UserID] {
			t.Fatalf("pagination overlap on user %d", p.UserID)
		}
	}
}

func contractUpdateParticipant(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	mut := join(t, st, call.ID, 101, 7001, now)
	v := mut.Call.Version
	muted := true
	out, changed, err := st.UpdateParticipant(ctx, call.ID, 101, domain.GroupCallParticipantUpdate{Muted: &muted, Now: now + 100})
	if err != nil || !changed || !out.Participant.Muted || out.Call.Version != v+1 {
		t.Fatalf("update = %+v changed=%v err=%v", out, changed, err)
	}
	// No change leaves version untouched.
	out, changed, err = st.UpdateParticipant(ctx, call.ID, 101, domain.GroupCallParticipantUpdate{Muted: &muted, Now: now + 101})
	if err != nil || changed || out.Call.Version != v+1 {
		t.Fatalf("noop update = %+v changed=%v err=%v", out, changed, err)
	}
	// A member not in the call.
	if _, _, err := st.UpdateParticipant(ctx, call.ID, 999, domain.GroupCallParticipantUpdate{Muted: &muted}); !errors.Is(err, domain.ErrGroupCallNotJoined) {
		t.Fatalf("update non-member err = %v, want ErrGroupCallNotJoined", err)
	}
}

func contractReset(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	join(t, st, call.ID, 101, 7001, now)
	mut := join(t, st, call.ID, 102, 7002, now+1)
	v := mut.Call.Version
	calls, err := st.ResetAllParticipants(ctx, now+100)
	if err != nil || len(calls) != 1 {
		t.Fatalf("reset = %d calls err=%v", len(calls), err)
	}
	if calls[0].ParticipantsCount != 0 || calls[0].Version != v+1 {
		t.Fatalf("reset call = %+v, want count=0 version=%d", calls[0], v+1)
	}
	// After a restart cleanup, a client touch returns not-in-call -> triggering rejoin.
	if _, joined, err := st.TouchParticipant(ctx, call.ID, 101, now+101); err != nil || joined {
		t.Fatalf("touch after reset joined=%v err=%v, want false", joined, err)
	}
}

func contractConferenceChainBlocks(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	now := baseNow()
	slug := fmt.Sprintf("contract-chain-%d", channelID)
	call, err := st.CreateConferenceCall(ctx, domain.GroupCall{
		ID: channelID*100 + 51, AccessHash: channelID*100 + 58, CreatorUserID: 1,
		InviteSlug: slug, InviteLink: conferenceInviteLink(slug),
		RandomID: channelID*100 + 51, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create conference call: %v", err)
	}
	firstBlock := []byte("same-chain-block")
	first, err := st.AppendGroupCallChainBlock(ctx, domain.GroupCallChainBlock{
		CallID: call.ID, SubChainID: 0, Offset: -1, Block: firstBlock, CreatedAt: now,
	})
	if err != nil || first.Offset != 0 {
		t.Fatalf("append first chain block = %+v err=%v", first, err)
	}
	dup, err := st.AppendGroupCallChainBlock(ctx, domain.GroupCallChainBlock{
		CallID: call.ID, SubChainID: 0, Offset: -1, Block: append([]byte(nil), firstBlock...), CreatedAt: now + 1,
	})
	if !errors.Is(err, domain.ErrConferenceChainInvalid) {
		t.Fatalf("append duplicate chain block = %+v err=%v, want ErrConferenceChainInvalid", dup, err)
	}
	secondBlock := []byte("next-chain-block")
	second, err := st.AppendGroupCallChainBlock(ctx, domain.GroupCallChainBlock{
		CallID: call.ID, SubChainID: 0, Offset: -1, Block: secondBlock, CreatedAt: now + 2,
	})
	if err != nil || second.Offset != 1 {
		t.Fatalf("append second chain block = %+v err=%v", second, err)
	}
	if _, err := st.AppendGroupCallChainBlock(ctx, domain.GroupCallChainBlock{
		CallID: call.ID, SubChainID: 0, Offset: 0, Block: []byte("stale-offset-block"), CreatedAt: now + 3,
	}); !errors.Is(err, domain.ErrConferenceChainInvalid) {
		t.Fatalf("append stale offset chain block err=%v, want ErrConferenceChainInvalid", err)
	}
	page, err := st.ListGroupCallChainBlocks(ctx, call.ID, 0, 0, 10)
	if err != nil || page.NextOffset != 2 || len(page.Blocks) != 2 {
		t.Fatalf("list chain blocks = %+v err=%v", page, err)
	}
	latest, err := st.ListGroupCallChainBlocks(ctx, call.ID, 0, domain.GroupCallChainBlockLatestOffset, 1)
	if err != nil || latest.NextOffset != 2 || len(latest.Blocks) != 1 || string(latest.Blocks[0].Block) != string(secondBlock) {
		t.Fatalf("latest chain block = %+v err=%v", latest, err)
	}
}

func contractConferenceRecipientsTerminalAccess(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	now := baseNow()
	slug := fmt.Sprintf("contract-recipient-%d", channelID)
	call, err := st.CreateConferenceCall(ctx, domain.GroupCall{
		ID: channelID*100 + 61, AccessHash: channelID*100 + 68, CreatorUserID: 1,
		InviteSlug: slug, InviteLink: conferenceInviteLink(slug),
		RandomID: channelID*100 + 61, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create conference call: %v", err)
	}
	join(t, st, call.ID, 2, 7102, now+1)
	join(t, st, call.ID, 3, 7103, now+2)
	if _, err := st.LeaveGroupCall(ctx, call.ID, 3, now+3); err != nil {
		t.Fatalf("leave historical participant: %v", err)
	}
	if _, err := st.CreateConferenceInvite(ctx, domain.GroupCallInvite{
		CallID: call.ID, InviterUserID: 1, InviteeUserID: 4, MessageID: contractInviteMessageID(channelID, 401),
		Status: domain.GroupCallInvitePending, CreatedAt: now + 4,
	}); err != nil {
		t.Fatalf("create pending invite: %v", err)
	}
	if _, err := st.CreateConferenceInvite(ctx, domain.GroupCallInvite{
		CallID: call.ID, InviterUserID: 1, InviteeUserID: 5, MessageID: contractInviteMessageID(channelID, 501),
		Status: domain.GroupCallInviteDeclined, CreatedAt: now + 5, UpdatedAt: now + 5,
	}); err != nil {
		t.Fatalf("create declined invite: %v", err)
	}
	activeRecipients, err := st.ListConferenceRecipientUserIDs(ctx, call.ID)
	if err != nil {
		t.Fatalf("active recipients: %v", err)
	}
	if want := []int64{1, 2, 4}; !reflect.DeepEqual(activeRecipients, want) {
		t.Fatalf("active recipients = %v, want %v", activeRecipients, want)
	}
	if _, _, err := st.DiscardGroupCall(ctx, call.ID, now+10); err != nil {
		t.Fatalf("discard conference: %v", err)
	}
	discardedRecipients, err := st.ListConferenceRecipientUserIDs(ctx, call.ID)
	if err != nil {
		t.Fatalf("discarded recipients: %v", err)
	}
	if want := []int64{1, 2, 3, 4, 5}; !reflect.DeepEqual(discardedRecipients, want) {
		t.Fatalf("discarded recipients = %v, want %v", discardedRecipients, want)
	}
}

func contractConferenceEmptyDiscards(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	now := baseNow()
	emptySlug := fmt.Sprintf("contract-empty-%d", channelID)
	call, err := st.CreateConferenceCall(ctx, domain.GroupCall{
		ID: channelID*100 + 71, AccessHash: channelID*100 + 78, CreatorUserID: 1,
		InviteSlug: emptySlug,
		InviteLink: conferenceInviteLink(emptySlug),
		RandomID:   channelID*100 + 71,
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("create conference call: %v", err)
	}
	join(t, st, call.ID, 1, 7201, now+1)
	join(t, st, call.ID, 2, 7202, now+2)
	firstLeave, err := st.LeaveGroupCall(ctx, call.ID, 2, now+3)
	if err != nil {
		t.Fatalf("first leave conference: %v", err)
	}
	if !firstLeave.Call.Active() || firstLeave.Call.ParticipantsCount != 1 {
		t.Fatalf("first leave call = %+v, want still active with one participant", firstLeave.Call)
	}
	lastLeave, err := st.LeaveGroupCall(ctx, call.ID, 1, now+4)
	if err != nil {
		t.Fatalf("last leave conference: %v", err)
	}
	if lastLeave.Call.Active() || lastLeave.Call.ParticipantsCount != 0 || lastLeave.Call.DiscardedAt != now+4 {
		t.Fatalf("last leave call = %+v, want discarded empty conference", lastLeave.Call)
	}
	if _, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: 3, SSRC: 7203, Now: now + 5}); !errors.Is(err, domain.ErrGroupCallDiscarded) {
		t.Fatalf("join empty discarded conference err = %v, want ErrGroupCallDiscarded", err)
	}

	resetSlug := fmt.Sprintf("contract-reset-empty-%d", channelID)
	resetCall, err := st.CreateConferenceCall(ctx, domain.GroupCall{
		ID: channelID*100 + 81, AccessHash: channelID*100 + 88, CreatorUserID: 1,
		InviteSlug: resetSlug,
		InviteLink: conferenceInviteLink(resetSlug),
		RandomID:   channelID*100 + 81,
		CreatedAt:  now + 10,
	})
	if err != nil {
		t.Fatalf("create reset conference call: %v", err)
	}
	join(t, st, resetCall.ID, 1, 7301, now+11)
	reset, err := st.ResetAllParticipants(ctx, now+12)
	if err != nil || len(reset) != 1 {
		t.Fatalf("reset conferences = %+v err=%v, want one affected call", reset, err)
	}
	if reset[0].ID != resetCall.ID || reset[0].Active() || reset[0].ParticipantsCount != 0 {
		t.Fatalf("reset conference call = %+v, want discarded empty conference", reset[0])
	}
}

// The following are the M2 contracts: per-viewer overrides and the raise-hand sequence number
// (callable separately from RunGroupCallStoreContract to avoid changing the existing signature --
// the two implementations' tests each call RunGroupCallStoreM2Contract).

// RunGroupCallStoreM2Contract runs the overrides and raise-hand contracts.
func RunGroupCallStoreM2Contract(t *testing.T, factory GroupCallStoreFactory) {
	t.Helper()
	t.Run("ParticipantOverrides", func(t *testing.T) { contractOverrides(t, factory) })
	t.Run("RaiseHandRatingMonotonic", func(t *testing.T) { contractRaiseHand(t, factory) })
}

func contractOverrides(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	now := baseNow()
	join(t, st, call.ID, 101, 7001, now)
	join(t, st, call.ID, 102, 7002, now+1)

	// Write an override: setter view only, does not touch the call version.
	before, _, err := st.GetGroupCall(ctx, call.ID)
	if err != nil {
		t.Fatalf("get call: %v", err)
	}
	if err := st.SetParticipantOverride(ctx, call.ID, 101, 102, domain.GroupCallParticipantOverride{MutedByYou: true, Volume: 5000}, false); err != nil {
		t.Fatalf("set override: %v", err)
	}
	after, _, err := st.GetGroupCall(ctx, call.ID)
	if err != nil || after.Version != before.Version {
		t.Fatalf("override must not bump version: %d → %d err=%v", before.Version, after.Version, err)
	}
	ov, found, err := st.GetParticipantOverride(ctx, call.ID, 101, 102)
	if err != nil || !found || !ov.MutedByYou || ov.Volume != 5000 {
		t.Fatalf("override = %+v found=%v err=%v", ov, found, err)
	}
	// Directionality: setter<->target are not interchangeable.
	if _, found, _ := st.GetParticipantOverride(ctx, call.ID, 102, 101); found {
		t.Fatalf("override must be directional")
	}
	// Clear.
	if err := st.SetParticipantOverride(ctx, call.ID, 101, 102, domain.GroupCallParticipantOverride{}, true); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if _, found, _ := st.GetParticipantOverride(ctx, call.ID, 101, 102); found {
		t.Fatalf("override must be cleared")
	}
}

func contractRaiseHand(t *testing.T, factory GroupCallStoreFactory) {
	st, channelID := factory(t)
	ctx := context.Background()
	call := newContractCall(t, st, channelID, channelID*100+1)
	prev := int64(0)
	for i := 0; i < 5; i++ {
		rating, err := st.NextRaiseHandRating(ctx, call.ID)
		if err != nil {
			t.Fatalf("next rating: %v", err)
		}
		if rating <= prev {
			t.Fatalf("rating %d not monotonic after %d (raise-hand ordering depends on monotonicity)", rating, prev)
		}
		prev = rating
	}
}
