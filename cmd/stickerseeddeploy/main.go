package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type deployStats struct {
	SpecialSets int
	StickerSets int
	EmojiSets   int
	Reactions   int
}

type setCatalog struct {
	Sets []struct {
		MetadataFile string `json:"metadata_file"`
	} `json:"sets"`
}

type specialCatalogEntry struct {
	Key          string `json:"key"`
	MetadataFile string `json:"metadata_file"`
}

var specialSeedDirs = map[string]string{
	"animated_emoji":            "DefaultSet_AnimatedEmoji",
	"animated_emoji_animations": "DefaultSet_AnimatedEmojiAnimations",
	"emoji_generic_animations":  "DefaultSet_EmojiGenericAnimations",
	"emoji_default_statuses":    "DefaultSet_EmojiDefaultStatuses",
	"emoji_default_topic_icons": "DefaultSet_EmojiDefaultTopicIcons",
	"premium_gifts":             "DefaultSet_PremiumGifts",
}

func main() {
	source := flag.String("source", "", "HSgram premium promo repository checkout")
	dest := flag.String("dest", "data/sticker-seed", "telesrv sticker seed destination")
	clean := flag.Bool("clean", true, "remove managed seed subdirectories before deploying")
	flag.Parse()
	if *source == "" && flag.NArg() > 0 {
		*source = flag.Arg(0)
	}
	if *source == "" {
		fatalf("usage: stickerseeddeploy -source /path/to/HSgram_-premium-promo [-dest data/sticker-seed]")
	}
	stats, err := deploy(*source, *dest, *clean)
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("deployed sticker seed to %s: special_sets=%d sticker_sets=%d emoji_sets=%d reactions=%d\n",
		*dest, stats.SpecialSets, stats.StickerSets, stats.EmojiSets, stats.Reactions)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func deploy(source, dest string, clean bool) (deployStats, error) {
	catalogRoot := filepath.Join(source, "telegram_official_catalog")
	if st, err := os.Stat(catalogRoot); err != nil || !st.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", catalogRoot)
		}
		return deployStats{}, err
	}
	if clean {
		for _, rel := range []string{
			"telegram_default_stickers_export",
			"telegram_stickers_export",
			"telegram_emoji_export",
			"telegram_reactions_export",
		} {
			if err := os.RemoveAll(filepath.Join(dest, rel)); err != nil {
				return deployStats{}, err
			}
		}
	}
	var stats deployStats
	if n, err := deploySpecialSets(catalogRoot, dest); err != nil {
		return stats, err
	} else {
		stats.SpecialSets = n
	}
	if n, err := deployCatalogSets(filepath.Join(catalogRoot, "featured_stickers"), filepath.Join(dest, "telegram_stickers_export")); err != nil {
		return stats, err
	} else {
		stats.StickerSets = n
	}
	if n, err := deployCatalogSets(filepath.Join(catalogRoot, "featured_emoji_stickers"), filepath.Join(dest, "telegram_emoji_export")); err != nil {
		return stats, err
	} else {
		stats.EmojiSets = n
	}
	if n, err := deployReactions(filepath.Join(catalogRoot, "reactions"), filepath.Join(dest, "telegram_reactions_export")); err != nil {
		return stats, err
	} else {
		stats.Reactions = n
	}
	return stats, nil
}

func deploySpecialSets(catalogRoot, dest string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(catalogRoot, "special_sets", "catalog.json"))
	if err != nil {
		return 0, err
	}
	var entries []specialCatalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return 0, fmt.Errorf("parse special_sets/catalog.json: %w", err)
	}
	seen := map[string]bool{}
	count := 0
	for _, entry := range entries {
		dstName, ok := specialSeedDirs[entry.Key]
		if !ok || seen[dstName] {
			continue
		}
		seen[dstName] = true
		srcSetDir := filepath.Dir(filepath.Join(catalogRoot, "special_sets", entry.MetadataFile))
		dstSetDir := filepath.Join(dest, "telegram_default_stickers_export", dstName)
		if err := deploySet(srcSetDir, dstSetDir); err != nil {
			return count, fmt.Errorf("deploy special set %s: %w", entry.Key, err)
		}
		count++
	}
	return count, nil
}

