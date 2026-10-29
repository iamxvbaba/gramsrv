package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type fakeMainTabs map[domain.Peer]string

func (f fakeMainTabs) SetMainTab(_ context.Context, peer domain.Peer, tab string) error {
	if tab == "" {
		delete(f, peer)
	} else {
		f[peer] = tab
	}
	return nil
}

func (f fakeMainTabs) MainTab(_ context.Context, peer domain.Peer) (string, error) {
	return f[peer], nil
}

func TestProfileMainTabRoundTrip(t *testing.T) {
	r, _, _ := newMediaTestRouter(t)
	store := fakeMainTabs{}
	r.deps.MainTabs = store
	ctx := context.Background()
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 9003}

	if _, ok := r.mainTabFor(ctx, channel); ok {
		t.Fatal("unset main tab must stay absent so clients keep their default")
	}
	for _, tab := range []tg.ProfileTabClass{
		&tg.ProfileTabPosts{}, &tg.ProfileTabGifts{}, &tg.ProfileTabMedia{}, &tg.ProfileTabFiles{},
		&tg.ProfileTabMusic{}, &tg.ProfileTabVoice{}, &tg.ProfileTabLinks{}, &tg.ProfileTabGifs{},
	} {
		if err := r.saveChannelMainTab(ctx, channel.ID, tab); err != nil {
			t.Fatalf("save %T: %v", tab, err)
		}
		got, ok := r.mainTabFor(ctx, channel)
		if !ok || got.TypeID() != tab.TypeID() {
			t.Fatalf("main tab after saving %T = %T (%v)", tab, got, ok)
		}
	}
	if err := r.saveChannelMainTab(ctx, channel.ID, nil); err == nil {
		t.Fatal("a missing tab must be rejected")
	}
	// Without a store the setting is accepted and the default tab is kept.
	r.deps.MainTabs = nil
	if err := r.saveChannelMainTab(ctx, channel.ID, &tg.ProfileTabMedia{}); err != nil {
		t.Fatalf("save without store: %v", err)
	}
	if _, ok := r.mainTabFor(ctx, channel); ok {
		t.Fatal("no store must mean no main_tab")
	}
}
