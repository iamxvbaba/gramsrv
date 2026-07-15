// Command catalogfetch 从官方 Telegram 拉取「目录/参考数据」并写成 telesrv
// internal/seed/catalog 的 JSON 种子:
//   - 国家区号   help.getCountriesList     (免登)
//   - 时区       help.getTimezonesList     (免登)
//   - emoji 分组 messages.getEmojiGroups   (需登录)
//   - emoji 关键词 messages.getEmojiKeywords (需登录)
//
// 免登项即使会话未授权也能拉;emoji 两项需要授权会话,故复用 appearancefetch 建立的
// 已登录会话(SESSION env,默认 /tmp/appearance.session)。api 凭据用 TDesktop 开源公开 id/hash。
//
// 用法: SESSION=/tmp/appearance.session catalogfetch <out_dir> [langCode=en]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"
)

const (
	tdesktopAPIID   = 17349
	tdesktopAPIHash = "344583e45741c457fe1862106095a5eb"
)

func sessionPath() string {
	if p := os.Getenv("SESSION"); p != "" {
		return p
	}
	return "/tmp/appearance.session"
}

type countryCodeJSON struct {
	CountryCode string   `json:"country_code"`
	Prefixes    []string `json:"prefixes,omitempty"`
	Patterns    []string `json:"patterns,omitempty"`
}
type countryJSON struct {
	ISO2         string            `json:"iso2"`
	DefaultName  string            `json:"default_name"`
	Name         string            `json:"name,omitempty"`
	Hidden       bool              `json:"hidden,omitempty"`
	CountryCodes []countryCodeJSON `json:"country_codes"`
}
type countriesFile struct {
	Countries []countryJSON `json:"countries"`
}

type timezoneJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UTCOffset int    `json:"utc_offset"`
}
type timezonesFile struct {
	Timezones []timezoneJSON `json:"timezones"`
}

type emojiGroupJSON struct {
	Title       string   `json:"title"`
	IconEmojiID int64    `json:"icon_emoji_id"`
	Emoticons   []string `json:"emoticons"`
}
type emojiGroupsFile struct {
	Groups []emojiGroupJSON `json:"groups"`
}

type emojiKeywordJSON struct {
	Keyword   string   `json:"keyword"`
	Emoticons []string `json:"emoticons"`
}
type emojiKeywordsFile struct {
	LangCode string             `json:"lang_code"`
	Version  int                `json:"version"`
	Keywords []emojiKeywordJSON `json:"keywords"`
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: SESSION=/tmp/appearance.session catalogfetch <out_dir> [langCode=en]")
		os.Exit(2)
	}
	out := os.Args[1]
	lang := "en"
	if len(os.Args) > 2 {
		lang = os.Args[2]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	client := telegram.NewClient(tdesktopAPIID, tdesktopAPIHash, telegram.Options{
		SessionStorage: &telegram.FileSessionStorage{Path: sessionPath()},
	})
	if err := client.Run(ctx, func(ctx context.Context) error {
		api := client.API()
		if status, err := client.Auth().Status(ctx); err == nil {
			fmt.Printf("[session %s authorized=%v]\n", sessionPath(), status.Authorized)
		}

		// 国家区号(免登)
		if cl, err := api.HelpGetCountriesList(ctx, &tg.HelpGetCountriesListRequest{LangCode: lang, Hash: 0}); err != nil {
			fmt.Fprintln(os.Stderr, "skip countries:", err)
		} else if full, ok := cl.(*tg.HelpCountriesList); ok {
			var f countriesFile
			for _, c := range full.Countries {
				cj := countryJSON{ISO2: c.ISO2, DefaultName: c.DefaultName, Name: c.Name, Hidden: c.Hidden}
				for _, cc := range c.CountryCodes {
					cj.CountryCodes = append(cj.CountryCodes, countryCodeJSON{CountryCode: cc.CountryCode, Prefixes: cc.Prefixes, Patterns: cc.Patterns})
				}
				f.Countries = append(f.Countries, cj)
			}
			if err := writeJSON(filepath.Join(out, "countries.json"), f); err != nil {
				return err
			}
			fmt.Printf("[countries] %d\n", len(f.Countries))
		}

		// 时区(免登)
		if tl, err := api.HelpGetTimezonesList(ctx, 0); err != nil {
			fmt.Fprintln(os.Stderr, "skip timezones:", err)
		} else if full, ok := tl.(*tg.HelpTimezonesList); ok {
			var f timezonesFile
			for _, t := range full.Timezones {
				f.Timezones = append(f.Timezones, timezoneJSON{ID: t.ID, Name: t.Name, UTCOffset: t.UtcOffset})
			}
			if err := writeJSON(filepath.Join(out, "timezones.json"), f); err != nil {
				return err
			}
			fmt.Printf("[timezones] %d\n", len(f.Timezones))
		}

		// emoji 分组(需登录)
		if eg, err := api.MessagesGetEmojiGroups(ctx, 0); err != nil {
			fmt.Fprintln(os.Stderr, "skip emoji_groups:", err)
		} else if full, ok := eg.(*tg.MessagesEmojiGroups); ok {
			var f emojiGroupsFile
			for _, g := range full.Groups {
				if gg, ok := g.(*tg.EmojiGroup); ok {
					f.Groups = append(f.Groups, emojiGroupJSON{Title: gg.Title, IconEmojiID: gg.IconEmojiID, Emoticons: gg.Emoticons})
				}
			}
			if err := writeJSON(filepath.Join(out, "emoji_groups.json"), f); err != nil {
				return err
			}
			fmt.Printf("[emoji_groups] %d\n", len(f.Groups))
		}

		// emoji 关键词(需登录)
		if kw, err := api.MessagesGetEmojiKeywords(ctx, lang); err != nil {
			fmt.Fprintln(os.Stderr, "skip emoji_keywords:", err)
		} else {
			f := emojiKeywordsFile{LangCode: kw.LangCode, Version: kw.Version}
			for _, k := range kw.Keywords {
				if kk, ok := k.(*tg.EmojiKeyword); ok {
					f.Keywords = append(f.Keywords, emojiKeywordJSON{Keyword: kk.Keyword, Emoticons: kk.Emoticons})
				}
			}
			name := fmt.Sprintf("emoji_keywords_%s.json", f.LangCode)
			if f.LangCode == "" {
				name = "emoji_keywords_" + lang + ".json"
			}
			if err := writeJSON(filepath.Join(out, name), f); err != nil {
				return err
			}
			fmt.Printf("[emoji_keywords %s] %d (v%d)\n", f.LangCode, len(f.Keywords), f.Version)
		}

		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
