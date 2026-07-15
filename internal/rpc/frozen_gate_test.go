package rpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type frozenGateFreezeProvider struct {
	freeze domain.AccountFreeze
	found  bool
	err    error
	calls  int
	items  map[int64]domain.AccountFreeze
}

func (p *frozenGateFreezeProvider) AccountFreeze(_ context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	p.calls++
	if p.items != nil {
		freeze, found := p.items[userID]
		return freeze, found, p.err
	}
	return p.freeze, p.found, p.err
}

type frozenGateChannels struct {
	ChannelsService
	views map[int64]domain.ChannelView
	err   error
}

func (s frozenGateChannels) ResolveChannel(_ context.Context, _ int64, channelID int64) (domain.ChannelView, error) {
	if s.err != nil {
		return domain.ChannelView{}, s.err
	}
	view, ok := s.views[channelID]
	if !ok {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	return view, nil
}

func frozenGateActiveState(userID int64) domain.AccountFreeze {
	since := time.Unix(1_700_000_000, 0).UTC()
	return domain.AccountFreeze{
		UserID:    userID,
		Frozen:    true,
		Since:     since,
		Until:     since.Add(7 * 24 * time.Hour),
		AppealURL: "https://example.test/appeal",
	}
}

func TestFrozenMethodGateIsReadOnlyAndFailsClosed(t *testing.T) {
	tests := map[string]bool{
		"help.getAppConfig":                false,
		"messages.getHistory":              false,
		"messages.searchGlobal":            false,
		"contacts.resolveUsername":         false,
		"payments.checkCanSendGift":        false,
		"messages.readHistory":             false,
		"messages.readDiscussion":          false,
		"stats.loadAsyncGraph":             false,
		"stories.incrementStoryViews":      false,
		"account.updateDeviceLocked":       false,
		"account.updateStatus":             false,
		"account.deleteAccount":            false,
		"auth.logOut":                      false,
		"messages.sendMessage":             true,
		"messages.editMessage":             true,
		"messages.deleteHistory":           true,
		"messages.forwardMessages":         true,
		"messages.sendReaction":            true,
		"channels.joinChannel":             true,
		"channels.searchPosts":             true,
		"contacts.importContacts":          true,
		"account.saveAutoDownloadSettings": true,
		"future.performNewMutation":        true,
	}
	for method, want := range tests {
		t.Run(method, func(t *testing.T) {
			if got := frozenMethodRequiresWriteGate(method); got != want {
				t.Fatalf("frozenMethodRequiresWriteGate(%q) = %v, want %v", method, got, want)
			}
		})
	}
}

func TestFrozenMethodGateIsUserScopedAcrossSessionsAndUnfreezesImmediately(t *testing.T) {
	const (
		frozenUser = int64(1001)
		otherUser  = int64(1002)
	)
	provider := &frozenGateFreezeProvider{items: map[int64]domain.AccountFreeze{
		frozenUser: frozenGateActiveState(frozenUser),
	}}
	router := &Router{deps: Deps{AccountFreeze: provider}}

	// Separate MTProto sessions for one user share the same durable account fact.
	for _, sessionID := range []int64{11, 22} {
		ctx := WithSessionID(WithUserID(context.Background(), frozenUser), sessionID)
		if err := router.checkFrozenRPC(ctx, "messages.sendMessage"); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
			t.Fatalf("session %d err = %v, want FROZEN_METHOD_INVALID", sessionID, err)
		}
	}
	if err := router.checkFrozenRPC(WithUserID(context.Background(), otherUser), "messages.sendMessage"); err != nil {
		t.Fatalf("other user was gated: %v", err)
	}

	// Unfreeze is a durable state transition; existing sessions are admitted on
	// their very next RPC without reconnecting or retaining a per-session flag.
	delete(provider.items, frozenUser)
	for _, sessionID := range []int64{11, 22} {
		ctx := WithSessionID(WithUserID(context.Background(), frozenUser), sessionID)
		if err := router.checkFrozenRPC(ctx, "messages.sendMessage"); err != nil {
			t.Fatalf("session %d remained gated after unfreeze: %v", sessionID, err)
		}
	}
}

func TestFrozenMethodGateReturns420BeforeLayerHandler(t *testing.T) {
	const userID = int64(1001)
	for _, profile := range []tg.LayerProfile{
		tg.LayerProfile225,
		tg.LayerProfile226,
		tg.LayerProfile227,
		tg.LayerProfile228,
	} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			provider := &frozenGateFreezeProvider{freeze: frozenGateActiveState(userID), found: true}
			router := New(
				Config{DC: 2, IP: "127.0.0.1", Port: 2398},
				Deps{AccountFreeze: provider},
				zaptest.NewLogger(t),
				clock.System,
			)
			body := encodeExactLayerRPC(t, profile, &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerSelf{},
				Message:  "must not reach handler",
				RandomID: 1,
			})
			admitted, err := router.AdmitLayer(profile, &body, tg.LayerDecodeLimits{})
			if err != nil {
				t.Fatal(err)
			}
			_, method, err := router.DispatchAdmitted(WithUserID(context.Background(), userID), [8]byte{1}, 10, 0, 1, admitted)
			if method != "messages.sendMessage" || !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
				t.Fatalf("DispatchAdmitted = method:%q err:%v", method, err)
			}
			rpcErr, ok := tgerr.As(err)
			if !ok || rpcErr.Code != 420 {
				t.Fatalf("RPC error = %#v, want code 420", rpcErr)
			}
			if provider.calls != 1 {
				t.Fatalf("freeze provider calls = %d, want 1", provider.calls)
			}
		})
	}
}

