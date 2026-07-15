// Command langpackfetch 从官方 Telegram 拉取 langpack(免登公开方法 langpack.getLangPack),
// 转成 telesrv seed 的 .strings 格式(复数用 Key#one/#other 后缀)。api 凭据用 TDesktop
// 开源公开的 id/hash。
//
// 用法:
//
//	langpackfetch languages [pack]                 列出某 pack(默认 android)的可用语言
//	langpackfetch <out_dir> <langCode> [pack...]   拉取语言包(默认 packs = android ios macos)
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"
)

const (
	tdesktopAPIID   = 17349
	tdesktopAPIHash = "344583e45741c457fe1862106095a5eb"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage:\n  langpackfetch languages [pack]\n  langpackfetch <out_dir> <langCode> [pack...]")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	client := telegram.NewClient(tdesktopAPIID, tdesktopAPIHash, telegram.Options{})
	if err := client.Run(ctx, func(ctx context.Context) error {
		api := client.API()

		if os.Args[1] == "languages" {
			pack := "android"
			if len(os.Args) > 2 {
				pack = os.Args[2]
			}
			langs, err := api.LangpackGetLanguages(ctx, pack)
			if err != nil {
				return fmt.Errorf("getLanguages %q: %w", pack, err)
			}
			fmt.Printf("pack=%s, %d languages:\n", pack, len(langs))
			for _, l := range langs {
				fmt.Printf("  %-18s %-26s official=%-5v beta=%-5v strings=%-6d translated=%-6d base=%q\n",
					l.LangCode, l.Name, l.Official, l.Beta, l.StringsCount, l.TranslatedCount, l.BaseLangCode)
			}
			return nil
		}

		outRoot := os.Args[1]
		langCode := "en"
		if len(os.Args) > 2 {
			langCode = os.Args[2]
		}
		packs := os.Args[3:]
		if len(packs) == 0 {
			packs = []string{"android", "ios", "macos"}
		}
		for _, pack := range packs {
			diff, err := api.LangpackGetLangPack(ctx, &tg.LangpackGetLangPackRequest{
				LangPack: pack,
				LangCode: langCode,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "skip %s/%s: %v\n", pack, langCode, err)
				continue
			}
			if err := writePack(outRoot, pack, diff); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func writePack(root, pack string, diff *tg.LangPackDifference) error {
	dir := filepath.Join(root, pack)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	count := 0
	for _, s := range diff.Strings {
		switch v := s.(type) {
		case *tg.LangPackString:
			fmt.Fprintf(&b, "%s = %s;\n", strconv.Quote(v.Key), strconv.Quote(v.Value))
			count++
		case *tg.LangPackStringPluralized:
			emit := func(suffix, val string) {
				if val != "" {
					fmt.Fprintf(&b, "%s = %s;\n", strconv.Quote(v.Key+suffix), strconv.Quote(val))
				}
			}
			emit("#zero", v.ZeroValue)
			emit("#one", v.OneValue)
			emit("#two", v.TwoValue)
			emit("#few", v.FewValue)
			emit("#many", v.ManyValue)
			emit("#other", v.OtherValue)
			count++
		case *tg.LangPackStringDeleted:
			// 跳过删除项
		}
	}
	langCode := diff.LangCode
	if langCode == "" {
		langCode = "unknown"
	}
	name := fmt.Sprintf("%s_%s_v%d.strings", pack, langCode, diff.Version)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d strings, version %d)\n", path, count, diff.Version)
	return nil
}
