package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
)

func TestAccountGetChatThemesReturnsStaticThemes(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.AccountGetChatThemesRequest{}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	got, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := got.(*tg.AccountThemesBox)
	if !ok {
		t.Fatalf("response type = %T, want *tg.AccountThemesBox", got)
	}
	themes, ok := box.Themes.(*tg.AccountThemes)
	if !ok {
		t.Fatalf("boxed response type = %T, want *tg.AccountThemes", box.Themes)
	}
	if themes.Hash == 0 || len(themes.Themes) == 0 {
		t.Fatalf("themes = %+v, want non-empty stable list", themes)
	}
}

func TestAccountGetUniqueGiftChatThemesReturnsEmptyStub(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.AccountGetUniqueGiftChatThemesRequest{Limit: 20}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	got, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := got.(*tg.AccountChatThemesBox)
	if !ok {
		t.Fatalf("response type = %T, want *tg.AccountChatThemesBox", got)
	}
	themes, ok := box.ChatThemes.(*tg.AccountChatThemes)
	if !ok {
		t.Fatalf("boxed response type = %T, want *tg.AccountChatThemes", box.ChatThemes)
	}
	if themes.Hash == 0 || len(themes.Themes) != 0 || len(themes.Chats) != 0 || len(themes.Users) != 0 {
		t.Fatalf("unique gift themes = %+v, want stable empty catalog", themes)
	}
}

func TestAccountGetWallPapersReturnsDefaultCatalog(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.AccountGetWallPapersRequest{}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	got, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := got.(*tg.AccountWallPapersBox)
	if !ok {
		t.Fatalf("response type = %T, want *tg.AccountWallPapersBox", got)
	}
	wallpapers, ok := box.WallPapers.(*tg.AccountWallPapers)
	if !ok {
		t.Fatalf("boxed response type = %T, want *tg.AccountWallPapers", box.WallPapers)
	}
	if wallpapers.Hash == 0 || len(wallpapers.Wallpapers) == 0 {
		t.Fatalf("wallpapers = %+v, want stable default catalog", wallpapers)
	}
}

func TestAccountWallpaperSeedLookupAndAckRPCs(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	var listReq bin.Buffer
	if err := (&tg.AccountGetWallPapersRequest{}).Encode(&listReq); err != nil {
		t.Fatalf("encode list request: %v", err)
	}
	listGot, err := r.Dispatch(ctx, [8]byte{}, 0, &listReq)
	if err != nil {
		t.Fatalf("dispatch list: %v", err)
	}
	list := listGot.(*tg.AccountWallPapersBox).WallPapers.(*tg.AccountWallPapers)
	first := list.Wallpapers[0].(*tg.WallPaper)
	input := &tg.InputWallPaper{ID: first.ID, AccessHash: first.AccessHash}

	var oneReq bin.Buffer
	if err := (&tg.AccountGetWallPaperRequest{Wallpaper: input}).Encode(&oneReq); err != nil {
		t.Fatalf("encode getWallPaper request: %v", err)
	}
	oneGot, err := r.Dispatch(ctx, [8]byte{}, 0, &oneReq)
	if err != nil {
		t.Fatalf("dispatch getWallPaper: %v", err)
	}
	oneBox, ok := oneGot.(*tg.WallPaperBox)
	if !ok {
		t.Fatalf("getWallPaper = %T, want *tg.WallPaperBox", oneGot)
	}
	oneWallpaper, ok := oneBox.WallPaper.(*tg.WallPaper)
	if !ok {
		t.Fatalf("getWallPaper boxed = %T, want *tg.WallPaper", oneBox.WallPaper)
	}
	if oneWallpaper.ID != first.ID {
		t.Fatalf("getWallPaper id = %d, want %d", oneWallpaper.ID, first.ID)
	}

	nofileInput := &tg.InputWallPaperNoFile{ID: 930000000000000001}
	var nofileReq bin.Buffer
	if err := (&tg.AccountGetWallPaperRequest{Wallpaper: nofileInput}).Encode(&nofileReq); err != nil {
		t.Fatalf("encode getWallPaper nofile request: %v", err)
	}
	nofileGot, err := r.Dispatch(ctx, [8]byte{}, 0, &nofileReq)
	if err != nil {
		t.Fatalf("dispatch getWallPaper nofile: %v", err)
	}
	nofileBox, ok := nofileGot.(*tg.WallPaperBox)
	if !ok {
		t.Fatalf("getWallPaper nofile = %T, want *tg.WallPaperBox", nofileGot)
	}
	nofileWallpaper, ok := nofileBox.WallPaper.(*tg.WallPaperNoFile)
	if !ok || nofileWallpaper.ID != nofileInput.ID {
		t.Fatalf("getWallPaper nofile boxed = %T %#v, want no-file id", nofileBox.WallPaper, nofileBox.WallPaper)
	}

	var multiReq bin.Buffer
	if err := (&tg.AccountGetMultiWallPapersRequest{Wallpapers: []tg.InputWallPaperClass{
		input,
		&tg.InputWallPaperSlug{Slug: first.Slug},
		nofileInput,
	}}).Encode(&multiReq); err != nil {
		t.Fatalf("encode getMultiWallPapers request: %v", err)
	}
	multiGot, err := r.Dispatch(ctx, [8]byte{}, 0, &multiReq)
	if err != nil {
		t.Fatalf("dispatch getMultiWallPapers: %v", err)
	}
	if vector, ok := multiGot.(*tg.WallPaperClassVector); !ok || len(vector.Elems) != 3 {
		t.Fatalf("getMultiWallPapers = %T %#v, want 3 wallpapers", multiGot, multiGot)
	}

	for name, request := range map[string]bin.Encoder{
		"save":           &tg.AccountSaveWallPaperRequest{Wallpaper: input},
		"install":        &tg.AccountInstallWallPaperRequest{Wallpaper: input},
		"save_nofile":    &tg.AccountSaveWallPaperRequest{Wallpaper: nofileInput},
		"install_nofile": &tg.AccountInstallWallPaperRequest{Wallpaper: nofileInput},
		"reset":          &tg.AccountResetWallPapersRequest{},
	} {
		var encoded bin.Buffer
		if err := request.Encode(&encoded); err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		got, err := r.Dispatch(ctx, [8]byte{}, 0, &encoded)
		if err != nil {
			t.Fatalf("dispatch %s: %v", name, err)
		}
		box, ok := got.(*tg.BoolBox)
		if !ok {
			t.Fatalf("%s = %T, want *tg.BoolBox", name, got)
		}
		if _, ok := box.Bool.(*tg.BoolTrue); !ok {
			t.Fatalf("%s boxed = %T, want *tg.BoolTrue", name, box.Bool)
		}
	}

	var badReq bin.Buffer
	if err := (&tg.AccountGetWallPaperRequest{Wallpaper: &tg.InputWallPaper{ID: first.ID, AccessHash: first.AccessHash + 1}}).Encode(&badReq); err != nil {
		t.Fatalf("encode bad getWallPaper request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &badReq); err == nil || !strings.Contains(err.Error(), "WALLPAPER_INVALID") {
		t.Fatalf("bad getWallPaper err = %v, want WALLPAPER_INVALID", err)
	}
}

func TestPaymentsGetStarGiftCollectionsNoServiceFallbackAndValidatesPeer(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	var okReq bin.Buffer
	if err := (&tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}}).Encode(&okReq); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &okReq)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := got.(*tg.PaymentsStarGiftCollectionsBox)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarGiftCollectionsBox", got)
	}
	collections, ok := box.StarGiftCollections.(*tg.PaymentsStarGiftCollections)
	if !ok {
		t.Fatalf("boxed response type = %T, want *tg.PaymentsStarGiftCollections", box.StarGiftCollections)
	}
	if len(collections.Collections) != 0 {
		t.Fatalf("collections = %+v, want empty list", collections.Collections)
	}

	var badReq bin.Buffer
	if err := (&tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerEmpty{}}).Encode(&badReq); err != nil {
		t.Fatalf("encode bad request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &badReq); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("bad peer err = %v, want PEER_ID_INVALID", err)
	}
}

