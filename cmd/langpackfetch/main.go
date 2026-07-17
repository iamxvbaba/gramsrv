// Command langpackfetch 从官方 Telegram 拉取 langpack(免登公开方法 langpack.getLangPack),
// 转成 telesrv seed 的 .strings 格式(复数用 Key#one/#other 后缀)。api 凭据用 TDesktop
// 开源公开的 id/hash。
//
// 用法:
//
//	langpackfetch languages [pack]                 列出某 pack(默认 android)的可用语言
//	langpackfetch all <out_dir> [pack...]           拉取所有官方语言及 manifest
//	langpackfetch <out_dir> <langCode> [pack...]   拉取语言包(默认 packs = android ios macos)
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

var (
	officialPacks = []string{"android", "android_x", "ios", "macos", "tdesktop", "weba", "webk"}
	packNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	langCodeRE    = regexp.MustCompile(`^[a-z0-9]{1,16}(?:[-_][a-z0-9]{1,16})*$`)
)

type manifest struct {
	Schema int            `json:"schema"`
	Packs  []packManifest `json:"packs"`
}

type packManifest struct {
	Name      string             `json:"name"`
	Languages []languageManifest `json:"languages"`
}

type languageManifest struct {
	LangCode        string `json:"lang_code"`
	Name            string `json:"name"`
	NativeName      string `json:"native_name"`
	BaseLangCode    string `json:"base_lang_code,omitempty"`
	PluralCode      string `json:"plural_code"`
	Official        bool   `json:"official"`
	RTL             bool   `json:"rtl,omitempty"`
	Beta            bool   `json:"beta,omitempty"`
	StringsCount    int    `json:"strings_count"`
	TranslatedCount int    `json:"translated_count"`
	TranslationsURL string `json:"translations_url,omitempty"`
	Version         int    `json:"version"`
	File            string `json:"file"`
	SHA256          string `json:"sha256"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage:\n  langpackfetch languages [pack]\n  langpackfetch all <out_dir> [pack...]\n  langpackfetch <out_dir> <langCode> [pack...]")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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
		if os.Args[1] == "all" {
			if len(os.Args) < 3 {
				return fmt.Errorf("all needs <out_dir>")
			}
			packs := os.Args[3:]
			if len(packs) == 0 {
				packs = officialPacks
			}
			return fetchAll(ctx, api, os.Args[2], packs)
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
			if _, err := writePack(outRoot, pack, diff); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func fetchAll(ctx context.Context, api *tg.Client, root string, packs []string) error {
	seen := make(map[string]struct{}, len(packs))
	result := manifest{Schema: 1}
	for _, pack := range packs {
		pack = strings.ToLower(strings.TrimSpace(pack))
		if !packNameRE.MatchString(pack) {
			return fmt.Errorf("invalid lang pack %q", pack)
		}
		if _, ok := seen[pack]; ok {
			continue
		}
		seen[pack] = struct{}{}

		languages, err := api.LangpackGetLanguages(ctx, pack)
		if err != nil {
			return fmt.Errorf("getLanguages %q: %w", pack, err)
		}
		sort.Slice(languages, func(i, j int) bool { return languages[i].LangCode < languages[j].LangCode })
		pm := packManifest{Name: pack}
		for _, language := range languages {
			if !language.Official {
				continue
			}
			code := strings.ToLower(strings.TrimSpace(language.LangCode))
			if !langCodeRE.MatchString(code) {
				return fmt.Errorf("invalid language code %q returned for %q", language.LangCode, pack)
			}
			diff, err := api.LangpackGetLangPack(ctx, &tg.LangpackGetLangPackRequest{
				LangPack: pack,
				LangCode: code,
			})
			if err != nil {
				return fmt.Errorf("getLangPack %s/%s: %w", pack, code, err)
			}
			written, err := writePack(root, pack, diff)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, written.Path)
			if err != nil {
				return err
			}
			pm.Languages = append(pm.Languages, languageManifest{
				LangCode:        code,
				Name:            language.Name,
				NativeName:      language.NativeName,
				BaseLangCode:    language.BaseLangCode,
				PluralCode:      language.PluralCode,
				Official:        language.Official,
				RTL:             language.Rtl,
				Beta:            language.Beta,
				StringsCount:    language.StringsCount,
				TranslatedCount: language.TranslatedCount,
				TranslationsURL: language.TranslationsURL,
				Version:         diff.Version,
				File:            filepath.ToSlash(rel),
				SHA256:          written.SHA256,
			})
		}
		fmt.Printf("pack %s complete: %d official languages\n", pack, len(pm.Languages))
		result.Packs = append(result.Packs, pm)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(root, "official-language-packs.json"), append(encoded, '\n'))
}

type writtenPack struct {
	Path   string
	SHA256 string
}

func writePack(root, pack string, diff *tg.LangPackDifference) (writtenPack, error) {
	pack = strings.ToLower(strings.TrimSpace(pack))
	if !packNameRE.MatchString(pack) {
		return writtenPack{}, fmt.Errorf("invalid lang pack %q", pack)
	}
	dir := filepath.Join(root, pack)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return writtenPack{}, err
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
		return writtenPack{}, fmt.Errorf("empty language code returned for %q", pack)
	}
	langCode = strings.ToLower(langCode)
	if !langCodeRE.MatchString(langCode) {
		return writtenPack{}, fmt.Errorf("invalid language code %q returned for %q", diff.LangCode, pack)
	}
	name := fmt.Sprintf("%s_%s_v%d.strings", pack, langCode, diff.Version)
	path := filepath.Join(dir, name)
	data := []byte(b.String())
	if err := writeFileAtomic(path, data); err != nil {
		return writtenPack{}, err
	}
	sum := sha256.Sum256(data)
	fmt.Printf("wrote %s (%d strings, version %d)\n", path, count, diff.Version)
	return writtenPack{Path: path, SHA256: hex.EncodeToString(sum[:])}, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".langpack-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace %q: %w (remove existing: %v)", path, err, removeErr)
		}
		if err := os.Rename(tmpName, path); err != nil {
			return err
		}
	}
	return nil
}