func deployCatalogSets(srcCategoryDir, dstCategoryDir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(srcCategoryDir, "catalog.json"))
	if err != nil {
		return 0, err
	}
	var catalog setCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Join(srcCategoryDir, "catalog.json"), err)
	}
	count := 0
	for _, item := range catalog.Sets {
		if item.MetadataFile == "" {
			continue
		}
		srcSetDir := filepath.Dir(filepath.Join(srcCategoryDir, item.MetadataFile))
		dstSetDir := filepath.Join(dstCategoryDir, filepath.Base(srcSetDir))
		if err := deploySet(srcSetDir, dstSetDir); err != nil {
			return count, fmt.Errorf("deploy set %s: %w", filepath.Base(srcSetDir), err)
		}
		count++
	}
	return count, nil
}

func deploySet(srcSetDir, dstSetDir string) error {
	meta, err := readNormalizedJSON(filepath.Join(srcSetDir, "metadata.json"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dstSetDir, "stickers"), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dstSetDir, "set_info.json"), map[string]any{"result": meta}); err != nil {
		return err
	}
	return copyDir(filepath.Join(srcSetDir, "files"), filepath.Join(dstSetDir, "stickers"))
}

func deployReactions(srcDir, dstDir string) (int, error) {
	meta, err := readNormalizedJSON(filepath.Join(srcDir, "metadata.json"))
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Join(dstDir, "global_json"), 0o755); err != nil {
		return 0, err
	}
	if err := writeJSON(filepath.Join(dstDir, "global_json", "available_reactions_raw.json"), map[string]any{"result": meta}); err != nil {
		return 0, err
	}
	if err := copyDir(filepath.Join(srcDir, "files"), filepath.Join(dstDir, "reactions")); err != nil {
		return 0, err
	}
	if m, ok := meta.(map[string]any); ok {
		if reactions, ok := m["reactions"].([]any); ok {
			return len(reactions), nil
		}
	}
	return 0, nil
}

func readNormalizedJSON(path string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return normalizeSeedJSON(value)
}

func normalizeSeedJSON(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["file_reference_hex"]; ok {
			v["file_reference"] = ref
			delete(v, "file_reference_hex")
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "bytes" {
				if s, ok := v[k].(string); ok {
					hexBytes, converted, err := pythonBytesLiteralToHex(s)
					if err != nil {
						return nil, err
					}
					if converted {
						v[k] = hexBytes
					}
					continue
				}
			}
			child, err := normalizeSeedJSON(v[k])
			if err != nil {
				return nil, err
			}
			v[k] = child
		}
	case []any:
		for i := range v {
			child, err := normalizeSeedJSON(v[i])
			if err != nil {
				return nil, err
			}
			v[i] = child
		}
	}
	return value, nil
}

func pythonBytesLiteralToHex(s string) (string, bool, error) {
	if len(s) < 3 || s[0] != 'b' || (s[1] != '\'' && s[1] != '"') {
		return s, false, nil
	}
	quote := s[1]
	if s[len(s)-1] != quote {
		return "", false, fmt.Errorf("invalid python bytes literal: %q", s)
	}
	inner := s[2 : len(s)-1]
	out := make([]byte, 0, len(inner))
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' {
			out = append(out, inner[i])
			continue
		}
		i++
		if i >= len(inner) {
			out = append(out, '\\')
			break
		}
		switch c := inner[i]; c {
		case '\\', '\'', '"':
			out = append(out, c)
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, '\v')
		case 'x':
			if i+2 >= len(inner) {
				return "", false, fmt.Errorf("short hex escape in python bytes literal")
			}
			b, err := strconv.ParseUint(inner[i+1:i+3], 16, 8)
			if err != nil {
				return "", false, err
			}
			out = append(out, byte(b))
			i += 2
		default:
			if c >= '0' && c <= '7' {
				start := i
				for i+1 < len(inner) && i-start < 2 && inner[i+1] >= '0' && inner[i+1] <= '7' {
					i++
				}
				b, err := strconv.ParseUint(inner[start:i+1], 8, 8)
				if err != nil {
					return "", false, err
				}
				out = append(out, byte(b))
				continue
			}
			out = append(out, c)
		}
	}
	return hex.EncodeToString(out), true, nil
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return nil
}
