package langpack

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

var tdesktopStringRE = regexp.MustCompile(`(?s)"((?:\\.|[^"\\])*)"\s*=\s*"((?:\\.|[^"\\])*)";`)
var langPackNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var langCodeRE = regexp.MustCompile(`^[a-z0-9]{1,16}(?:-[a-z0-9]{1,16})*$`)

// ParseTDesktopFile 解析客户端 .strings 文件为 domain 语言包。
func ParseTDesktopFile(path string) (domain.LangPack, error) {
	pack, err := packFromFilename(path)
	if err != nil {
		return domain.LangPack{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("read langpack %q: %w", path, err)
	}

	plain := make([]domain.LangPackString, 0)
	plurals := make(map[string]*domain.LangPackString)
	pluralOrder := make([]string, 0)
	for _, match := range tdesktopStringRE.FindAllStringSubmatch(string(data), -1) {
		key := unquoteTDesktop(match[1])
		value := unquoteTDesktop(match[2])
		base, plural := splitPluralKey(key)
		if plural == "" {
			plain = append(plain, domain.LangPackString{Key: key, Value: value})
			continue
		}
		item, ok := plurals[base]
		if !ok {
			plurals[base] = &domain.LangPackString{Key: base, Pluralized: true}
			item = plurals[base]
			pluralOrder = append(pluralOrder, base)
		}
		setPluralValue(item, plural, value)
	}

	pack.Strings = make([]domain.LangPackString, 0, len(plain)+len(pluralOrder))
	pack.Strings = append(pack.Strings, plain...)
	for _, key := range pluralOrder {
		pack.Strings = append(pack.Strings, *plurals[key])
	}
	return pack, nil
}

func packFromFilename(path string) (domain.LangPack, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	idx := strings.LastIndex(name, "_v")
	if idx <= 0 || idx+2 >= len(name) {
		return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
	}
	version, err := strconv.Atoi(name[idx+2:])
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("parse langpack version %q: %w", name[idx+2:], err)
	}

	head := name[:idx]
	dirPack := filepath.Base(filepath.Dir(path))
	prefix := dirPack + "_"
	langPack := ""
	langCode := ""
	if dirPack != "." && dirPack != "" && strings.HasPrefix(head, prefix) {
		langPack = dirPack
		langCode = strings.TrimPrefix(head, prefix)
	} else {
		sep := strings.Index(head, "_")
		if sep <= 0 || sep+1 >= len(head) {
			return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
		}
		langPack = head[:sep]
		langCode = head[sep+1:]
	}
	if langPack == "" || langCode == "" {
		return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
	}
	langPack = normalizePack(langPack)
	langCode = normalizeCode(langCode)
	if !langPackNameRE.MatchString(langPack) {
		return domain.LangPack{}, fmt.Errorf("invalid langpack name %q in %q", langPack, filepath.Base(path))
	}
	if len(langCode) > 64 || !langCodeRE.MatchString(langCode) {
		return domain.LangPack{}, fmt.Errorf("invalid language code %q in %q", langCode, filepath.Base(path))
	}
	if version <= 0 || version > math.MaxInt32 {
		return domain.LangPack{}, fmt.Errorf("invalid langpack version %d in %q", version, filepath.Base(path))
	}
	return domain.LangPack{
		LangPack: langPack,
		LangCode: langCode,
		Version:  version,
	}, nil
}

func splitPluralKey(key string) (base, plural string) {
	for _, suffix := range []string{"#zero", "#one", "#two", "#few", "#many", "#other"} {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSuffix(key, suffix), strings.TrimPrefix(suffix, "#")
		}
	}
	return key, ""
}

func setPluralValue(item *domain.LangPackString, plural, value string) {
	switch plural {
	case "zero":
		item.ZeroValue = value
	case "one":
		item.OneValue = value
	case "two":
		item.TwoValue = value
	case "few":
		item.FewValue = value
	case "many":
		item.ManyValue = value
	case "other":
		item.OtherValue = value
	}
}

func unquoteTDesktop(s string) string {
	v, err := strconv.Unquote(`"` + s + `"`)
	if err != nil {
		return s
	}
	return v
}
