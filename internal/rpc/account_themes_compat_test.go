package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/domain"
)

// TestLegacyThemeWireDispatch 验证 DrKLO theme 构造器经过 Router.Dispatch 的完整链路：
// layerwire 结构预检 -> gotd dispatcher fallback -> compat 解码 -> 现有 handler。
// 不能只直调 tryLegacyThemeRPC，否则会掩盖预检早于 fallback 的路由回归。
func TestLegacyThemeWireDispatch(t *testing.T) {
	const userID = 1000010
	ctx := WithUserID(context.Background(), userID)
	var authKeyID [8]byte
	authKeyID[0] = 1
	const sessionID = 99
	files := &fakeFiles{docs: map[int64]domain.Document{
		777: {ID: 777, AccessHash: 7, DCID: 2, MimeType: "application/x-tgtheme-android", Size: 4096},
	}}
	r := newThemeRouter(t, files)

	// createTheme 0x8432c21f:flags(document+single settings) + slug + title。
	var cb bin.Buffer
	cb.PutID(legacyCreateThemeID)
	cb.PutInt32((1 << 2) | (1 << 3))
	cb.PutString("") // empty slug → auto
	cb.PutString("Legacy Theme")
	(&tg.InputDocument{ID: 777, AccessHash: 7}).Encode(&cb)
	(&tg.InputThemeSettings{BaseTheme: &tg.BaseThemeDay{}, AccentColor: 0x3997d3}).Encode(&cb)

	enc, err := r.Dispatch(ctx, authKeyID, sessionID, &cb)
	if err != nil {
		t.Fatalf("createTheme legacy dispatch: %v", err)
	}
	th, ok := enc.(*tg.Theme)
	if !ok {
		t.Fatalf("createTheme legacy result = %T, want *tg.Theme", enc)
	}
	if th.Slug == "" || !th.GetCreator() {
		t.Fatalf("created theme = slug %q creator %v, want auto slug + creator", th.Slug, th.GetCreator())
	}
	if doc, ok := th.GetDocument(); !ok {
		t.Fatalf("created theme missing document")
	} else if d, _ := doc.(*tg.Document); d == nil || d.ID != 777 {
		t.Fatalf("created theme document = %#v, want id 777", doc)
	}
	if settings, ok := th.GetSettings(); !ok || len(settings) != 1 || settings[0].AccentColor != 0x3997d3 {
		t.Fatalf("created theme settings = %#v ok=%v, want one legacy setting", settings, ok)
	}
	mustEncodeTheme(t, th)
	slug := th.Slug

	// updateTheme 0x5cb367d5:flags(=2,title) + format + InputTheme + title。
	var ub bin.Buffer
	ub.PutID(legacyUpdateThemeID)
	ub.PutInt32(1 << 1)
	ub.PutString("android")
	(&tg.InputTheme{ID: th.ID, AccessHash: th.AccessHash}).Encode(&ub)
	ub.PutString("Legacy Theme Updated")

	enc, err = r.Dispatch(ctx, authKeyID, sessionID, &ub)
	if err != nil {
		t.Fatalf("updateTheme legacy dispatch: %v", err)
	}
	updated, ok := enc.(*tg.Theme)
	if !ok || updated.Title != "Legacy Theme Updated" {
		t.Fatalf("updateTheme legacy result = %#v, want updated title", enc)
	}

	// getTheme 0x8d9d742b:format + InputThemeSlug + document_id(被忽略)。
	var gb bin.Buffer
	gb.PutID(legacyGetThemeID)
	gb.PutString("android")
	(&tg.InputThemeSlug{Slug: slug}).Encode(&gb)
	gb.PutLong(12345) // document_id ignored

	enc, err = r.Dispatch(ctx, authKeyID, sessionID, &gb)
	if err != nil {
		t.Fatalf("getTheme legacy dispatch: %v", err)
	}
	got, ok := enc.(*tg.Theme)
	if !ok || got.Slug != slug {
		t.Fatalf("getTheme legacy result = %#v, want theme slug %q", enc, slug)
	}
	if _, ok := got.GetDocument(); !ok {
		t.Fatalf("getTheme by slug missing document → client ThemeNotSupported")
	}

	// installTheme 0x7ae43737:flags(dark@bit0 + bit1 gates format+theme)。
	var ib bin.Buffer
	ib.PutID(legacyInstallThemeID)
	ib.PutInt32((1 << 0) | (1 << 1)) // dark + has format/theme
	ib.PutString("android")
	(&tg.InputThemeSlug{Slug: slug}).Encode(&ib)

	enc, err = r.Dispatch(ctx, authKeyID, sessionID, &ib)
	if err != nil {
		t.Fatalf("installTheme legacy dispatch: %v", err)
	}
	if _, ok := enc.(*tg.BoolTrue); !ok {
		t.Fatalf("installTheme legacy result = %T, want *tg.BoolTrue", enc)
	}

	// 已声明 legacy 方法仍必须精确消费完整结构，截断字段不能到手写 decoder。
	var malformed bin.Buffer
	malformed.PutID(legacyCreateThemeID)
	malformed.PutInt32(0)
	malformed.PutString("slug") // missing title
	if _, err := r.Dispatch(ctx, authKeyID, sessionID, &malformed); !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("malformed legacy theme err = %v, want INPUT_REQUEST_INVALID", err)
	}
}

func TestUnknownRPCReachesCompatibilityTraceAfterOpaquePreflight(t *testing.T) {
	const unknownID = uint32(0x12345678)
	r := newThemeRouter(t, &fakeFiles{})
	r.deps.Auth = &captureAuthService{}
	core, logs := observer.New(zap.WarnLevel)
	r.log = zap.New(core)

	var b bin.Buffer
	b.PutID(unknownID)
	// Deliberately resembles a forged vector count. Because the constructor is unknown, the
	// body remains opaque and is never decoded or allocated from; total frame/RPC budgets bound it.
	b.PutUint32(0xffffffff)
	if _, err := r.Dispatch(context.Background(), [8]byte{1}, 101, &b); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("unknown dispatch err = %v, want NOT_IMPLEMENTED", err)
	}
	entries := logs.FilterMessage("Unhandled RPC (compatibility trace)").All()
	if len(entries) != 1 {
		t.Fatalf("compatibility trace entries = %d, want 1", len(entries))
	}
	if got, ok := entries[0].ContextMap()["type_id"]; !ok || got != "0x12345678" {
		t.Fatalf("trace type_id = %#v, want %#x", got, unknownID)
	}
}