func TestPaymentsGetStarsRevenueAdsAccountURLReturnsCompatURLAndValidatesPeer(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	var okReq bin.Buffer
	if err := (&tg.PaymentsGetStarsRevenueAdsAccountURLRequest{Peer: &tg.InputPeerSelf{}}).Encode(&okReq); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &okReq)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	url, ok := got.(*tg.PaymentsStarsRevenueAdsAccountURL)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarsRevenueAdsAccountURL", got)
	}
	if url.URL != "https://ads.telegram.org/" {
		t.Fatalf("url = %q, want ads compat URL", url.URL)
	}

	var badReq bin.Buffer
	if err := (&tg.PaymentsGetStarsRevenueAdsAccountURLRequest{Peer: &tg.InputPeerEmpty{}}).Encode(&badReq); err != nil {
		t.Fatalf("encode bad request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &badReq); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("bad peer err = %v, want PEER_ID_INVALID", err)
	}
}

func TestPaymentsGetStarsRevenueStatsReturnsZeroStubAndValidatesPeer(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	var okReq bin.Buffer
	req := &tg.PaymentsGetStarsRevenueStatsRequest{Ton: true, Peer: &tg.InputPeerSelf{}}
	req.SetFlags()
	if err := req.Encode(&okReq); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	got, err := r.Dispatch(ctx, [8]byte{}, 0, &okReq)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	stats, ok := got.(*tg.PaymentsStarsRevenueStats)
	if !ok {
		t.Fatalf("response type = %T, want *tg.PaymentsStarsRevenueStats", got)
	}
	if _, ok := stats.Status.CurrentBalance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("current balance = %T, want *tg.StarsTonAmount", stats.Status.CurrentBalance)
	}
	if _, ok := stats.RevenueGraph.(*tg.StatsGraphError); !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraphError", stats.RevenueGraph)
	}

	var badReq bin.Buffer
	if err := (&tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerEmpty{}}).Encode(&badReq); err != nil {
		t.Fatalf("encode bad request: %v", err)
	}
	if _, err := r.Dispatch(ctx, [8]byte{}, 0, &badReq); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("bad peer err = %v, want PEER_ID_INVALID", err)
	}
}
