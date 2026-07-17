package rpc

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"

	applangpack "telesrv/internal/app/langpack"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestLangpackGetLanguagesCurrentAndLegacy(t *testing.T) {
	r := newSeededLangpackRouter(t)

	t.Run("current layer", func(t *testing.T) {
		var in bin.Buffer
		if err := (&tg.LangpackGetLanguagesRequest{LangPack: "tdesktop"}).Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		langs := dispatchLangpackLanguages(t, r, context.Background(), &in)
		assertHasLangpackLanguage(t, langs, "en")
		assertNoLangpackLanguage(t, langs, "fa")
	})

	t.Run("legacy android no args", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x800fd57d)
		ctx := WithClientInfo(context.Background(), ClientInfo{
			DeviceModel: "Android",
			AppVersion:  "12.7.3",
			LangCode:    "en",
		})
		langs := dispatchLangpackLanguages(t, r, ctx, &in)
		assertHasLangpackLanguage(t, langs, "en")
		assertHasLangpackLanguage(t, langs, "fa")
	})

	t.Run("legacy android from cached client type", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x800fd57d)
		ctx := WithClientInfo(context.Background(), ClientInfo{Type: ClientTypeAndroid})
		langs := dispatchLangpackLanguages(t, r, ctx, &in)
		assertHasLangpackLanguage(t, langs, "fa")
	})
}

func TestLangpackGetLanguage(t *testing.T) {
	r := newSeededLangpackRouter(t)

	var in bin.Buffer
	if err := (&tg.LangpackGetLanguageRequest{LangPack: "tdesktop", LangCode: "zh-hans"}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(context.Background(), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch langpack.getLanguage: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var lang tg.LangPackLanguage
	if err := lang.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if lang.LangCode != "zh-hans" || lang.PluralCode != "zh" {
		t.Fatalf("language = %+v, want zh-hans", lang)
	}

	raw, err := r.langpackLanguage(context.Background(), "tdesktop", "en-raw")
	if err != nil {
		t.Fatalf("language(en-raw): %v", err)
	}
	if raw.LangCode != "en" {
		t.Fatalf("language(en-raw) = %+v, want en", raw)
	}
	if _, err := r.langpackLanguage(context.Background(), "tdesktop", "zh"); err == nil || !strings.Contains(err.Error(), "LANG_CODE_NOT_SUPPORTED") {
		t.Fatalf("language(zh) error = %v, want LANG_CODE_NOT_SUPPORTED", err)
	}
}

func TestLangpackAndroidPersianLanguage(t *testing.T) {
	r := newSeededLangpackRouter(t)

	lang, err := r.langpackLanguage(context.Background(), "android", "fa")
	if err != nil {
		t.Fatalf("android fa language: %v", err)
	}
	if lang.LangCode != "fa" || lang.PluralCode != "fa" || lang.NativeName != "فارسی" || !lang.Rtl {
		t.Fatalf("android fa language = %+v", lang)
	}

	languages, err := r.langpackLanguages(androidClientContext(), "")
	if err != nil {
		t.Fatalf("android languages: %v", err)
	}
	for _, item := range languages {
		if item.LangCode == "fa" {
			return
		}
	}
	t.Fatalf("android languages = %+v, want fa entry", languages)
}

func TestLegacyLangpackGetLangPack(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(0x9ab5c58e)
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
	in.PutID(0x2e1ee318)
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

func TestLegacyUpdatesGetDifference(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	var flags bin.Fields
	flags.Set(0)
	var in bin.Buffer
	in.PutID(0x25939651)
	if err := flags.Encode(&in); err != nil {
		t.Fatalf("encode flags: %v", err)
	}
	in.PutInt(10)
	in.PutInt(100)
	in.PutInt(123456)
	in.PutInt(0)

	enc, err := r.Dispatch(WithUserID(androidClientContext(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy updates.getDifference: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var diff tg.UpdatesDifferenceEmpty
	if err := diff.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestLegacyAccountRegisterDevice(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	in.PutID(0x637ea878)
	in.PutInt(2)
	in.PutString("android-fcm-token")

	enc, err := r.Dispatch(WithUserID(androidClientContext(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch legacy account.registerDevice: %v", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	ok, err := tg.DecodeBool(&out)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := ok.(*tg.BoolTrue); !ok {
		t.Fatalf("legacy account.registerDevice = false, want true")
	}
}

func newSeededLangpackRouter(t *testing.T) *Router {
	t.Helper()
	return New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		LangPack: seededLangPackService(t),
	}, zaptest.NewLogger(t), clock.System)
}

func seededLangPackService(t testing.TB) LangPackService {
	t.Helper()
	ctx := context.Background()
	store := memory.NewLangPackStore()
	for _, pack := range []domain.LangPack{
		{
			LangPack: "tdesktop",
			LangCode: "en",
			Version:  1,
			Strings:  []domain.LangPackString{{Key: "lng_language_name", Value: "English"}},
		},
		{
			LangPack: "tdesktop",
			LangCode: "zh-hans",
			Version:  1,
			Strings:  []domain.LangPackString{{Key: "lng_language_name", Value: "简体中文"}},
		},
		{
			LangPack: "android",
			LangCode: "en",
			Version:  1,
			Strings:  []domain.LangPackString{{Key: "LanguageName", Value: "English"}},
		},
		{
			LangPack: "android",
			LangCode: "fa",
			Version:  1,
			Strings: []domain.LangPackString{
				{Key: "LanguageName", Value: "انگلیسی"},
				{Key: "TranslateLanguageFA", Value: "فارسی"},
			},
		},
	} {
		if err := store.UpsertPack(ctx, pack); err != nil {
			t.Fatalf("seed %s/%s: %v", pack.LangPack, pack.LangCode, err)
		}
	}
	return applangpack.NewService(store)
}

func dispatchLangpackLanguages(t *testing.T, r *Router, ctx context.Context, in *bin.Buffer) []tg.LangPackLanguage {
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
	return langs.Elems
}

func assertHasLangpackLanguage(t *testing.T, langs []tg.LangPackLanguage, code string) {
	t.Helper()
	for _, lang := range langs {
		if lang.LangCode == code {
			return
		}
	}
	t.Fatalf("languages = %+v, want %s entry", langs, code)
}

func assertNoLangpackLanguage(t *testing.T, langs []tg.LangPackLanguage, code string) {
	t.Helper()
	for _, lang := range langs {
		if lang.LangCode == code {
			t.Fatalf("languages = %+v, want no %s entry", langs, code)
		}
	}
}

func androidClientContext() context.Context {
	return WithClientInfo(context.Background(), ClientInfo{
		DeviceModel: "Android",
		AppVersion:  "12.7.3",
		LangCode:    "en",
	})
}
