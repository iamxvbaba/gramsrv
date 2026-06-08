package rpc

import (
	"context"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
)

func TestLangpackGetLanguagesCurrentAndLegacy(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	t.Run("current layer", func(t *testing.T) {
		var in bin.Buffer
		if err := (&tg.LangpackGetLanguagesRequest{LangPack: "tdesktop"}).Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		assertLangpackLanguages(t, r, context.Background(), &in)
	})

	t.Run("legacy android no args", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(legacyLangpackGetLanguagesTypeID)
		ctx := WithClientInfo(context.Background(), ClientInfo{
			DeviceModel: "Android",
			AppVersion:  "12.7.3",
			LangCode:    "en",
		})
		assertLangpackLanguages(t, r, ctx, &in)
	})
}

func TestLegacyLangpackGetLangPack(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(legacyLangpackGetLangPackTypeID)
	in.PutString("en")
	enc, err := r.Dispatch(androidClientContext(), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy langpack.getLangPack: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var diff tg.LangPackDifference
	if err := diff.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if diff.LangCode != "en" {
		t.Fatalf("difference = %+v, want lang_code=en", diff)
	}
}

func TestLegacyLangpackGetStrings(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(legacyLangpackGetStringsTypeID)
	in.PutString("en")
	in.PutVectorHeader(1)
	in.PutString("lng_intro_about")
	enc, err := r.Dispatch(androidClientContext(), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy langpack.getStrings: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var strings tg.LangPackStringClassVector
	if err := strings.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertLangpackLanguages(t *testing.T, r *Router, ctx context.Context, in *bin.Buffer) {
	t.Helper()
	enc, err := r.Dispatch(ctx, [8]byte{}, 0, in)
	if err != nil {
		t.Fatalf("dispatch langpack.getLanguages: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var langs tg.LangPackLanguageVector
	if err := langs.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(langs.Elems) == 0 || langs.Elems[0].LangCode != "en" {
		t.Fatalf("languages = %+v, want English entry", langs.Elems)
	}
}

func androidClientContext() context.Context {
	return WithClientInfo(context.Background(), ClientInfo{
		DeviceModel: "Android",
		AppVersion:  "12.7.3",
		LangCode:    "en",
	})
}