func TestFrozenMethodGateReturns420BeforeLegacyHandler(t *testing.T) {
	const userID = int64(1001)
	provider := &frozenGateFreezeProvider{freeze: frozenGateActiveState(userID), found: true}
	router := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{AccountFreeze: provider},
		zaptest.NewLogger(t),
		clock.System,
	)
	request := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "must not reach legacy handler",
		RandomID: 2,
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Dispatch(WithUserID(context.Background(), userID), [8]byte{1}, 10, &body); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
		t.Fatalf("legacy Dispatch err = %v, want FROZEN_METHOD_INVALID", err)
	} else if rpcErr, ok := tgerr.As(err); !ok || rpcErr.Code != 420 {
		t.Fatalf("legacy RPC error = %#v, want code 420", rpcErr)
	}
	if provider.calls != 1 {
		t.Fatalf("freeze provider calls = %d, want 1", provider.calls)
	}
}

func TestFrozenParticipantGateAllowsOnlyJoinedChannels(t *testing.T) {
	const userID = int64(1001)
	provider := &frozenGateFreezeProvider{freeze: frozenGateActiveState(userID), found: true}
	router := &Router{deps: Deps{
		AccountFreeze: provider,
		Channels: frozenGateChannels{views: map[int64]domain.ChannelView{
			10: {Self: domain.ChannelMember{UserID: userID, Status: domain.ChannelMemberActive}},
			20: {Self: domain.ChannelMember{UserID: userID, Status: domain.ChannelMemberLeft}},
			30: {Self: domain.ChannelMember{UserID: userID, Status: domain.ChannelMemberActive, Guest: true}},
		}},
	}}
	ctx := WithUserID(context.Background(), userID)
	if err := router.checkFrozenChannelParticipants(ctx, userID, 10, 10); err != nil {
		t.Fatalf("joined channel: %v", err)
	}
	for _, channelID := range []int64{20, 30} {
		if err := router.checkFrozenChannelParticipants(ctx, userID, channelID); !tgerr.Is(err, "FROZEN_PARTICIPANT_MISSING") {
			t.Fatalf("channel %d err = %v, want FROZEN_PARTICIPANT_MISSING", channelID, err)
		} else if rpcErr, ok := tgerr.As(err); !ok || rpcErr.Code != 400 {
			t.Fatalf("channel %d RPC error = %#v, want code 400", channelID, rpcErr)
		}
	}
}

func TestFrozenParticipantGateRejectsBeforeCatchupRateLimitWrite(t *testing.T) {
	const (
		userID     = int64(1001)
		channelID  = int64(20)
		accessHash = int64(20020)
	)
	provider := &frozenGateFreezeProvider{freeze: frozenGateActiveState(userID), found: true}
	limiter := &captureRateLimiter{}
	router := &Router{
		cfg: Config{CatchupRateLimit: 1, CatchupRateWindow: time.Minute},
		deps: Deps{
			AccountFreeze: provider,
			Limiter:       limiter,
			Channels: frozenGateChannels{views: map[int64]domain.ChannelView{
				channelID: {
					Channel: domain.Channel{ID: channelID, AccessHash: accessHash},
					Self:    domain.ChannelMember{UserID: userID, Status: domain.ChannelMemberLeft},
				},
			}},
		},
	}
	_, err := router.onUpdatesGetChannelDifference(
		WithUserID(context.Background(), userID),
		&tg.UpdatesGetChannelDifferenceRequest{
			Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
			Limit:   100,
		},
	)
	if !tgerr.Is(err, "FROZEN_PARTICIPANT_MISSING") {
		t.Fatalf("getChannelDifference err = %v, want FROZEN_PARTICIPANT_MISSING", err)
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("rejected frozen participant consumed rate-limit state: %+v", limiter.calls)
	}
}

func TestFrozenGatesFailClosedOnFreezeLookupError(t *testing.T) {
	provider := &frozenGateFreezeProvider{err: errors.New("database unavailable")}
	router := &Router{deps: Deps{AccountFreeze: provider}}
	if err := router.checkFrozenRPC(WithUserID(context.Background(), 1001), "messages.sendMessage"); !tgerr.Is(err, "INTERNAL_SERVER_ERROR") {
		t.Fatalf("checkFrozenRPC error = %v, want INTERNAL_SERVER_ERROR", err)
	}
}
